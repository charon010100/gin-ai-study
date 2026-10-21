# 用 Gin 从零搭建一个最基础的网站

> 写给「学过 Gin 语法，但没独立搭过一个站」的 Go 程序员。
>
> 每个 `stepNN-*` 目录都是**一个完整可运行的项目**，不是代码片段。
> 你可以逐级往下读，也可以直接进任何一步 `go run .` 看效果。

---

## 一、先说清楚：这个教程不做什么

**不做的**：
- 不讲 Gin 的语法（`c.JSON`、`c.Param`、`c.Bind` 这些你已经会了）
- 不讲 Go 语言本身
- 不追求功能多，不写博客的增删改查

**要讲的**：一个能上线的网站骨架，是由哪些**决策**构成的，以及为什么这么决策。

因为你已经会 Gin 了，真正卡住你的不是 API，而是这些没人明说的问题：

| 你可能遇到的困惑 | 在哪一步回答 |
| --- | --- |
| 所有路由都写在 `main.go` 里，写到 200 行怎么办？ | Step 2 |
| `r.Run(":8080")` 到底有什么问题？为什么别人不这么写？ | Step 3 |
| 接口返回格式五花八门，前端怎么处理？ | Step 4 |
| URL 里的 `:id` 和静态资源路径打架了？ | Step 5 |
| 中间件为什么有时候拿不到值？`c.Next()` 到底什么语义？ | Step 6 |
| `c.JSON` 之后代码还在跑，日志打印了两次？ | Step 4、Step 6 |
| 数据为什么存在 DB 里还要存 Redis 一份？ | Step 8 |
| 为什么线上必须关掉 DebugMode？ | Step 3、Step 10 |

---

## 二、路线图

每一步只增加**一个**新概念，并且都会说明「上一步的做法哪里会出问题」。

| 步骤 | 目录 | 新增概念 | 解决的问题 |
| --- | --- | --- | --- |
| 1 | `step01-minimal` | 最小可运行服务 | 确认环境、理解启动流程 |
| 2 | `step02-handler` | 路由与处理器分离 | `main.go` 不再无限膨胀 |
| 3 | `step03-server` | 配置 + 优雅关停 | 硬编码、重启丢请求 |
| 4 | `step04-response` | 统一响应体与错误处理 | 前端无法统一处理返回值 |
| 5 | `step05-routes` | 路由分组与静态资源 | URL 冲突、路由混乱 |
| 6 | `step06-middleware` | 中间件与 Context 传递 | 日志、鉴权无处安放 |
| 7 | `step07-model` | 接入数据库（GORM） | 内存假数据无法持久化 |
| 8 | `step08-redis` | 接入 Redis 缓存 | 读多写少时的性能问题 |
| 9 | `step09-layout` | 完整工程分层 | 项目变大后的可维护性 |
| 10 | `step10-deploy` | 生产化配置与部署 | 上线前必须做的事 |

**建议节奏**：Step 1–3 一次性读完（这是骨架），Step 4–6 慢慢读（这是最容易踩坑的地方），
Step 7 之后按需。

---

## 三、准备环境

```bash
go version          # 需要 1.22 或更高
```

每个步骤目录独立，进入后执行：

```bash
cd step01-minimal
go mod tidy         # 第一次会下载依赖
go run .
```

然后用浏览器或 curl 访问：

```bash
curl -i http://localhost:8080/ping
```

> **关于 `go.sum`**：本教程的目录里没有提交 `go.sum`，因为它由 `go mod tidy`
> 根据你本机的依赖解析结果生成。第一次运行前必须执行 `go mod tidy`，
> 否则会报 `missing go.sum entry`。

---

## 四、阅读方式

每步目录里有一个 `NOTES.md`，回答三个问题：

1. **这一步解决了什么问题**（上一步的痛点）
2. **代码里每个决策的理由**（为什么这样写，不那样写）
3. **常见的错法**（如果你这样写，会出现什么现象）

代码里的注释也按这个思路写：**只解释「为什么」，不解释「是什么」**。
`// 返回 JSON` 这种注释没有价值，`// Data 用 any 而不是具体类型，是为了让所有接口共用一个响应结构` 才有。

---

## 五、十步说明（按需跳转）

| 步 | 主题 | 一句话总结 | 说明 |
| --- | --- | --- | --- |
| 1 | `step01-minimal` | 服务是怎么起来的 | [NOTES](step01-minimal/NOTES.md) |
| 2 | `step02-handler` | 建立依赖方向；`gin.Context` 必须用指针 | [NOTES](step02-handler/NOTES.md) |
| 3 | `step03-server` | 配置分层 + 四个超时 + 优雅关停 | [NOTES](step03-server/NOTES.md) |
| 4 | `step04-response` | 统一响应体；内部错误永不外泄 | [NOTES](step04-response/NOTES.md) |
| 5 | `step05-routes` | 分组是安全机制；路由冲突启动即 panic | [NOTES](step05-routes/NOTES.md) |
| 6 | `step06-middleware` | `c.Next()` 的洋葱模型；`Use` 顺序陷阱 | [NOTES](step06-middleware/NOTES.md) |
| 7 | `step07-model` | GORM 接入；为什么不能在 handler 里直接用 DB | [NOTES](step07-model/NOTES.md) |
| 8 | `step08-redis` | 缓存一致性；`redis.Nil` 不是故障 | [NOTES](step08-redis/NOTES.md) |
| 9 | `step09-layout` | 完整分层、依赖倒置、事务边界 | [NOTES](step09-layout/NOTES.md) |
| 10 | `step10-deploy` | 多阶段构建、探针、优雅下线、上线清单 | [NOTES](step10-deploy/NOTES.md) |

### 每一步都不依赖前一步的代码

每个目录都是独立的 module，可以单独 `go run .`。这样你可以直接跳到感兴趣的一步，
不必从 Step 1 一路跑过来。

代价是有些代码重复（比如 `config` 包在几步里长得一样）。这是**刻意的**：
教程里每步都应该是"完整可运行的整体"，而不是"在上一版基础上打个补丁"——
后者会让你在阅读时不断往回翻。

### 环境依赖

| 步 | 需要什么 |
| --- | --- |
| 1–6 | 只要 Go |
| 7–9 | 只要 Go（用 SQLite 纯 Go 驱动，不需要装数据库）；Step 8 需要 Redis |
| 10 | Go + Docker + Docker Compose |

### 遇到诡异问题先看这里

[CHEATSHEET.md](CHEATSHEET.md) 按「现象 → 原因 → 怎么查」整理了十步里最容易咬人的问题。
比从头读 NOTES 快得多。

### 请务必自己跑一遍

本教程的代码是按 Gin v1.10 的行为写的，但没有在这个环境里实际编译过
（写它的执行环境里 shell 被策略限制，无法运行 `go build`）。
如果你跑的时候发现和文档描述不一致——**那是文档的问题，以你看到的实际输出为准**。

