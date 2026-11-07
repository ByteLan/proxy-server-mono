---
name: install-proxy-manage-server
description: 安装或升级本仓库 Go/Hertz 代理管理端，配置 SQLite、嵌入前端、HTTPS 地址及节点注册 key。用于部署管理面；代理节点安装使用 install-proxy-server。
---

# 安装代理管理端

先读仓库 [AGENTS.md](../../AGENTS.md) 和 [部署指南](../../docs/deployment.md)。源码根目录是本文件所在目录的上两级。

管理端只提供控制面、SQLite 和嵌入的 React/Arco UI；不要在管理端安装代理核心或打开代理监听。目标是 Linux systemd 主机，1 核 / 1 GiB 尚未实测，构建在开发机/CI 完成。

1. 确认目标架构、已有安装、对外 HTTPS 地址与本机监听地址。检查已有 `/etc/proxy-manage-server/manager.env` 和 DATA_DIR，升级时保留凭据与数据库。
2. 首次构建使用 Go 1.25+、Node.js 20+：在仓库根运行 `npm --prefix web ci`，再按目标架构运行 `GOTOOLCHAIN=auto GOOS=linux GOARCH=amd64 make build`；ARM64 用 `arm64`。两个 Go 二进制都生成于 `bin/`，目标管理机只需要 manager 程序。
3. 将 manager 二进制及仓库 `scripts/`、`deploy/` 放到目标机，保持脚本相对目录。在获得该主机安装授权的范围内运行：

   ```bash
   sudo bash scripts/install-proxy-manage-server.sh \
     --binary ./bin/proxy-manage-server --public-url https://manager.example.com
   ```

4. 首次安装自动生成 ADMIN_TOKEN / ENROLL_TOKEN 并写入 root-only `manager.env`，不在回复或日志中展示秘密。必要时由用户在目标机私下读取 ADMIN_TOKEN 登录 UI，用 ENROLL_TOKEN 接入节点。首次启动自动保存主密钥到 DATA_DIR/master.key；显式 MASTER_KEY 必须是 32 字节的 base64，保留原密钥与 SQLite 一起备份。
5. 已有证书时用安装参数 `--listen 0.0.0.0:8443 --public-url https://manager.example.com:8443 --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem` 直接提供 HTTPS，无需额外常驻服务；也可用现有 HTTPS 反向代理连接默认 `127.0.0.1:8080`。验证证书和 `/healthz`。使用 IP 时证书必须含对应 IP SAN，私有 CA 通过节点 `--ca-file` 信任。确认管理端可直连，避免订阅获取依赖尚未下载的代理配置。
6. 检查 systemd 运行状态、UI token 登录、节点列表和订阅创建。选择变化应在同一链接下一次刷新时生效。订阅链接只在创建/轮换时显示；不要把它写入公开日志或 Git。

配置变量为 LISTEN、DATA_DIR、PUBLIC_URL、ADMIN_TOKEN、ENROLL_TOKEN、可选 MASTER_KEY 和成对的 TLS_CERT_FILE / TLS_KEY_FILE。脚本重跑保留已有环境文件；修改监听/地址需明确编辑该文件再重启；显式传 TLS 文件会复制并更新 TLS 路径。续期需手动更新文件并重启，不自动 ACME。改变主密钥不等同于 token 轮换，错误更换会使历史凭据不可解密。

不操作真实主机时，可用 `DESTDIR=/absolute/staging/path bash scripts/install-proxy-manage-server.sh ...` 验证目录和 unit；它跳过用户创建、systemctl、数据库及主密钥初始化，不能报告为已上线。交付应区分文件安装、服务启动、HTTPS验证及真实节点接入各自的验证结果。
