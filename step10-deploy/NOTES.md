# Step 10：把「本地能跑」变成「线上能跑」

## 这一步解决了什么问题

前九步做出来的是一个能在你笔记本上 `go run .` 跑通的服务。它和线上服务之间的差距，
不在功能，而在这六件没人会主动告诉你的事：

1. **产物怎么打包**——你的机器上有 Go 1.22、有 libc、有 `curl`，服务器上不一定有。
2. **进程怎么被关掉**——`Ctrl+C` 是信号，`docker stop` 也是信号，但默认行为是「立刻死」。
3. **怎么知道自己活着**——编排系统不会读你的日志，它只会敲一个 HTTP 端点。
4. **表结构谁来改**——`AutoMigrate` 在本地很爽，在线上是没有版本、没有回滚、要求 DDL 权限的东西。
5. **配置从哪来**——写死在代码里，就意味改一个密码要重新发一次版。
6. **怎么知道自己没被打穿**——root 跑容器、密码进了镜像、pprof 暴露在公网。

这一步把六件事各给一个最小可用的答案，并说明每个答案为什么是这样、不这样会出什么事故。

---

## 目录结构

```
step10-deploy/
├── go.mod
├── .dockerignore                       # 放在构建上下文根目录，不是 deploy/ 下
├── cmd/server/main.go                  # 启动、信号处理、优雅关停
├── internal/
│   ├── config/config.go                # 默认值 → 文件 → 环境变量，逐层覆盖
│   ├── store/store.go                  # MySQL / Redis 连接与探活
│   ├── model/article.go                # 一张表，给迁移一个真实对象
│   ├── handler/handler.go              # /healthz、/readyz、/api/v1/articles
│   └── router/router.go                # 路由 + 结构化访问日志
├── configs/config.example.yaml         # 可选项清单与含义（默认不加载）
└── deploy/
    ├── Dockerfile                      # 多阶段、非 root、distroless
    ├── docker-compose.yml              # app + mysql + redis + migrate + nginx
    ├── nginx.conf                      # 反向代理、gzip、转发头
    ├── .env.example
    └── migrations/
        ├── 0001_init.up.sql
        └── 0001_init.down.sql
```

关于 `.dockerignore` 的位置，有一处与常见教程不同，这里说明清楚：

> 它的位置由**构建上下文**决定，不是由 Dockerfile 的位置决定。
> 本项目用 `docker build -f deploy/Dockerfile .` 构建，上下文是项目根，
> 所以 `.dockerignore` 必须在项目根。如果把它放进 `deploy/`，
> Docker 根本不会读它——上下文里的 `.git`、`.env` 照样会被打包发给 daemon。

---

## 核心决策

下面每条都按「方案 / 理由 / 不这样写出什么问题」写。

### 决策 1：多阶段构建

**方案**

```dockerfile
FROM golang:1.22-alpine AS builder     # 编译阶段，有完整工具链
...
COPY --from=builder /out/server /server

FROM gcr.io/distroless/static-debian12:nonroot   # 运行阶段，只有一个二进制
```

**理由**

编译产物和编译环境是两回事。生产环境需要的是那一个静态二进制，
不需要 Go 工具链、源码、`go.sum`、构建缓存。

**不这样写出什么问题**

单阶段构建会把整个 Go 工具链和你的源码打进生产镜像：

- 镜像体积大一个数量级（带完整工具链的镜像在 900MB 量级，静态二进制加 distroless
  基础层在 20MB 上下——这两个数是量级估算，**具体数值取决于版本，我没有实测**）；
- 每次发布都要传输、存储、拉取这些内容，发布变慢；
- 容器一旦被突破，攻击者可以直接读源码；
- 源码和工具链都是额外的攻击面（编译器、包管理器历史上都有过漏洞）。

### 决策 2：`CGO_ENABLED=0` + distroless

