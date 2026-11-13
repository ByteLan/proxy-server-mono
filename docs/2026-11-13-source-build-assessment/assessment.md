# 从源码构建与自编译更新评估

评估日期：2026-11-13。

Go 程序可以用标准库 `os/exec` 启动 Git 和 Go 工具链，实现拉取源码后编译新的自身程序。目标机需要已安装的 Git、满足 `go.mod` 要求的 Go 工具链及源码读取权限。编译后的 server 二进制本身不携带编译器；当前代码未实现源码更新子命令。

官方依据：[os/exec](https://pkg.go.dev/os/exec)、[Go 工具链选择](https://go.dev/doc/toolchain)、[go install](https://go.dev/ref/mod#go-install)、[Go 内存软限制](https://go.dev/doc/gc-guide#Memory_limit)。

## 推荐的部署方式

本仓库更适合在开发机本地编译，再由维护者手动上传 GitHub Release，节点只下载已编译程序：

```text
开发机 checkout 固定 commit → 本地构建/校验 → 手动 Release
                                                   ↓
                                      VPS 安装脚本 / update 命令
```

当前生效的发布与更新不变式见 [免编译发布安装与自更新](../../product-spec/发布与安装升级/免编译发布安装与自更新.md)。

## 当前仓库的最少变更路径

当前 `go.mod` 的 module 为 `proxy-mono`，内部 import 使用 `proxy-mono/...`。已 checkout 到本地时，`go build -tags stdjson,gjson ./cmd/...` 可以正常工作。

`internal/webui/dist` 已提交到 Git，并由 `go:embed` 编入管理端。直接构建两个 Go 程序可复用这些资源，不需要 Node/npm；修改 React 后仍需在开发机运行前端构建并提交更新的 `dist`。从已固定 commit 的 checkout 中，可执行 `GOMAXPROCS=1 GOFLAGS="-p=1" GOTOOLCHAIN=auto make build-go`：

```bash
mkdir -p bin
CGO_ENABLED=0 GOMAXPROCS=1 GOTOOLCHAIN=auto \
  go build -p 1 -tags stdjson,gjson -trimpath \
  -o bin/proxy-server ./cmd/proxy-server
CGO_ENABLED=0 GOMAXPROCS=1 GOTOOLCHAIN=auto \
  go build -p 1 -tags stdjson,gjson -trimpath \
  -o bin/proxy-manage-server ./cmd/proxy-manage-server
```

## 1 核 / 1 GiB 的边界与隔离冷构建抽样

日常运行和现场编译是不同负载。首次源码构建还需要模块缓存、工具链和临时磁盘空间；SQLite 依赖的编译有较大开销。`-p 1` 与 `GOMAXPROCS=1` 可以限制构建并发，`GOMEMLIMIT` 仅为各 Go 进程的内存软目标，无法保证整个构建进程树低于 1 GiB。

2026-11-13 在 macOS arm64、Go 1.27.3，从干净源码快照开始，新建独立 `GOCACHE`，使用 `CGO_ENABLED=0 GOMAXPROCS=1 GOMEMLIMIT=384MiB` 和 `-p 1`：

| 构建 | 耗时 | `time -l` 最大 RSS |
|---|---|---|
| 管理端，空构建缓存 | 65.456 秒 | 451.91 MiB |
| 节点端，复用上述隔离缓存 | 2.345 秒 | 176.86 MiB |

384 MiB 软预算下观测到 451.91 MiB RSS，印证生产上应坚持开发机本地构建并分发已编译二进制，而非在 1 GiB 节点现场源码编译。
