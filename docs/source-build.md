# 从源码构建与自编译更新评估

Go 程序可以用标准库 `os/exec` 启动 Git 和 Go 工具链，实现拉取源码后编译新的自身程序。目标机需要已安装的 Git、满足 go.mod 要求的 Go 工具链及源码读取权限。编译后的 server 二进制本身不携带编译器；当前代码尚未实现源码更新子命令。

官方依据：[os/exec](https://pkg.go.dev/os/exec)、[Go 工具链选择](https://go.dev/doc/toolchain)、[go install](https://go.dev/ref/mod#go-install)、[Go 内存软限制](https://go.dev/doc/gc-guide#Memory_limit)。Go 工具链支持根据 go.mod 选择或下载适当版本，前提是机器已有 go 命令和相应网络访问。

## 推荐的部署方式

本仓库更适合在开发机本地编译，再由维护者手动上传 GitHub Release，节点只下载已编译程序：

```text
开发机 checkout 固定 commit → 本地构建/校验 → 手动 Release
                                                   ↓
                                      VPS 安装脚本 / update 命令
```

显式运行 `make publish` 会调用本地发布脚本，需要开发机的 Go/Node 和 GitHub CLI；若复用已提交的前端资源可用脚本 `--skip-web`。它不调用 Actions，也不在服务器上启动编译。源码 commit 需要先提交并推送；实际上传需 GitHub 仓库写权限，流程见 [手动发布指南](releases.md)。

## 当前仓库的最少变更路径

当前 `go.mod` 的 module 为 `proxy-mono`，内部 import 使用 `proxy-mono/...`。不能直接把仓库地址拼成 `go install github.com/ByteLan/proxy-server-mono/cmd/proxy-server@main` 使用；这需要先统一 module 路径及内部引用。已 checkout 到本地时，`go build ./cmd/...` / `go install ./cmd/...` 可以正常工作。

最少改动方式是通过已有 SSH/Git 凭据拉取私有仓库，解析为完整 commit 后 detached checkout，再在临时目录构建。完整 commit 用于清楚记录实际构建的代码；不要在正在运行的服务目录内直接 git pull 和覆盖源码。

`internal/webui/dist` 已提交到 Git，并由 `go:embed` 编入管理端。直接构建两个 Go 程序可复用这些资源，不需要 Node/npm；修改 React 后仍需在开发机运行前端构建并提交更新的 dist。`make build` 会主动重建 React；新增的 `make build-go` 只编译两个 Go server，复用已提交的前端资源。

从已固定 commit 的 checkout 中，可执行 `GOMAXPROCS=1 GOFLAGS="-p=1" GOTOOLCHAIN=auto make build-go`；等价的直接命令为：

```bash
mkdir -p bin
CGO_ENABLED=0 GOMAXPROCS=1 GOTOOLCHAIN=auto \
  go build -p 1 -tags stdjson,gjson -trimpath \
  -o bin/proxy-server ./cmd/proxy-server
CGO_ENABLED=0 GOMAXPROCS=1 GOTOOLCHAIN=auto \
  go build -p 1 -tags stdjson,gjson -trimpath \
  -o bin/proxy-manage-server ./cmd/proxy-manage-server
```

这只构建本机架构的程序，不需要 C 编译器、不执行初始化、不改数据库或节点配置。手动多架构打包仍可使用 `make release`，产物不会自动上传到 GitHub。

## 如果实现源码更新

建议让管理员显式执行更新命令：

1. 检查 Git/Go 与私有仓库只读访问，获取并固定完整 commit。
2. 在独立临时目录 checkout，记录 commit 并校验 `go.mod` / 嵌入资源存在。
3. 用 `go build -p 1` 构建临时二进制；使用 `exec.CommandContext` 传入独立参数，设置超时并避免打印认证信息。
4. 构建和产物检查成功后，才在目标目录准备新程序、保存 `.previous` 并原子替换。
5. 保留配置、数据与协议核心，管理员另行重启服务；管理端数据备份与二进制回退分开处理。

这条路径可以复用当前更新器的替换原则，但需要新增源码取得和构建流程，不能把现有 GitHub Release 下载更新命令描述为源码编译。

## 1 核 / 1 GiB 的边界

日常运行和现场编译是不同负载。首次源码构建还需要模块缓存、工具链和临时磁盘空间；SQLite 依赖的编译有较大开销。`-p 1` 与 `GOMAXPROCS=1` 可以限制构建并发，`GOMEMLIMIT` 仅为各 Go 进程的内存软目标，无法保证整个构建进程树低于 1 GiB。

生产上优先在开发机或独立构建机从源码构建，再分发二进制。若希望 1 GiB 节点现场编译，需要在实际 Linux 主机测量峰值内存、磁盘与构建耗时；不要把低并发参数当成硬件容量保证。

已验证在临时 detached checkout 中、仅使用已有 Go 模块/构建缓存与 `GOPROXY=off`，两种服务均可生成 Linux amd64 静态 ELF，未运行 npm、初始化或重启服务。这项验证不包含远程私库鉴权，也不等于冷缓存资源验证。

## 隔离冷构建抽样

2026-11-13 在 macOS arm64、Go 1.27.3，从干净源码快照开始，新建独立 GOCACHE，使用 `CGO_ENABLED=0 GOMAXPROCS=1 GOMEMLIMIT=384MiB` 和 `-p 1`：

| 构建 | 耗时 | time -l 最大 RSS |
|---|---|---|
| 管理端，空构建缓存 | 65.456 秒 | 451.91 MiB |
| 节点端，复用上述隔离缓存 | 2.345 秒 | 176.86 MiB |

两种构建及 version 命令检查成功，未运行 npm，也未创建服务 DATA_DIR。Go 模块和工具链已预先下载，节点端不是独立冷缓存构建。该 RSS 为计时工具报告的最大驻留值，不是整个进程树同时总内存，且环境并非 Linux 1 核 / 1 GiB。384 MiB 软预算下观测到更高 RSS，进一步说明它不能充当硬容量保证。