**方案**

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server
FROM gcr.io/distroless/static-debian12:nonroot
```

**理由**

distroless 的 `static` 变体里**没有 glibc、没有 musl、没有 shell**。
任何动态链接的二进制在里面都跑不起来。`CGO_ENABLED=0` 让 Go 生成完全静态的二进制
（DNS 解析等原本可能走 cgo 的地方改用纯 Go 实现）。

顺带三个编译参数：

| 参数 | 作用 |
| --- | --- |
| `-trimpath` | 二进制里不记录 `/home/you/project/...` 这类绝对路径，缩小体积且不泄露构建机结构 |
| `-ldflags "-s"` | 去掉符号表 |
| `-ldflags "-w"` | 去掉 DWARF 调试信息 |

`-s -w` 合计能减小不少体积，**代价是线上 panic 的堆栈没有行号**。
有的团队只在正式发布用、staging 保留调试信息，这是个取舍，没有唯一答案。

**不这样写出什么问题**

最典型的报错是启动时：

```
exec /server: no such file or directory
```

文件明明在那儿。这个报错其实是在说「找不到动态加载器（`/lib/ld-musl-*.so.1`）」，
而不是「找不到 /server」。它会浪费你半小时去检查 COPY 路径。

不用 distroless 而用 `alpine` 的问题是：里面有 `sh`、`apk`、`wget`，
被入侵后攻击者可以下载工具、横向移动。distroless 让「进了容器也做不了什么」成为默认状态。
代价是排障困难——没有 shell 不能 `docker exec -it ... sh`，需要时可以临时换
`:debug` 变体或 alpine 镜像来定位。

### 决策 3：非 root 运行

**方案**

```dockerfile
USER nonroot:nonroot
```

**理由**

容器里的 root 和宿主机的 root 是**同一个 UID 0**。一旦出现内核漏洞、
或者错误的挂载（把 `/var/run/docker.sock` 挂进容器），攻击者就可能从容器逃逸到宿主机。
非 root 用户不能安装软件、不能改系统文件、不能绑定低端口，逃逸后的影响面小得多。

**不这样写出什么问题**

容器默认以 root 运行，而且大多数人不会去想这件事——直到出现「应用在容器里写不了临时目录」
或者安全扫描报出「Container is running as root」。

注意 `:nonroot` 这个 tag 已经把默认用户设为 `nonroot`（UID 65532），
Dockerfile 里再显式写一次 `USER`，是为了不依赖 tag 的隐式行为：
哪天有人把 tag 换成 `:latest`（默认 root），这个安全属性就悄悄消失了。

### 决策 4：构建缓存顺序

**方案**

```dockerfile
COPY go.* ./          # 先拷依赖清单
RUN go mod download   # 再下载依赖
COPY . .              # 最后拷源码
```

**理由**

Docker 的层缓存按「这一层的输入有没有变」判断能否复用。源码几乎每次提交都变，
而 `go.mod` / `go.sum` 很久才变一次。把稳定的内容放在前面，
就能让「下载依赖」这一层长期命中缓存。

**不这样写出什么问题**

写成 `COPY . .` 然后 `RUN go mod download`：

```dockerfile
COPY . .
RUN go mod download    # 改一个空格，这层缓存就失效，重新拉几百 MB
```

每次改一行代码都要重新下载全部依赖，构建从 20 秒变成 3 分钟。
在 CI 里这意味着每次提交多花几分钟、多消耗几分钟的构建机时间。

补充一个细节：`COPY go.* ./` 而不是 `COPY go.mod go.sum ./`。
后者在 `go.sum` 尚未生成（刚 clone、还没跑过 `go mod tidy`）时会直接构建失败。

### 决策 5：`GIN_MODE=release`

**方案**

镜像里 `ENV GIN_MODE=release`，代码里再按 `APP_ENV` 显式 `gin.SetMode` 一次。

**理由**

Debug 模式下 gin 会：

- 启动时打印完整路由表（`[GIN-debug] GET /api/v1/admin/users ...`）——等于把接口清单
  交给任何能看到启动日志的人，而路由表里往往还包括后台管理路径；
- 每个请求有额外的调试开销；
- 打印 `WARNING: This is a development build` 之类的提示。

gin 自身会读取 `GIN_MODE` 环境变量，所以镜像里设一次就够。代码里再显式设一次，
是为了让「模式由什么决定」在代码里可见，而不是藏在一个 IMAGE 的 ENV 里没人注意到。

**不这样写出什么问题**

最容易出的事故是**部署时忘了设**，于是线上跑着 debug 模式：
日志里全是路由表，性能和本地开发版一样，而且没人会去看那一行 warning。
有的团队把这条写进上线检查清单，就是为了防这个。

### 决策 6：配置全部走环境变量，配置文件不进镜像

**方案**

优先级：代码默认值 < `CONFIG_FILE` 指定的文件 < 环境变量。
镜像里只有默认值，没有任何真实配置。

**理由**

12-factor 的核心一条：**构建产物与环境无关**。同一个镜像应该能在 dev / staging / prod 跑，
区别只在注入的配置。否则「这个镜像是给哪个环境用的」这件事只能靠 tag 名来猜。

另外两个现实原因：

- 配置文件进了镜像层，`docker history`、`docker inspect` 都可能看到里面的密码；
- 密码轮换后，旧密码永久留在镜像历史里，删不掉。

**不这样写出什么问题**

把 `config.yaml`（含数据库密码）`COPY` 进镜像后：

1. 改一个密码 = 重新构建 + 推送 + 发布，紧急轮换密码时根本来不及；
2. 密码泄露后无法彻底清除，只能重建所有历史镜像；
3. 出问题时无法确认线上跑的到底是哪份配置——它固化在镜像里，
   而不是在 compose / manifest / secret 里可审计。

敏感值应当走 K8s Secret、docker secret 或云平台参数仓库，以环境变量或只读文件注入。

### 决策 7：liveness 与 readiness 必须分开

**方案**

- `/healthz`：只返回 200，**不检查任何依赖**；
- `/readyz`：检查 MySQL 和 Redis，任一不可用返回 503。

**理由**

两个端点回答的是两个不同的问题：

| 端点 | 问题 | 失败后的动作 |
| --- | --- | --- |
| liveness | 「这个进程要不要被**重启**？」 | 杀掉重启 |
| readiness | 「现在能给它**转发流量**吗？」 | 从负载均衡里摘掉，不重启 |

**不这样写出什么问题**

这是本步最重要的一条，因为它的故障模式很反直觉：**在 liveness 里检查数据库，
会把一次数据库抖动放大成全站故障**。

过程是这样的：

1. MySQL 因为一次慢查询或主从切换，抖动 20 秒；
2. 所有实例的 liveness 探测失败；
3. 编排系统认为容器坏了，全部杀掉重启；
4. 新容器启动后依然连不上数据库（因为抖动还没结束），又被杀掉；
5. 几十个实例同时反复重启，重启带来的连接风暴把数据库彻底压垮；
6. 本来只是一次 20 秒的抖动，变成十几分钟的全站不可用。

业内把现象叫 **restart storm**（重启风暴）。

反过来，如果合成一个端点、又不检查依赖，那么数据库已经挂了，负载均衡还在往里转发流量，
用户看到的是持续 500，而不是被自动切到健康实例（多副本场景）。

**两个都错，所以必须分开。**

### 决策 8：不用 `AutoMigrate`，迁移作为独立步骤

**方案**

用 `golang-migrate` 执行版本化 SQL，迁移作为 compose 里一个**一次性的服务**，
在 app 启动之前跑完；`app` 用 `condition: service_completed_successfully` 依赖它。

**理由**

`AutoMigrate` 的问题不是「不好用」，而是**能力边界恰好不覆盖生产需要的部分**：

- 它只会加表和加列，**不会删列、不会改类型、不会改索引名**；
- 它没有版本概念，无法回答「线上现在是什么结构」，自然也无法回滚；
- 它要求应用账号有 `ALTER` 权限，而生产账号通常只该有 DML 权限；
- 对有数据的表做结构变更时，它可能触发锁表，而它不会告诉你预计锁多久。

至于「迁移放启动时还是独立步骤」，两种方案各有取舍：

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| 启动时执行（应用内） | 部署只有一步，不会漏 | 多副本同时启动会**并发执行迁移**，可能互相冲突；应用账号需要 DDL 权限；迁移失败会让新版本起不来，回滚麻烦 |
| 独立步骤（本步采用） | 一次只有一个执行者；失败时应用根本不启动；可以用独立的高权限账号；有明确的版本记录 | 部署流程多一步，需要编排系统支持「先跑完任务再启动」 |

本项目选独立步骤，因为在 compose / K8s 里这一步很容易表达，
而「多副本并发迁移」是个很难复现、后果又很严重的问题。

**不这样写出什么问题**

用 `AutoMigrate` 跑上几个月后：

- 数据库结构和代码里的模型定义已经不一致（手工改过的列、加过的索引、遗留的旧列）；
- 没有可信的结构快照，新建一套 staging 环境时结构可能和生产不一样；
- 想回滚时发现没有 down 脚本，也没有版本记录；
- 某次 AutoMigrate 在亿级表上加了索引，锁表十分钟，站点挂了。

另外，本项目的迁移文件里**必须有 down 脚本**。只写 up 不写 down 的话，
等到需要回滚时才发现没有退路——而「需要回滚」通常正是出事的时候。

### 决策 9：`CMD` 用 exec 形式，配合 SIGTERM 做优雅关停

**方案**

```dockerfile
CMD ["/server"]        # exec 形式，二进制是 PID 1
```

```go
sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
...
srv.Shutdown(shutdownCtx)
```

**理由**

`docker stop` 的流程是：发 `SIGTERM` → 等超时（默认 10 秒）→ 发 `SIGKILL`。
优雅关停的前提是**你的进程真的收到了 SIGTERM**。

**不这样写出什么问题**

写成 shell 形式：

```dockerfile
CMD ./server          # 等价于 /bin/sh -c "./server"
```

此时 PID 1 是 `sh`，你的程序是它的子进程。`docker stop` 把 SIGTERM 发给 PID 1（`sh`），
而 `sh` 默认**不会转发**信号给子进程。结果：

- 你的程序收不到 SIGTERM，优雅关停代码根本没执行；
- 死等 10 秒后收到 SIGKILL；
- 表现是每次发布/重启，接口都有几秒钟的报错，日志里找不到「收到停止信号」这一行。

更直接的证据：distroless 镜像里**没有 `/bin/sh`**，写成 shell 形式会立刻启动失败
（`exec: "/bin/sh": stat /bin/sh: no such file or directory`）。所以这个错误至少不会
在 distroless 上被带到生产。

还有一点容易漏：`Shutdown` 的等待时间必须**小于**编排系统的 stop 超时。
本项目的 `SHUTDOWN_TIMEOUT=15`，而 Docker 默认只等 10 秒，所以 compose 里显式写了
`stop_grace_period: 20s`。不写的话，你的 15 秒优雅关停永远等不到执行完就被 SIGKILL 了。

### 决策 10：日志写 stdout，用结构化格式

**方案**

`slog.NewJSONHandler(os.Stdout, ...)`，生产 JSON、开发 Text。

**理由**

容器的日志约定是「应用写 stdout/stderr，由运行时负责收集和轮转」。
应用自己往容器内写文件，`docker logs` 看不到，容器销毁时日志一起消失。

用 JSON 的理由：日志会被采集系统解析，纯文本一旦遇到带换行的错误信息就会错行，
把一个字段的值串到下一个字段里去。结构化日志不会有这个问题。

**不这样写出什么问题**

- 写文件：容器重启后日志丢失，出问题无法回溯；
- 纯文本：日志平台里的字段提取全靠正则，一条带堆栈的日志就能让解析全乱；
- 把 `Query` 和请求体写进访问日志：里面可能有邮箱、token、密码。
  日志系统通常权限更宽、留存更久，等于把敏感数据的泄露面扩大了一倍。

### 决策 11：`depends_on` 用 `condition`，`restart` 用 `unless-stopped`

**方案**

```yaml
depends_on:
  mysql:
    condition: service_healthy
  migrate:
    condition: service_completed_successfully
