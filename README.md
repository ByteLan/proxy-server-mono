# Proxy monorepo

集中管理多个代理节点，并通过固定订阅链接生成随节点选择变化的 mihomo / Clash Meta YAML。

- `proxy-server`：Go 节点 Agent，管理独立 Hysteria2 / Xray 子进程、首次注册和定期心跳。
- `proxy-manage-server`：Go / Hertz 控制面，SQLite 保存节点与订阅，嵌入 React / Arco Design 前端；不运行代理核心、不转发代理流量。
- `web`：React / TypeScript / Vite / Arco Design 管理界面。

**推荐流程：开发机编译 → 手动发布 GitHub Release → VPS 下载已编译程序。** 目标机无需 Go、Node 或现场编译。

目标是让管理端运行在 Linux 1 核 / 1 GiB 机器上，前端和 Go 程序由维护者在开发机手动编译；**尚未在该规格服务器上做资源、容量或长期稳定性实测**。

## 下载、安装与更新

维护者手动构建并上传 GitHub Release 后，目标机可运行独立安装脚本，下载并校验程序及安装文件，再完成 systemd 配置和初始化；无需克隆源码、安装 Go/Node 或在目标机编译。仓库没有 push 或 tag 触发的构建发布；必须显式执行本地发布命令才能上传可下载程序。安装阶段需要 Python 3、OpenSSL 和 systemd 工具；首次下载脚本使用 curl。私有仓库需要仅该仓库 **Contents: Read-only** 的 fine-grained PAT。

