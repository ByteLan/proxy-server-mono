# Proxy monorepo

集中管理多个海外代理节点，并通过固定订阅链接动态生成 mihomo / Clash Meta YAML 配置（内置国内 DoH 分流、全局模式对齐规则节点，以及 Claude / ChatGPT / Google 专属纯净节点自动测速与禁直连保护）。

- `proxy-server`：Go 节点 Agent，管理独立 Hysteria2 / Xray 子进程、首次注册和定期心跳。
- `proxy-manage-server`：Go / Hertz 控制面，SQLite 保存节点与订阅，通过 `go:embed` 内置编译后的 React / Arco Design 前端；不运行代理核心、不转发代理流量。
- `web`：React / TypeScript / Vite / Arco Design 管理界面源码。

**核心交付闭环：开发机编译前后端 → 脚本一键发布 GitHub Release → Server 端（Manager / Node）直接下载已编译二进制安装或更新（服务端零编译，更新管理端即同时更新前后端）。**

目标是让管理端运行在 Linux 1 核 / 1 GiB 机器上，前端和 Go 程序由维护者在开发机手动编译；**尚未在该规格服务器上做资源、容量或长期稳定性实测**。仓库不配置 GitHub Actions 自动构建或自动发布，所有 Release 均由维护者在本地显式发布。

---

## 本地开发与发布 GitHub Release

### 1. 本地前置条件

开发机发布 Release 需要以下环境与条件（目标 VPS **不需要**这些编译工具）：

- **基础工具链**：`git`、Go `1.25+`（见 `go.mod`）、Node.js `20+` 与 `npm`、`python3`、`sha256sum`（或 macOS 自带的 `shasum`）、GitHub CLI `gh`。
- **GitHub 写权限登录**：已执行 `gh auth login`。若为私有仓库，登录凭据需具备目标仓库 **Contents: Read and write** 权限（切勿与 VPS 上的只读下载 Token 混用）。
- **干净且已推送的 Git 状态**（`scripts/publish-release.sh` 会强制校验）：
  1. 前端静态产物已同步至 `internal/webui/dist` 并提交，工作区无任何未提交修改或未跟踪文件（`git status` 完全干净）。
  2. 当前本地 `HEAD` commit 已经 `git push` 到远端 GitHub（默认发布 `main-<12位SHA>` 预发布时，要求本地 `HEAD` 与远端 `main` 分支最新提交一致）。

### 2. 标准开发与发布步骤

```bash
# 1. 首次安装前端依赖
npm --prefix web ci

# 2. 构建前端（同步至 internal/webui/dist）+ 构建本地双端二进制 + 运行全量测试与静态检查
GOTOOLCHAIN=auto make build
GOTOOLCHAIN=auto make test check

# 3. 提交代码（含更新后的 internal/webui/dist 嵌入产物）并推送到远端
git add -A
git commit -m "feat: your changes"
git push origin main

# 4. 本地交叉编译 Linux (amd64/arm64/armv7) 全套产物并发布到 GitHub Release
gh auth status
GOTOOLCHAIN=auto make publish
```

`make publish` 底层调用 `scripts/publish-release.sh`：在本地编译 3 种 Linux 架构共 6 个静态二进制（管理端已内嵌最新前端），打包 `install.sh`、`install-bundle.tar.gz` 与 `SHA256SUMS`，创建草稿上传完整 9 个文件校验无误后正式公开为 `main-<sha12>` prerelease（对应服务端默认的 `main` 更新渠道）。

常用可选构建与发布命令：

```bash
# 发布正式稳定版 tag（对应服务端的 --channel stable）
GOTOOLCHAIN=auto bash scripts/publish-release.sh --tag v0.1.0

# Dry-run：仅在本地构建并校验全部 9 个发布文件，不要求 gh 登录、不上传远端
GOTOOLCHAIN=auto bash scripts/publish-release.sh --dry-run

# 仅在本地生成 Linux 发布包到 bin/releases/，不调用发布逻辑
GOTOOLCHAIN=auto make release

# 未改动前端时复用已提交的 internal/webui/dist 纯 Go 构建（无需 Node）
GOMAXPROCS=1 GOFLAGS="-p=1" GOTOOLCHAIN=auto make build-go
```