restart: unless-stopped
```

**理由**

`depends_on` 默认只保证「启动顺序」，**不保证依赖已就绪**。MySQL 容器「启动了」
和「能接受连接了」之间隔了几十秒（要先初始化系统表）。用 `condition: service_healthy`
才能真正等到健康检查通过。

`migrate` 用 `service_completed_successfully` 而不是 `service_started`：
后者只保证迁移容器启动过，它可能还在跑、甚至马上要失败。前者要求它以退出码 0 结束。

`restart: unless-stopped` 与 `always` 的区别：手动 `docker stop` 停掉的容器
不会被自动拉起来，这是符合直觉的行为；`always` 会。

**不这样写出什么问题**

只用启动顺序时，app 会比 MySQL 先起来，连不上就退出，靠 restart 反复重试——
能跑起来，但启动日志里一串「connection refused」，出问题排查时非常干扰判断。

`migrate` 用 `service_started` 时，app 可能在表还没建好时就开始接受请求，
表现为间歇性的 500（`table doesn't exist`），而且只在部署的头几秒出现，非常难复现。

### 决策 12：nginx 转发头与「IP 不可信」的前提

**方案**

```nginx
proxy_set_header X-Real-IP       $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

**理由**

应用需要知道真实客户端 IP（限流、评论黑名单、审计日志都依赖它），
还需要知道原始协议（生成绝对链接、决定是否重定向到 HTTPS）。

**不这样写出什么问题**

这里有个必须理解的前提：**`X-Forwarded-For` 是客户端能自己伪造的。**

任何人在发请求时加一行 `X-Forwarded-For: 1.2.3.4`，都能让应用以为请求来自 1.2.3.4。
而 `$proxy_add_x_forwarded_for` 的语义是「保留客户端传来的值，再追加真实 IP」，
所以：

- 应用取**第一个** IP → 取到伪造值；
- 应用取**最后一个** IP → 取到 nginx 追加的真实 IP。

如果应用里的限流、黑名单直接信任了第一个 IP，攻击者每次伪造一个新 IP 就能绕过限流——
这类防护就形同虚设。

正确做法是明确「信任边界」：应用只信任来自已知代理的转发头。
nginx 前面还有 CDN 时，要配 `set_real_ip_from` + `real_ip_header` + `real_ip_recursive on`，
由 nginx 负责清掉伪造部分。**只有在可信代理后面，`X-Forwarded-For` 才可信。**

---

## 动手实验

以下命令都需要本机能跑 Docker。**我无法在当前环境执行它们**（宿主机沙箱禁止了 shell），
所以下面的「你应该看到」是根据配置推导的预期结果，**不是实测输出**。

首次构建前，请先在 `step10-deploy/` 下执行 `go mod tidy` 并**把生成的 `go.sum` 一起提交**。
没有 `go.sum` 时，`go mod download` 会在构建过程中现场解析依赖并写入校验和，
通常能构建成功，但解析结果可能随上游变化——同样的代码在不同时间构建出的镜像可能不同，
这与「构建可复现」的目标相悖。

### 实验 1：本地跑通全链路

```bash
cd step10-deploy
docker build -f deploy/Dockerfile -t blog-step10:dev .