首次安全下载脚本及管理端、节点的完整命令见[发布与更新](docs/releases.md#首次下载并初始化)。已经获得 `install.sh` 后：

```bash
sudo --preserve-env=GITHUB_TOKEN bash ./install.sh manager -- \
  --public-url https://manager.example.com
```

默认使用 `ByteLan/proxy-server-mono` 中维护者手动发布的最新 `main-<sha12>` 预发布；未发布对应资产时安装与更新会失败，不会在服务器上临时编译。`--channel stable` 选择正式 release，`--version TAG` 固定版本。节点安装还需提供固定版本 Hysteria2 / Xray 的二进制、可信 SHA256 和版本信息，不会自动下载最新协议核心。

已有程序支持手动检查和更新：

```bash
/opt/proxy-server/bin/proxy-server version
/opt/proxy-server/bin/proxy-server update --check
sudo /opt/proxy-server/bin/proxy-server update
sudo systemctl restart proxy-server
```

私库更新添加 `--token-file /private/path/github-token`（0600），或向管理员执行的进程安全提供 `GITHUB_TOKEN` / `GH_TOKEN`。管理端使用相应的 `/opt/proxy-manage-server/bin/proxy-manage-server` 和 unit 名。更新不自动重启、不修改配置/数据库/核心；旧程序保存为 `.previous`，数据库须独立备份。[渠道、凭据及回退步骤](docs/releases.md)。

## 本地手动构建

需要 Go 1.25+（版本见 `go.mod`）、Node.js 20+ 和 npm。首次安装前端依赖，然后构建两个二进制：

```bash
npm --prefix web ci
GOTOOLCHAIN=auto make build
GOTOOLCHAIN=auto make test check
```

`make build` 先将 `web/dist` 复制到 `internal/webui/dist`，再构建 `bin/proxy-server` 与 `bin/proxy-manage-server`。目标机器只需要相应二进制；管理端不需要 Node.js、npm、独立前端进程或数据库服务。跨平台构建时可在 `make build` 前设置 `GOOS=linux GOARCH=amd64`（ARM64 使用 `arm64`）。

需要分发到 VPS 时，在工作树干净、当前 commit 已自行 push 后，使用本地手动发布入口：

```bash
gh auth login
GOTOOLCHAIN=auto make publish
```

`make publish` 在开发机编译三种 Linux 架构的六个程序及安装文件，校验清单，再通过 draft 上传完整资产并发布；默认版本为 `main-<sha12>` prerelease，VPS 的默认 main 渠道可获取。它不会自动 commit/push，已发布的同名 tag 不会被覆盖。发布凭据需要仓库写权限，不能复用 VPS 的只读下载 token；实际发布必须以 GitHub API 成功为准。

```bash
# 只在本地构建、校验，不需要 gh 登录，也不上传
GOTOOLCHAIN=auto bash scripts/publish-release.sh --dry-run
# 发布指定正式版本（换成实际版本号）
GOTOOLCHAIN=auto bash scripts/publish-release.sh --tag v0.1.0
```

只需要本地产物时使用 `make release`，输出到 `bin/releases/`，不上传。完整参数与手动发布流程见[发布指南](docs/releases.md#维护者手动构建与发布)。当前 `update` 只下载已发布二进制，没有拉取源码并重新编译的更新功能。

Makefile 对 Go 构建、测试和 vet 使用 Hertz 的 `stdjson` build tag；手动执行对应命令时也应添加 `-tags stdjson,gjson`，避免依赖 Sonic 的特定 Go 编译器版本支持。

不重建前端时可使用纯 Go 路径，复用已提交的嵌入资源，不需要 Node：

```bash
GOMAXPROCS=1 GOFLAGS="-p=1" GOTOOLCHAIN=auto make build-go
```

Go 拉取源码、自编译及 1 GiB 构建限制见 [源码构建评估](docs/source-build.md)。当前未实现源码自更新命令。

## 本地体验管理端

```bash
export ADMIN_TOKEN="$(openssl rand -hex 32)"
export ENROLL_TOKEN="$(openssl rand -hex 32)"
export DATA_DIR="$(mktemp -d)"
export LISTEN=127.0.0.1:8080
export PUBLIC_URL=http://127.0.0.1:8080
./bin/proxy-manage-server
```

访问 `http://127.0.0.1:8080`，使用自己设置的 `ADMIN_TOKEN` 登录。登录 key 存在浏览器会话存储中。管理端首次启动会在 `DATA_DIR/master.key` 创建加密密钥；也可以通过 `MASTER_KEY` 指定 base64 编码的 32 字节密钥。不要把运行目录或密钥加入 Git。

开发前端时另开终端运行 `npm --prefix web run dev`，Vite 将 `/api` 和 `/sub` 请求代理到本地管理端。

## 部署与节点接入

详细步骤见 [部署指南](docs/deployment.md)，二进制下载/更新见 [发布指南](docs/releases.md)，系统结构见 [架构说明](docs/architecture.md)，字段及认证方式见 [API 契约](docs/api.md)，验证范围与资源抽样见 [验证记录](docs/verification.md)。

节点配置里的 `manager_url` 可以使用管理端的域名或 IP：`https://manager.example.com` / `https://203.0.113.10:8443`。IP 连接也必须通过证书校验，使用包含该 IP SAN 的证书；私有 CA 可用 `--ca-file` 导入。登录 key 是 `ENROLL_TOKEN`，首次注册换取节点自己的 token，后续心跳不再使用共享 enrollment key。

管理端支持 `TLS_CERT_FILE` / `TLS_KEY_FILE` 直接提供 HTTPS，无需额外常驻反向代理；也可用已有反向代理连接默认的本机 HTTP 监听。证书申请与续期由部署者管理。

在管理界面选择节点和协议创建订阅，保存首次显示的链接。之后编辑该订阅的节点集合时链接保持不变；禁用/删除节点会影响同一链接下一次刷新。Clash 客户端最终何时生效取决于客户端刷新间隔。临时心跳离线不会自动删除节点。链接撤销也不会使客户端已下载的代理密码失效。

## 仓库技能

canonical 技能位于 `skills/`，三个工具目录通过相对符号链接指向同一份内容，不安装到用户全局目录。

- [安装管理端](skills/install-proxy-manage-server/SKILL.md)
- [安装代理节点](skills/install-proxy-server/SKILL.md)
- [原始协议部署参考](skills/proxy-server-setup/SKILL.md)

当前自动初始化 Hysteria2 与 VLESS+REALITY。原始 skill 中的 SOCKS5 / HTTP / HTTPS 配置保留为手工扩展参考，不会自动开放到每台机器。
