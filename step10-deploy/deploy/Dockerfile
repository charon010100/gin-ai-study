# syntax=docker/dockerfile:1
#
# ── 多阶段构建 ──
#
# 第一阶段（builder）里装的是完整的 Go 工具链和源码，镜像约 900MB 级别；
# 第二阶段（runtime）只拷贝那一个静态二进制，镜像可以压到 20MB 以内。
# 如果用单阶段，等于把编译器、源码、go.sum、构建缓存全部打包进生产镜像：
# 体积大、推送慢、启动慢，而且源码和构建工具本身就是额外的攻击面
# （容器一旦被突破，攻击者能直接拿到你的全部源码）。
#
# stage 命名（AS builder）的目的是让后面可以用 --from=builder 精确拷贝。

FROM golang:1.22-alpine AS builder

# 把工作目录固定下来，后面的 COPY/RUN 都相对于它。
WORKDIR /src

# ── 依赖层 ──
#
# 顺序是这一步最重要的技巧：先 COPY 依赖清单并下载依赖，再 COPY 源码。
#
# Docker 的层缓存按「某一层的输入是否变化」判断能否复用。源码几乎每次提交都变，
# 但 go.mod/go.sum 很久才变一次。如果先 COPY . （全部源码）再 go mod download，
# 那么任何一次改动一行代码都会让「下载依赖」这一层缓存失效，每次构建都重新拉几百 MB。
#
# 用 COPY go.* ./ 而不是 COPY go.mod go.sum ./：
# go.sum 可能还没生成（第一次 clone 时），写成明确文件名会在文件缺失时构建失败；
# go.* 通配只要匹配到 go.mod 就能通过。
COPY go.* ./

# 依赖下载也走层缓存。GOFLAGS 让模块下载更安静些，GOPROXY 可按需替换为国内镜像。
ENV GOFLAGS=-mod=mod \
    GOPROXY=https://proxy.golang.org,direct

RUN go mod download

# ── 源码层 ──
COPY . .

# ── 编译 ──
#
# CGO_ENABLED=0 是必须的：
#   distroless/static 和 scratch 里没有 glibc / musl，任何动态链接的二进制都跑不起来，
#   报错是 "no such file or directory"（看起来像文件不存在，其实是缺动态加载器，
#   这个报错极其容易误导人）。设置 CGO_ENABLED=0 让 Go 使用纯 Go 的实现
#   （包括 DNS 解析器），产出一个完全静态的二进制。
#
# -trimpath   去掉二进制里记录的本地绝对路径（/home/you/project/...），
#             既缩小体积，也避免泄露构建机的目录结构。
# -ldflags "-s -w"
#             -s 去掉符号表，-w 去掉 DWARF 调试信息。
#             两者合计能把二进制减小 25% 左右（具体比例随代码量变化，未实测）。
#             代价是线上 panic 的堆栈里没有行号，所以有的团队只给正式发布用，
#             staging 环境保留调试信息。
# -X main.version=...
#             把版本号注入到变量里，这样 /readyz 能返回当前跑的到底是哪个版本。
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

# ── 运行阶段 ──
#
# distroless/static：只包含证书、时区数据、/etc/passwd 和 CA 根证书，没有 shell、
# 没有包管理器、没有 curl。体积约 2MB，且几乎没有可被利用的本地工具。
#
# 为什么不用 alpine：alpine 有 shell 和 apk，体积约 5MB，被入侵后攻击者
# 能下载工具、横向移动；distroless 让「进了容器也做不了什么」成为默认状态。
# 代价是调试困难（没有 shell，不能 docker exec 进去看），
# 需要排障时用 :debug 变体或临时换回 alpine。
#
# 如果拉不动 gcr.io，可换 cgr.dev/chainguard/static:latest（同样是非 root、无 shell）。
FROM gcr.io/distroless/static-debian12:nonroot

# 从 builder 只拷一个文件出来。
COPY --from=builder /out/server /server

# 声明监听端口。EXPOSE 只是文档性质，不改变实际行为，
# 但让 docker-compose / K8s 的工具有据可依。
EXPOSE 8080

# 非 root 运行。
#
# :nonroot 这个 tag 已经把默认用户设为 nonroot(65532)，这里显式再写一次 USER，
# 是为了让「不以 root 运行」这件事在 Dockerfile 里可见 —— 依赖 tag 的隐式行为，
# 一旦有人换成 :latest（默认 root），这个安全属性就无声消失了。
#
# 为什么重要：容器默认以 root 运行，而容器的 root 和宿主机的 root 是同一个 UID 0。
# 配合内核漏洞或错误的挂载配置（比如把 /var/run/docker.sock 挂进容器），
# 就可能从容器逃逸到宿主机。非 root 用户不具备安装软件、改系统文件、
# 绑定低端口等能力，把逃逸后的影响面压到最小。
USER nonroot:nonroot

# 环境变量只放「与镜像绑定的行为」，不放任何敏感值。
# GIN_MODE=release：关闭 gin 的 debug 输出（路由表、开发警告）。
# 显式设在这里而不是靠 compose 传，是为了保证「这个镜像在任何编排系统里都不会以 debug 模式跑」。
ENV GIN_MODE=release \
    APP_ENV=prod \
    APP_ADDR=:8080

# 声明配置来源，便于阅读者知道有哪些变量。
# 不要在这里写 ENV MYSQL_PASSWORD=xxx —— 环境变量会被 docker inspect 看到，
# 也会被镜像历史记录保留。
ENV MYSQL_HOST=mysql \
    MYSQL_PORT=3306 \
    MYSQL_DATABASE=blog \
    REDIS_ADDR=redis:6379

# ── CMD 必须用 exec 形式（JSON 数组）──
#
# 这一条是容器优雅关停的关键，也是最容易写错的一行。
#
# 写成 CMD ./server（shell 形式）时，Docker 实际执行的是：
#   /bin/sh -c "./server"
# 于是 PID 1 是 sh，你的程序是 sh 的子进程。docker stop 会把 SIGTERM 发给 PID 1（sh），
# 而 sh 默认**不会转发**信号给子进程。结果：
#   你的程序收不到 SIGTERM → 不执行优雅关停 → 等满 stop 超时（默认 10 秒）
#   → 收到 SIGKILL 被强杀。
# 现象是每次发布或重启，接口都会出现几秒钟报错，日志里看不到「收到停止信号」。
#
# 用 exec 形式 ["/server"] 时，二进制直接成为 PID 1，SIGTERM 能直达进程，
# 之后的优雅关停代码才会真正执行。
#
# 另外注意：distroless 里根本没有 /bin/sh，写成 shell 形式会直接启动失败
# （exec: "/bin/sh": stat /bin/sh: no such file or directory）。
CMD ["/server"]

# 容器健康检查。
#
# 用二进制自己的 -healthcheck 参数，而不是 curl/wget —— 这个镜像里没有它们。
# 检查的是 /healthz（liveness），不是 /readyz：
# HEALTHCHECK 的结果会决定容器是否被标记为 unhealthy 并触发重启策略，
# 所以它必须只反映「进程能不能响应」，不能受数据库抖动影响（理由见 NOTES）。
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/server", "-healthcheck"]