cd deploy
cp .env.example .env      # 把里面的 change-me 都改掉
docker compose up -d --build
docker compose ps
```

预期：`mysql`、`redis`、`app`、`nginx` 都是 `healthy`，`migrate` 是 `exited (0)`。

```bash
curl -s http://localhost:8080/api/v1/articles
curl -s http://localhost:8080/readyz
```

注意：不要用 `curl http://localhost:8080/healthz` 期望 200。
nginx 配置里对 `/healthz` 做了内网白名单，从宿主机访问会得到 **403**——
这是预期行为，不是配置错误（理由见 nginx.conf 里 `/healthz` 那段的注释）。
想验证 liveness，用下面的方式从容器内部访问。

### 实验 2：验证非 root

```bash
docker inspect --format '{{.Config.User}}' blog-step10:dev
```

预期输出 `nonroot:nonroot`。再试：

```bash
docker run --rm -it blog-step10:dev sh
```

预期：报错 `exec: "sh": executable file not found`——这正是 distroless 的特性，
没有 shell 可用。这一条同时验证了「镜像里没有可用的本地工具」这个安全属性。

### 实验 3：验证 SIGTERM 真的到达进程

```bash
docker compose stop app
```

预期日志里出现「收到停止信号，开始优雅关停」和「在途请求已处理完毕」，
并且 `docker compose ps` 显示停止耗时明显小于 `stop_grace_period`。

