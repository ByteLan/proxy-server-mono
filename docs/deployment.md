# 部署

推荐在开发机编译，再手动发布 GitHub Release，最后由 VPS 下载；目标机不需要 Go/Node 编译环境。开发机先自行提交并 push 当前 commit、保持工作树干净，再用 `GOTOOLCHAIN=auto make publish`。发布需要 gh 已登录且对仓库有写权限；`--dry-run` 只本地构建和校验，不需要 gh 登录。完整发布参数与凭据边界见[发布指南](releases.md#维护者手动构建与发布)。

## 直接下载并初始化

维护者已手动发布完整 Release 时，可在目标 Linux 主机运行独立 `install.sh`。它从指定 GitHub release 下载所选 Go 程序、`install-bundle.tar.gz` 和 `SHA256SUMS`，校验后调用 bundle 内的安装脚本完成 systemd 与配置初始化；目标机无需源码目录或 Go/Node 编译环境。完整的私库首次脚本下载和 manager/node 示例见[发布与更新](releases.md#首次下载并初始化)。

```bash
sudo --preserve-env=GITHUB_TOKEN bash ./install.sh manager -- \
  --public-url https://manager.example.com
```

仓库没有 push 或 tag 触发的构建发布；未手动上传目标架构程序、安装 bundle 和校验清单时，这条下载路径不可用。可以改用下文的本地手动构建与安装流程。

GitHub token 仅用于下载：使用单仓库 Contents 只读权限，经环境变量或 `--token-file`（0600）提供，不放入服务环境文件。默认 main 渠道；指定正式版本用 `manager --channel stable -- ...`，指定发布用 `manager --version TAG -- ...`。`--` 后是下文既有安装脚本的参数，`--binary` 由 bootstrap 自动提供。节点模式用 `node`，仍需准备固定版本核心的路径、预期 hash 和版本；bootstrap 不自动更新核心。

## 本地手动构建后安装

由维护者在开发机手动构建，避免在 1 GiB 服务器运行 npm 和完整编译链。目标容量尚未在 1 CPU / 1 GiB VPS 上实测。

```bash
npm --prefix web ci
GOTOOLCHAIN=auto GOOS=linux GOARCH=amd64 make build
```

ARM64 改为 `GOARCH=arm64`，ARMv7 使用 `GOARCH=arm GOARM=7`。自行构建路径下，将目标二进制、`scripts/` 和 `deploy/` 复制到目标 Linux 主机，保持 `scripts/../deploy/systemd` 的相对位置；使用上面的独立 bootstrap 时无需复制这些目录。安装脚本使用 systemd、`useradd`、`install` 和 OpenSSL；选用 Debian/Ubuntu 等提供这些工具的发行版。脚本不负责 SSH、云防火墙或自动下载核心。

## 管理端

管理端只需要 `bin/proxy-manage-server`，不安装 Hysteria/Xray。

```bash
sudo bash scripts/install-proxy-manage-server.sh \
  --binary ./bin/proxy-manage-server \
  --public-url https://manager.example.com
```

首次安装自动生成 ADMIN_TOKEN / ENROLL_TOKEN，保存到 `/etc/proxy-manage-server/manager.env`，权限 `0600`，不会输出到终端。以 root 读取该文件，使用 ADMIN_TOKEN 登录 UI，使用 ENROLL_TOKEN 接入节点。也可在首次安装时由调用环境传入预先准备的值；`sudo` 默认不转发环境，应通过获授权的安全方式保留相应变量。

`manager.env` 配置项：

| 变量 | 用途 / 默认 |
|---|---|
| `LISTEN` | `127.0.0.1:8080` |
| `DATA_DIR` | 安装脚本设置为 `/var/lib/proxy-manage-server` |
| `PUBLIC_URL` | 对外 HTTPS 地址，用于返回订阅 URL |
| `ADMIN_TOKEN` | 管理 API 和 UI 登录 key |
| `ENROLL_TOKEN` | 新节点初次注册 key |
| `MASTER_KEY` | 可选，base64 编码的 32 字节密钥；未设置则首次启动创建 `DATA_DIR/master.key` |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | 可选，成对设置 PEM 证书与私钥路径，直接提供 HTTPS |
| `HTTP_SUB_LISTEN` / `HTTP_SUB_PUBLIC_URL` | 可选，成对设置独立 HTTP 订阅监听地址与对外 URL（仅暴露 `/sub/:token` 与 `/healthz`，供纯 IP 无域名环境一键导入 Clash） |

已有证书时可让管理端直接提供 HTTPS，减少额外常驻服务：

```bash
sudo bash scripts/install-proxy-manage-server.sh \
  --binary ./bin/proxy-manage-server \
  --listen 0.0.0.0:8443 --public-url https://manager.example.com:8443 \
  --tls-cert /path/to/fullchain.pem --tls-key /path/to/privkey.pem
```

脚本将证书/私钥复制到 `/etc/proxy-manage-server/tls.crt` / `tls.key`，目录为 root:proxy-manager `0750`，两个文件为 `0640`，环境文件仍为 root-only `0600`。TLS 最低版本为 1.2；证书申请和自动续期不在当前服务范围内。续期后重新复制证书和私钥并重启管理端，或用相同参数重跑安装脚本。已有环境文件不会被 `--listen` / `--public-url` 覆盖，首次从 HTTP 切换到直接 TLS 时需显式同步这两个配置；显式传入 TLS 文件会更新环境文件中的 TLS 路径。

也可把已有 HTTPS 反向代理的请求转发到默认 `127.0.0.1:8080`；例如已有 Caddy 可添加：

```caddyfile
manager.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

为域名配置 DNS、证书与防火墙，先验证管理端对应 HTTPS 地址的 `/healthz`，再接入远端节点。管理端不提供 TLS 证书自动申请。管理域名与订阅地址应能在未启用这些节点代理时直连，避免下载订阅依赖尚未获得的代理配置。

安装脚本再次执行会更新二进制、保留已有 `manager.env` 并重启；`--public-url` / `--listen` 不会覆盖旧文件。修改配置后使用 `systemctl restart proxy-manage-server`。单独改变加密主密钥会让已有加密数据无法解密，不能当成普通 token 轮换。

仅更新程序时优先使用 `proxy-manage-server update --check` / `update`，避免重新执行安装流程；update 不自动重启。升级前停服备份整个 DATA_DIR 及加密主密钥，操作和 `.previous` 二进制回退见[发布指南](releases.md#升级前备份与二进制回退)。备份程序不等于备份数据库。

## 代理节点

节点部署支持两种模式：**已有内核无感收编（方案 A：`--adopt-existing`）**与**纯新机器统一托管（方案 B：默认模式）**。

### 方案 A：已有内核机器无感收编（`--adopt-existing`）

如果目标主机已经安装并正在通过 systemd 运行 `hysteria.service` 与/或 `xray.service`（例如先前按 `proxy-server-setup` 搭建），可使用 `--adopt-existing` 直接收编：

```bash
export ENROLL_TOKEN  # 从私有配置或交互输入读取，勿提交到仓库
sudo --preserve-env=ENROLL_TOKEN bash scripts/install-proxy-server.sh \
  --binary ./bin/proxy-server \
  --adopt-existing \
  --manager https://manager.example.com \
  --server node01.example.com --name node01
```

- 脚本只安装 `/opt/proxy-server/bin/proxy-server` 与 `proxy-server.service`，**不覆盖或重启已有 `hysteria` / `xray` 二进制及 systemd 服务，不改动现有端口与连接密码**。
- `proxy-server adopt` 默认读取 `/etc/hysteria/config.yaml`（提取密码、监听端口、证书 SHA-256 指纹与 SNI）与 `/etc/xray/config.json`（支持 JSONC 注释，提取 `vless`+REALITY 公钥、`socks`、`http`、`http`+TLS 入站），私钥始终不写入 `/etc/proxy-server/config.json`。自定义路径或服务名可通过 `--hysteria-config`、`--hysteria-unit`、`--xray-config`、`--xray-unit` 指定。
- 安装脚本自动写入 `proxy-server.service.d/adopted-cores.conf`（`Wants=` + `After=` 弱依赖与启动先后顺序，避免用 `Requires=`/`BindsTo=` 导致内核异常连带杀掉 Agent）以及受限的 `proxy-server-recover-*.path/.service` 恢复单元。运行时 `proxy-server` 通过 `systemctl is-active` 探测被收编单元状态：若某内核未运行，`proxy-server` **不会崩溃退出**，而是按指数退避自动尝试拉起该服务（连续失败**至多尝试 10 次**；恢复后计数清零，超过 10 次停止拉起并持续通过心跳向管理端告警）。后续重启或升级 `proxy-server` **不会中断**原有的代理连接。

### 多管理端同时接入（Multi-Manager）

单个 `proxy-server` 节点可同时上报至多个独立的 `proxy-manage-server`（上限 16 个），每个管理端拥有独立的注册凭据、持久化节点 Token（`config.d/manager_<hash>.token`）及可选 TLS CA 证书：

```bash
read -r -s -p 'New manager enrollment key: ' ENROLL_TOKEN && printf '\n'
export ENROLL_TOKEN
sudo --preserve-env=ENROLL_TOKEN /opt/proxy-server/bin/proxy-server add-manager \
  --config /etc/proxy-server/config.json \
  --manager https://manager2.example.com [--ca-file /path/to/ca.pem]
unset ENROLL_TOKEN
sudo systemctl restart proxy-server
```

可用 `list-managers` 查看各管理端注册状态，或用 `remove-manager --manager URL` 移除不再使用的管理端（保留至少 1 个管理端）。其中一个管理端不可达或鉴权失败不会阻塞其余正常管理端的心跳。

### 方案 B：纯新机器统一安装托管（默认模式）

从各核心的官方 release 选择固定版本和正确 Linux 架构，下载并解包可执行文件，在可信渠道取得对应 SHA256。不要用 `latest`；不要只用对刚下载文件现算的 hash 替代对发布者预期 hash 的验证。Hysteria 与 Xray 版本及客户端版本需要一起做协议连通测试；版本记录不等同于已验证兼容性。

- Hysteria2 发布：<https://github.com/HyNetworks/hysteria/releases>
- Xray 发布：<https://github.com/XTLS/Xray-core/releases>

mihomo 的 [REALITY 官方说明](https://wiki.metacubex.one/config/proxies/tls/#reality-opts) 当前明确不保证 Xray v26.7.11+ 的兼容性。不要把 Xray 版本越新等同于对 mihomo 越兼容；选定的服务端、客户端组合须实际验证。

设置 `ENROLL_TOKEN` 为管理端的节点注册 key。以下变量要填入已经选定的版本、路径和预期 hash：

```bash
export ENROLL_TOKEN  # 从私有配置或交互输入读取，勿提交到仓库
sudo --preserve-env=ENROLL_TOKEN bash scripts/install-proxy-server.sh \
  --binary ./bin/proxy-server \
  --manager https://manager.example.com \
  --server node01.example.com --name node01 \
  --hy2-port 443 --vless-port 443 \
  --hysteria-binary "$HYSTERIA_BINARY" --hysteria-sha256 "$HYSTERIA_SHA256" \
  --core-version "hysteria=$HYSTERIA_VERSION" \
  --xray-binary "$XRAY_BINARY" --xray-sha256 "$XRAY_SHA256" \
  --core-version "xray=$XRAY_VERSION"
```

两个 `443` 分别使用 UDP 和 TCP，可以共存；请确认没有其他服务占用。开放相应云安全组及主机防火墙端口。IP 或 NAT 环境中，`--server` 应填客户端可以直接连接的对外地址，不是管理端地址；当前初始化要求对外端口与监听端口相同，不自动配置 NAT 转发。

`--manager https://203.0.113.10:8443` 也可使用 IP，但管理端证书必须含该 IP 的 SAN。使用私有 CA 时给安装脚本添加 `--ca-file /path/to/ca.pem`，安装至节点目录；Agent 仍验证证书，不关闭验证。

脚本校验核心 SHA256 后，将三个程序安装到 `/opt/proxy-server/bin/`，记录版本/hash到 `/opt/proxy-server/core-versions`，初始化 `/etc/proxy-server/config.json` 和 `config.d/`。节点登录 key 保存在 root-only `node.env`，节点注册 token 与私钥存于权限受限的配置目录。systemd 使用 `proxy-node` 用户运行，并仅授予绑定低端口的能力。

已注册的节点不要删除本地 token 后直接复用旧 ID 重新注册。需要全新身份时，先处理中心旧节点，再明确备份和重建配置。升级脚本保留已有配置；仅重跑时传新的 `--manager` / `--server` / `--name` 不会替换旧配置，请先停止服务，再显式编辑 `manager_url` / endpoints / core 配置并核对一致性。

当前更新命令只下载维护者已上传的 Release 二进制，不拉取源码或调用 Go 编译器。后续只升级 Agent 可运行 `/opt/proxy-server/bin/proxy-server update --check`，确认渠道或版本后以管理员运行 `update`，再手动 `systemctl restart proxy-server`。该路径保留节点身份和所有核心文件；重启 Agent 会重启核心并中断现有连接。不能为了自更新把 `/opt/proxy-server/bin` 改为服务用户可写。

## 直接运行 Agent

已有核心也可不使用安装脚本，直接初始化和运行：

```bash
./bin/proxy-server init --config ./local-node/config.json \
  --manager https://manager.example.com --server node01.example.com --name node01 \
  --hy2-port 8443 --vless-port 9443 \
  --hysteria-bin /absolute/path/hysteria --xray-bin /absolute/path/xray
ENROLL_TOKEN="$ENROLL_TOKEN" ./bin/proxy-server run --config ./local-node/config.json
```

`manager_url` 与可选 `enroll_token` 是 JSON 字段；推荐 enrollment key 经 ENROLL_TOKEN 环境变量提供，避免出现在命令参数和 shell history。首次注册成功后使用持久节点 token。私有 CA 用 `init --ca-file`；仅本机开发允许 `--manager http://127.0.0.1:8080 --allow-insecure-http`。远端 HTTP 不被允许。

## 验证与排障

```bash
systemctl status proxy-manage-server --no-pager
systemctl status proxy-server --no-pager
journalctl -u proxy-server --since '5 minutes ago'
```

在管理端确认节点已注册、心跳更新、两个核心运行。创建订阅、保存链接并导入支持 mihomo 的客户端，通过每个协议实际访问一个可控测试目标。仅观察服务器监听、服务器自身能访问互联网或 Agent 在线，都不能证明代理链路可用。

改动订阅选择后使用原链接再次刷新，验证选入/移除、禁用/删除效果。临时离线不自动移除；撤销链接不能回收已下载凭据。初始化自动启用两个协议；SOCKS5/HTTP/HTTPS 需按协议参考手工扩展，不能假定已经开启。

备份 SQLite 时停止管理端后备份整个 DATA_DIR（含 master.key），或使用 SQLite 一致性备份手段并单独保管同一主密钥。不要只复制一个仍在写入的数据库文件。安装脚本不包含备份、灾备或自动回滚。

## Staging

`DESTDIR=/absolute/staging/path` 可检查安装目录、权限、环境文件和 unit。管理端 staging 不创建数据库/密钥；节点 staging 不执行 init/注册；两者都不创建系统用户或调用 systemctl。它不是已配置可运行的打包镜像。

```bash
DESTDIR=/tmp/proxy-manager-stage bash scripts/install-proxy-manage-server.sh \
  --binary ./bin/proxy-manage-server --public-url https://manager.example.com
```

节点脚本同样在完整参数前设置 DESTDIR。staging 不能替代目标架构上的核心执行、TLS 和端到端网络验证。

首次注册若服务端已保存节点但响应在网络中丢失，Agent可能遇到409；不要反复覆盖中心身份。清理旧节点后用新的本地身份初始化接入。核心stdout/stderr不进入Agent日志；诊断协议配置可停止服务后按config.json的commands单独运行对应核心，避免同时启动两份监听进程。