> Makefile 默认附加 `-tags stdjson,gjson` 构建与测试；手动运行 `go build/test/vet` 时也需带上 `-tags stdjson,gjson`。更多细节见[发布与更新指南](docs/releases.md#维护者手动构建与发布)与[源码构建评估](docs/source-build.md)。

---

## Server 端免编译安装

目标 Linux 服务器**不需要安装 Go 或 Node.js**，仅需系统具备 `python3`、`openssl`、`systemd`（`useradd`、`install`）及 `curl`。

### 1. 准备下载凭据（仅私有仓库需要）

- **公开仓库**：无需任何 GitHub Token，下文所有命令省略 `--token-file`（或 `GITHUB_TOKEN`）即可。
- **私有仓库**：在 GitHub 创建仅勾选本仓库、权限仅为 **Contents: Read-only** 的 Fine-grained PAT，在服务器上保存为 `0600` 权限文件：
  ```bash
  sudo install -d -m 0700 /root/.config/proxy-server-mono
  sudo sh -c 'umask 077 && cat > /root/.config/proxy-server-mono/github-token'
  # 粘贴只读 PAT 后按 Ctrl-D
  ```

### 2. 两种获取安装脚本的方式

无论选择哪种方式，执行 `install.sh` 时都会自动从 GitHub Release 下载匹配当前服务器架构的已编译二进制与 `install-bundle.tar.gz`，核验 `SHA256SUMS` 后配置 `systemd` 服务，**全程不在服务器编译**：

- **方式 A（拉取仓库后运行脚本）**：在服务器上 `git clone` 或 `git pull` 仓库后，直接运行仓库内的 `./scripts/install.sh`。
- **方式 B（免克隆仓库，仅下载单文件 `install.sh`）**：通过 GitHub API 安全拉取 `scripts/install.sh`（具体 `curl` 命令见 [releases.md](docs/releases.md#下载-bootstrap-脚本)）。

`install.sh` 通用语法（`--` 前为下载源参数，`--` 后透传给底层安装器）：

```text
bash scripts/install.sh manager|node [--repo owner/repo] [--channel main|stable]
                        [--version TAG] [--token-file PATH] -- INSTALLER_ARGUMENTS
```

### 3. 安装管理端（proxy-manage-server）

```bash
# 默认拉取最新 main-<sha12> 发布；正式版可加 --channel stable
sudo bash ./scripts/install.sh manager \
  --token-file /root/.config/proxy-server-mono/github-token -- \
  --public-url https://manager.example.com
```

- 若管理端直接提供 HTTPS（无需前置反向代理），在 `--` 后替换为：`--listen 0.0.0.0:8443 --public-url https://manager.example.com:8443 --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem`。
- 首次初始化自动生成的 `ADMIN_TOKEN`（后台登录令牌）与 `ENROLL_TOKEN`（节点注册准入令牌）保存在 `/etc/proxy-manage-server/manager.env`（`0600`）。

### 4. 安装代理节点（proxy-server）

先按[部署指南](docs/deployment.md#代理节点)在节点准备好官方固定版本的 `hysteria` 与 `xray` 二进制及预期 SHA256（安装器不会自动拉取 `latest` 协议核心），然后执行：

```bash
read -r -s -p 'Manager enrollment key: ' ENROLL_TOKEN && printf '\n'
export ENROLL_TOKEN
sudo --preserve-env=ENROLL_TOKEN bash ./scripts/install.sh node \
  --token-file /root/.config/proxy-server-mono/github-token -- \
  --manager https://manager.example.com --server node01.example.com --name node01 \
  --hy2-port 443 --vless-port 443 \
  --hysteria-binary "$HYSTERIA_BINARY" --hysteria-sha256 "$HYSTERIA_SHA256" \
  --core-version "hysteria=$HYSTERIA_VERSION" \
  --xray-binary "$XRAY_BINARY" --xray-sha256 "$XRAY_SHA256" \
  --core-version "xray=$XRAY_VERSION"
unset ENROLL_TOKEN
```

---

## Server 端免编译更新与回退

当你在本地修改了后端或前端代码并执行 `make publish` 发布新 Release 后，服务端有两种免编译更新方式：

### 1. 使用内置 `update` 命令直接更新（推荐，无需拉取源码）

已安装的 `proxy-manage-server` 与 `proxy-server` 均内置 `update` 子命令，直接从 GitHub Release 下载对应架构新二进制、校验 `SHA256SUMS`、将旧版备份为 `.previous` 并原子替换。**更新管理端二进制即一次性完成后端 + 内嵌前端升级**，不会修改 SQLite 数据库、配置文件或节点协议核心。

- **更新管理端（前后端一体更新）**：
  ```bash
  # 1. 查看当前版本与检查远端候选更新
  /opt/proxy-manage-server/bin/proxy-manage-server version
  sudo /opt/proxy-manage-server/bin/proxy-manage-server update --check \
    --token-file /root/.config/proxy-server-mono/github-token

  # 2. 建议升级前备份数据目录（含 manager.db 与 master.key），随后更新并重启生效
  sudo /opt/proxy-manage-server/bin/proxy-manage-server update \
    --token-file /root/.config/proxy-server-mono/github-token
  sudo systemctl restart proxy-manage-server
  ```
  *(公开库可省略 `--token-file`；切换稳定版加 `--channel stable`，固定版本加 `--version TAG`。)*

- **更新代理节点 Agent**：
  ```bash
  /opt/proxy-server/bin/proxy-server version
  sudo /opt/proxy-server/bin/proxy-server update --check \
    --token-file /root/.config/proxy-server-mono/github-token
  sudo /opt/proxy-server/bin/proxy-server update \
    --token-file /root/.config/proxy-server-mono/github-token
  sudo systemctl restart proxy-server
  ```

### 2. 拉取仓库最新代码后通过脚本更新（可选）

如果服务端保留了 Git 仓库克隆，也可以 `git pull` 后重新执行 `./scripts/install.sh`（当 `systemd` 单元或安装布局有变更时尤为适用），底层安装器会保留已有的 `manager.env`、SQLite 数据库、节点身份与证书并完成二进制替换及服务重启：

```bash
git pull --ff-only
sudo bash ./scripts/install.sh manager \
  --token-file /root/.config/proxy-server-mono/github-token -- \
  --public-url https://manager.example.com
```

### 3. 二进制快速回退

每次 `update` 成功后均保留上一版可执行文件为 `.previous`（仅回退二进制，不回滚 SQLite 数据迁移；完整备份与恢复规范见 [releases.md](docs/releases.md#升级前备份与二进制回退)）：

```bash
sudo systemctl stop proxy-manage-server
sudo cp -p /opt/proxy-manage-server/bin/proxy-manage-server.previous \
  /opt/proxy-manage-server/bin/proxy-manage-server
sudo systemctl start proxy-manage-server
```

---

## 本地运行与调试

```bash
export ADMIN_TOKEN="$(openssl rand -hex 32)"
export ENROLL_TOKEN="$(openssl rand -hex 32)"
export DATA_DIR="$(mktemp -d)"
export LISTEN=127.0.0.1:8080
export PUBLIC_URL=http://127.0.0.1:8080
./bin/proxy-manage-server
```

访问 `http://127.0.0.1:8080`，使用 `ADMIN_TOKEN` 登录。管理端首次启动会在 `DATA_DIR/master.key` 创建加密主密钥（也可通过 `MASTER_KEY` 指定 base64 编码的 32 字节密钥）。前端热更新调试可在另一终端运行 `npm --prefix web run dev`（Vite 自动将 `/api` 和 `/sub` 代理至 `127.0.0.1:8080`）。

---

## 订阅分流与节点接入说明

详细系统设计见 [架构说明](docs/architecture.md)，接口定义见 [API 契约](docs/api.md)，部署细节见 [部署指南](docs/deployment.md)，验证与资源抽样见 [验证记录](docs/verification.md)。

- **节点接入**：节点 `manager_url` 支持 HTTPS 域名或带 IP SAN 证书的 IP（私有 CA 可用 `--ca-file` 导入），首次凭 `ENROLL_TOKEN` 注册换取绑定该节点 ID 的独立 Token，私钥始终留在节点本地。
- **动态固定订阅与专属纯净分流**：
  - 每条订阅链接可自由勾选**通用代理节点（`🌍 代理`）**与**协议范围**，并可单独为 **Claude / Anthropic**、**ChatGPT / Codex**、**Google / Gemini（含 Antigravity、YouTube）** 指定一台或多台纯净 IP 节点。
  - 三个专属组均采用 `REJECT` 置顶的 `url-test`（30 秒自动测速切换所选节点下最快的协议，组内全部失效或被停用/删除时自动 `REJECT` 阻断，严禁回退到脏 IP 节点或直连泄漏真实 IP）。
  - 显式声明 `GLOBAL` 策略组并将 `🌍 代理` 置于首位，客户端切换到「全局」模式时默认复用规则模式同款节点，不会误走 `DIRECT`。
  - 后台修改订阅节点、专属分流或停用/删除节点后，同一订阅 URL 下次刷新即刻生效；临时心跳离线不会自动移除节点；撤销订阅链接不会使客户端已下载的节点密码失效。

---

## 仓库技能

Canonical 技能位于 `skills/`，`.agents/skills`、`.claude/skills`、`.codex/skills` 通过相对符号链接指向同一份内容：

- [安装管理端](skills/install-proxy-manage-server/SKILL.md)
- [安装代理节点](skills/install-proxy-server/SKILL.md)
- [原始协议部署参考](skills/proxy-server-setup/SKILL.md)（自动初始化 Hysteria2 与 VLESS+REALITY；SOCKS5 / HTTP / HTTPS 保留为手工扩展参考）
