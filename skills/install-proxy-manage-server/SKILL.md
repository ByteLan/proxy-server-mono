---
name: install-proxy-manage-server
description: 从 GitHub Release 下载、初始化或手动更新本仓库 Go/Hertz 管理端，配置 SQLite、嵌入前端和 HTTPS。用于管理面部署；代理节点使用 install-proxy-server。
---

# 安装代理管理端

先读仓库 [AGENTS.md](../../AGENTS.md) 和 [部署指南](../../docs/deployment.md)。首次下载、私库认证及版本选择按[发布指南](../../docs/releases.md)执行；源码根目录是本文件所在目录的上两级。

推荐开发机编译并手动发布 Release，VPS 只下载已编译程序，无需 Go/Node 编译环境。发布者在工作树干净、当前 commit 已自行 push 后运行 `GOTOOLCHAIN=auto make publish`（默认 main-sha 预发布）；正式版本用 `bash scripts/publish-release.sh --tag vX.Y.Z`，只本地检查用 `--dry-run`，具体参数见发布指南。实际发布需要 gh 登录和仓库写权限；dry-run 不需要登录也不证明已发布。发布凭据与 VPS Contents 只读下载 token 分开保管。

仓库没有分支 push 自动构建或发布；发布脚本不自动 commit/push，也不覆盖已发布 tag。Release 安装与 update 依赖维护者已上传完整资产；没有对应发布时使用本地手动构建安装流程，不把 update 描述为已支持拉取源码编译。

管理端只提供控制面、SQLite 和嵌入的 React/Arco UI；不要在管理端安装代理核心或打开代理监听。目标是 Linux systemd 主机，1 核 / 1 GiB 尚未实测，构建由维护者在开发机手动完成。

1. 确认目标架构、已有安装、对外 HTTPS 地址与本机监听地址。检查已有 `/etc/proxy-manage-server/manager.env` 和 DATA_DIR，升级时保留凭据与数据库。
2. 优先按发布指南经 GitHub Contents API 获取独立 `install.sh`，在目标机以管理员运行 `bash install.sh manager [渠道/版本选项] -- --public-url URL [TLS参数]`。它验证发布程序与四文件 bundle 后完成初始化，无需源码、Go/Node、gh 或复制 scripts/deploy。安装阶段需要 Python3、OpenSSL、systemd 工具，首次获取脚本需要 curl。私库使用单仓库 Contents 只读 PAT，经 GITHUB_TOKEN / GH_TOKEN 或 `--token-file`（普通文件、0600）提供；不把 token 放到参数、回复、日志或 service env。下载时的 Authorization 不转发到跨域存储。
3. 已获得脚本与安全设置的 GITHUB_TOKEN 时，可执行：

   ```bash
   sudo --preserve-env=GITHUB_TOKEN bash ./install.sh manager -- \
     --public-url https://manager.example.com
   ```

   默认仓库 ByteLan/proxy-server-mono，默认 main 渠道，选择最新已发布 main-sha 预发布；稳定版本用 `manager --channel stable -- ...`，固定版本用 `manager --version TAG -- ...`。程序资产为 `proxy-manage-server_linux_amd64` / `arm64` / `armv7`。只在需要自行构建时使用 Go1.25+、Node和README构建流程，或在已备好源码相对目录时调用原安装器：

   ```bash
   sudo bash scripts/install-proxy-manage-server.sh \
     --binary ./bin/proxy-manage-server --public-url https://manager.example.com
   ```

4. 首次安装自动生成 ADMIN_TOKEN / ENROLL_TOKEN 并写入 root-only `manager.env`，不在回复或日志中展示秘密。必要时由用户在目标机私下读取 ADMIN_TOKEN 登录 UI，用 ENROLL_TOKEN 接入节点。首次启动自动保存主密钥到 DATA_DIR/master.key；显式 MASTER_KEY 必须是 32 字节的 base64，保留原密钥与 SQLite 一起备份。
5. 已有证书时用安装参数 `--listen 0.0.0.0:8443 --public-url https://manager.example.com:8443 --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem` 直接提供 HTTPS，无需额外常驻服务；也可用现有 HTTPS 反向代理连接默认 `127.0.0.1:8080`。纯 IP 自签证书场景下，Clash 客户端快速导入框会拒绝自签 HTTPS，可追加 `--http-sub-listen 0.0.0.0:9080 --http-sub-public-url http://<IP>:9080` 开启只读 HTTP 订阅端口（仅服务 `/sub/:token` 与 `/healthz`，管理 API 与节点注册仍强制走 HTTPS）。验证证书和 `/healthz`。使用 IP 时证书必须含对应 IP SAN，私有 CA 通过节点 `--ca-file` 信任。确认管理端可直连，避免订阅获取依赖尚未下载的代理配置。
6. 检查 systemd 运行状态、UI token 登录、节点列表和订阅创建。选择变化应在同一链接下一次刷新时生效。订阅 token 在数据库加密存储（`token_enc`），管理员可在后台反复查看、复制或重置订阅链接；不要把订阅链接写入公开日志或 Git。

配置变量为 LISTEN、DATA_DIR、PUBLIC_URL、ADMIN_TOKEN、ENROLL_TOKEN、可选 MASTER_KEY 和成对的 TLS_CERT_FILE / TLS_KEY_FILE。脚本重跑保留已有环境文件；修改监听/地址需明确编辑该文件再重启；显式传 TLS 文件会复制并更新 TLS 路径。续期需手动更新文件并重启，不自动 ACME。改变主密钥不等同于 token 轮换，错误更换会使历史凭据不可解密。

后续仅更新程序，先运行 `proxy-manage-server version` 与 `update --check`；默认 main，也可 `--channel stable` 或 `--version TAG`。以管理员运行安装路径的程序加 `update`，私库可加 `--token-file /root/.config/proxy-server-mono/github-token`，然后手动 `systemctl restart proxy-manage-server`。update 校验 SHA256、同目录原子替换并保留 `.previous`，不改配置、数据或协议核心，不自动重启。升级前停服备份实际 DATA_DIR（含 master.key 或原 MASTER_KEY）与 manager.env；二进制回退先停服，再恢复 `.previous`，不能声称数据库也已回滚。具体命令见发布指南。

保持 `/opt/proxy-manage-server/bin` 为 root 所有，服务用户不能自改二进制。GitHub PAT 不需要长期放入服务环境；确需 sudo 转发时只 preserve 对应 GitHub 变量，不保留全部环境。较旧程序无 update 子命令时重新使用 bootstrap 安装，已有环境与数据库保留，但安装器会重启服务。

不操作真实主机时，可用 `DESTDIR=/absolute/staging/path bash scripts/install-proxy-manage-server.sh ...` 验证目录和 unit；它跳过用户创建、systemctl、数据库及主密钥初始化，不能报告为已上线。交付应区分文件安装、服务启动、HTTPS验证及真实节点接入各自的验证结果。
