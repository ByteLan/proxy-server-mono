# Proxy monorepo

集中管理多个代理节点，并通过固定订阅链接生成随节点选择变化的 mihomo / Clash Meta YAML。

- `proxy-server`：Go 节点 Agent，管理独立 Hysteria2 / Xray 子进程、首次注册和定期心跳。
- `proxy-manage-server`：Go / Hertz 控制面，SQLite 保存节点与订阅，嵌入 React / Arco Design 前端；不运行代理核心、不转发代理流量。
- `web`：React / TypeScript / Vite / Arco Design 管理界面。

目标是让管理端运行在 Linux 1 核 / 1 GiB 机器上，前端和 Go 程序在开发机或 CI 编译；**尚未在该规格服务器上做资源、容量或长期稳定性实测**。

## 构建

需要 Go 1.25+（版本见 `go.mod`）、Node.js 20+ 和 npm。首次安装前端依赖，然后构建两个二进制：

```bash
npm --prefix web ci
GOTOOLCHAIN=auto make build
GOTOOLCHAIN=auto make test check
```

`make build` 先将 `web/dist` 复制到 `internal/webui/dist`，再构建 `bin/proxy-server` 与 `bin/proxy-manage-server`。目标机器只需要相应二进制；管理端不需要 Node.js、npm、独立前端进程或数据库服务。跨平台构建时可在 `make build` 前设置 `GOOS=linux GOARCH=amd64`（ARM64 使用 `arm64`）。

Makefile 对 Go 构建、测试和 vet 使用 Hertz 的 `stdjson` build tag；手动执行对应命令时也应添加 `-tags stdjson,gjson`，避免依赖 Sonic 的特定 Go 编译器版本支持。

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

详细步骤见 [部署指南](docs/deployment.md)，系统结构见 [架构说明](docs/architecture.md)，字段及认证方式见 [API 契约](docs/api.md)，验证范围与资源抽样见 [验证记录](docs/verification.md)。

节点配置里的 `manager_url` 可以使用管理端的域名或 IP：`https://manager.example.com` / `https://203.0.113.10:8443`。IP 连接也必须通过证书校验，使用包含该 IP SAN 的证书；私有 CA 可用 `--ca-file` 导入。登录 key 是 `ENROLL_TOKEN`，首次注册换取节点自己的 token，后续心跳不再使用共享 enrollment key。

管理端支持 `TLS_CERT_FILE` / `TLS_KEY_FILE` 直接提供 HTTPS，无需额外常驻反向代理；也可用已有反向代理连接默认的本机 HTTP 监听。证书申请与续期由部署者管理。

在管理界面选择节点和协议创建订阅，保存首次显示的链接。之后编辑该订阅的节点集合时链接保持不变；禁用/删除节点会影响同一链接下一次刷新。Clash 客户端最终何时生效取决于客户端刷新间隔。临时心跳离线不会自动删除节点。链接撤销也不会使客户端已下载的代理密码失效。

## 仓库技能

canonical 技能位于 `skills/`，三个工具目录通过相对符号链接指向同一份内容，不安装到用户全局目录。

- [安装管理端](skills/install-proxy-manage-server/SKILL.md)
- [安装代理节点](skills/install-proxy-server/SKILL.md)
- [原始协议部署参考](skills/proxy-server-setup/SKILL.md)

当前自动初始化 Hysteria2 与 VLESS+REALITY。原始 skill 中的 SOCKS5 / HTTP / HTTPS 配置保留为手工扩展参考，不会自动开放到每台机器。
