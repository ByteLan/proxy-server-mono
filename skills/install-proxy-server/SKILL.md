---
name: install-proxy-server
description: 从 GitHub Release 下载、初始化或手动更新本仓库 Go 节点 Agent，并安装固定版本 Hysteria2/Xray及注册到管理端。用于节点部署；管理端使用 install-proxy-manage-server。
---

# 安装代理节点

先读 [AGENTS.md](../../AGENTS.md)、[部署指南](../../docs/deployment.md)；首次下载、私库鉴权、渠道与回退按[发布指南](../../docs/releases.md)。需要协议细节时读 [原始协议参考](../proxy-server-setup/SKILL.md)。源码根目录是本文件所在目录的上两级。

节点 Agent 与两个独立核心运行在节点机，不能把管理端当成代理出口。安装目标为 Linux systemd amd64/arm64/armv7；ARMv7 核心也需单独提供相同架构版本。确认公网地址、UDP/TCP端口、云安全组和主机防火墙。节点能发心跳不代表客户端能访问其代理端口。

1. 明确管理端地址和节点对外地址。`manager_url` 使用 `https://管理域名或IP[:端口]`；`--server` 是客户端连接节点的域名/IP，不带 scheme/端口。使用 IP 的管理端证书必须包含对应 IP SAN，私有 CA 用 `--ca-file`，不关闭校验。
2. 优先按发布指南经 GitHub Contents API 获取独立 `install.sh`。它下载并校验目标架构 Agent 及安装 bundle，无需源码目录、Go/Node或gh；安装阶段需要 Python3、OpenSSL和systemd工具，首次获取脚本需要curl。默认仓库 ByteLan/proxy-server-mono、main 渠道；`node --channel stable -- ...` 选择正式 release，`node --version TAG -- ...` 固定发布。私库 PAT 只选此仓库 Contents 只读，经 GITHUB_TOKEN / GH_TOKEN 或 `--token-file`（普通0600文件）提供，不放到命令参数或长期service env。
3. 为 Hysteria2、Xray 分别从官方 release 选择固定版本、正确架构和可信的预期 SHA256；bootstrap 只下载 Agent，不自动下载/升级两个核心。不要使用 latest 或仅把下载文件现算出的 hash 视作发布者校验。通过安全交互向环境提供管理端 ENROLL_TOKEN，它与 GitHub PAT、ADMIN_TOKEN不同，不写在CLI参数、回复或日志中。已下载 install.sh 且安全设置 GITHUB_TOKEN / ENROLL_TOKEN 后，执行：

   ```bash
   sudo --preserve-env=GITHUB_TOKEN,ENROLL_TOKEN bash ./install.sh node -- \
     --manager https://manager.example.com --server node01.example.com --name node01 \
     --hy2-port 443 --vless-port 443 \
     --hysteria-binary "$HYSTERIA_BINARY" --hysteria-sha256 "$HYSTERIA_SHA256" \
     --core-version "hysteria=$HYSTERIA_VERSION" \
     --xray-binary "$XRAY_BINARY" --xray-sha256 "$XRAY_SHA256" \
     --core-version "xray=$XRAY_VERSION"
   ```

   路径、版本、hash须替换为已选实际值。若自行构建（Go1.25+，见README）或已经备好源码相对目录，也可直接使用原安装器：

   ```bash
   sudo --preserve-env=ENROLL_TOKEN bash scripts/install-proxy-server.sh \
     --binary ./bin/proxy-server \
     --manager https://manager.example.com --server node01.example.com --name node01 \
     --hy2-port 443 --vless-port 443 \
     --hysteria-binary "$HYSTERIA_BINARY" --hysteria-sha256 "$HYSTERIA_SHA256" \
     --core-version "hysteria=$HYSTERIA_VERSION" \
     --xray-binary "$XRAY_BINARY" --xray-sha256 "$XRAY_SHA256" \
     --core-version "xray=$XRAY_VERSION"
   ```

4. 脚本校验两个 hash，安装到 `/opt/proxy-server/bin`，生成 `/etc/proxy-server/config.json` 与 `config.d`，再以受限系统用户启动。443 UDP 与 443 TCP 可共存但需确认无占用。配置与 token 是私密运行数据，不能提交到仓库；REALITY/TLS 私钥只在节点保存。
5. 首次注册以 ENROLL_TOKEN 换取节点自己的 token（0600本地文件）；后续心跳使用节点 token。检查管理 UI 中节点 ID、公布地址、两个核心与最近心跳，再用 mihomo 客户端通过每个协议实际访问测试目标。不能用单纯的服务器 curl、端口监听或 agent 在线代替协议验证。

升级保留节点配置和 token。传新 manager/name/server 参数不会覆盖旧配置；需要变更时停服后明确编辑配置并核对对应核心配置。不要克隆已初始化目录给第二个节点，也不要删除 token 后复用旧 ID 注册。

只升级 Agent 时先运行 `/opt/proxy-server/bin/proxy-server version` 和 `update --check`，确认 main / `--channel stable` / `--version TAG` 后以管理员运行同一程序的 `update`；私库添加 `--token-file PATH` 或安全转发 GitHub环境变量。校验成功才同目录原子替换，旧程序在 `.previous`，配置、身份和两个协议核心均不改。update 不自动重启；手动 `systemctl restart proxy-server` 会重启核心并中断连接。回退先停止服务再恢复 `.previous`，不删除节点配置或凭据。旧程序无 update 子命令时使用 bootstrap 安装路径。

保持安装二进制与目录 root 所有，服务用户不能自改 `/opt/proxy-server/bin`。GitHub token 无需写入 node.env；sudo仅保留明确所需变量，不使用保留全部环境的方式。

若直接运行，使用 `proxy-server init --config PATH --manager URL --server HOST --name NAME --hysteria-bin PATH --xray-bin PATH`，随后 `ENROLL_TOKEN` 环境下 `proxy-server run --config PATH`。JSON 中连接地址字段是 `manager_url`，可选注册 key 字段是 `enroll_token`；优先使用环境变量。只有本机开发允许 `--manager http://127.0.0.1:8080 --allow-insecure-http`。

可用完整安装参数加 `DESTDIR=/absolute/staging/path` 检查文件/env/unit，但会跳过 init、注册、用户创建与 systemctl；staging 不是已接入的可运行节点。自动初始化只启用 Hysteria2 与 VLESS+REALITY，其他协议需显式手工配置和验证。