如果**没有**这两行日志、且 stop 卡满 20 秒，说明信号没到进程——
去检查 `CMD` 是不是被写成了 shell 形式。

对比实验：把 Dockerfile 改成 `CMD ./server` 重建，再 `docker compose stop app`，
观察耗时和日志差异（注意：在 distroless 上这会直接启动失败）。

### 实验 4：验证 liveness 与 readiness 的区别

```bash
docker compose stop mysql
sleep 5

# liveness 仍然正常：直接问应用自己（app 没对外暴露端口，用 exec 进容器跑）
docker compose exec app /server -healthcheck; echo "exit=$?"   # 预期 exit=0

# readiness 表现为 503
curl -i http://localhost:8080/readyz      # 预期 503，checks.mysql = down
```

**关键观察**：`docker compose ps` 里 `app` 仍然是 `healthy` 或至少没有被反复重启。
这就证明了「依赖挂了不会导致容器被重启」——如果 liveness 里检查了数据库，
这里会看到 `Restarting` 状态不断循环。

顺便验证 nginx 的白名单：`curl -i http://localhost:8080/healthz` 应返回 **403**
（从容器外访问被拒），而从 nginx 容器内部访问则是 200：

```bash
docker compose exec nginx wget -qO- http://127.0.0.1/healthz
```

恢复：

```bash
docker compose start mysql
```

### 实验 5：验证镜像里没有敏感信息

```bash
# 看镜像历史里有没有密码
docker history --no-trunc blog-step10:dev | grep -i password   # 预期无输出

# 看最终镜像里有哪些文件
docker run --rm blog-step10:dev ls /      # 会失败，因为没 shell
docker export $(docker create blog-step10:dev) | tar -tvf - | head -50
```

预期：只有 `/server` 和 distroless 自带的那点东西（ca-certificates、passwd 等），
没有源码、没有 config 文件、没有 go.sum。

### 实验 6：验证 X-Forwarded-For 可被伪造

```bash
# 直连应用（绕过 nginx）不行，因为 app 没暴露端口。
# 通过 nginx 发一个伪造头（用 /readyz，因为 /healthz 被白名单挡在外面）：
curl -s -H "X-Forwarded-For: 1.2.3.4" http://localhost:8080/readyz
```

然后看后端记录的 IP。真正要验证的是：
**后端如果信任第一个 IP，就会把 1.2.3.4 当成真实客户端**。
可以用 `docker compose logs app | grep ip` 观察访问日志里的 `ip` 字段。
（当前代码用的是 `c.ClientIP()`，gin 默认会解析转发头，
所以你会看到 `ip=1.2.3.4`——这就是想让你看到的伪造效果。）

**这一点不是理论问题**：如果限流按 `c.ClientIP()` 计数，攻击者每个请求带一个新的
`X-Forwarded-For` 就能无限绕过。修法是让 gin 只信任可信代理：

```go
// 只信任来自这些网段的转发头，其余一律用 TCP 对端地址
_ = engine.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12"})
// 完全不信任转发头则传 nil
```

不设时 gin 的默认行为取决于版本，**不要依赖默认值**——这是安全相关的行为，
应当显式配置。

### 实验 7：依赖漏洞扫描

```bash
cd step10-deploy
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

预期：会列出依赖中已知漏洞及其调用链。注意它只报告**能被调用到**的漏洞，
所以「有输出」不等于「你有问题」，要顺着调用链看是否真的可达。

### 实验 8：验证迁移可回滚

```bash
cd deploy
docker run --rm -v "$PWD/migrations:/migrations" --network "$(docker compose ps -q mysql | xargs docker inspect -f '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}')" \
  migrate/migrate -path=/migrations -database "mysql://blog:密码@tcp(mysql:3306)/blog" down 1
```

（网络参数在不同 compose 版本下写法有差异，更省事的做法是
`docker compose run --rm migrate -path=/migrations -database ... down 1`。）

预期：`articles` 表被删除。再执行 `up` 应该能恢复——**这证明迁移是可逆的**，
而不是只能前进。

---

## 上线检查清单

### 容器与进程

- [ ] 镜像以非 root 运行（`docker inspect` 的 `Config.User` 不为空、不为 `root`）
- [ ] `CMD` 是 exec 形式（`["/server"]`），并已实测 `docker stop` 能在秒级完成
- [ ] `stop_grace_period` / `terminationGracePeriodSeconds` **大于**应用的 `SHUTDOWN_TIMEOUT`
- [ ] 镜像里没有 shell、编译器、包管理器（distroless / scratch）
- [ ] 镜像里没有源码、`.git`、`.env`、任何配置文件
- [ ] `docker history` 里没有明文密钥
- [ ] 基础镜像 tag 固定（不要用 `latest`），并已订阅其安全更新

### 配置与密钥

- [ ] 所有环境相关的值都能通过环境变量覆盖，改配置不需要重新构建
- [ ] 数据库密码、Redis 密码、JWT 密钥来自 secret 或环境变量，不入镜像、不入 git
- [ ] 生产环境启动时校验空密码并拒绝启动（本项目 `config.validate` 已实现）
- [ ] `.env` 已在 `.gitignore` 里
- [ ] 确认 `GIN_MODE=release`（或 `APP_ENV=prod`）在线上生效，日志里无路由表

### 健康检查与生命周期

- [ ] liveness **不**检查数据库/缓存等外部依赖
- [ ] readiness 检查依赖，且对每个依赖设了独立超时
- [ ] readiness 的响应不包含内部错误详情（主机名、端口、SQL）
- [ ] `/healthz`、`/readyz` 在公网不可访问（或被网关限制）
- [ ] 探针的 `start_period` 足够覆盖冷启动

### 数据库

- [ ] 不使用 `AutoMigrate`，表结构变更走版本化迁移
- [ ] 每个迁移都有可执行的 `down` 脚本
- [ ] 迁移在独立步骤执行，且 app 依赖它成功结束
- [ ] 生产数据库账号**没有** DDL 权限（迁移用另一个账号）
- [ ] 迁移执行前有备份，大表变更评估过锁表时间
- [ ] 连接池参数显式设置（`MaxOpenConns` 不为 0）

### 安全

- [ ] `/debug/pprof` 已关闭或仅内网 + 鉴权（本项目 nginx 直接 404）
- [ ] 依赖漏洞扫描（`govulncheck ./...`）已执行，高危项已处理或评估
- [ ] 日志里没有密码、token、完整邮箱、评论正文
- [ ] 错误响应不包含内部细节（SQL 片段、堆栈、文件路径）
- [ ] 全站 HTTPS，HTTP 301 到 HTTPS，HSTS 已配置（首次上线先用短 `max-age` 验证）
- [ ] 反向代理传了 `X-Forwarded-For`，且应用**明确**只信任可信代理；限流/黑名单依赖的 IP 不可被伪造
- [ ] 安全响应头已配置（`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`、CSP）
- [ ] 容器可写文件系统只读（`read_only: true`，必要时用 `tmpfs` 挂 `/tmp`）

### 可观测性

- [ ] 日志写 stdout，结构化，能被采集系统解析
- [ ] 5xx 与关键业务失败有告警，而不只是「进程活着」
- [ ] 有明确的版本号可查（本项目的 `/readyz` 返回 `version`，由构建时注入）
- [ ] 发布/回滚流程已演练过至少一次

---

## 本步未覆盖、但上线前仍需要的

诚实地说，一个真实项目上线还缺这些，本步刻意不做（否则文件会失控）：

- **TLS 证书自动续期**（certbot / cert-manager）
- **多副本与滚动发布**（K8s Deployment / 蓝绿发布、`maxSurge`/`maxUnavailable`）
- **CI/CD 流水线**（本步只做到 `docker build`）
- **限流与鉴权**（前九步讨论过，部署层面需配合网关或应用中间件）
- **指标与追踪**（Prometheus / OpenTelemetry，日志只能回答「发生了什么」，
  回答不了「P99 延迟是多少」）
- **备份与恢复演练**（`mysqldump` + 定期恢复验证；没验证过的备份等于没有备份）
