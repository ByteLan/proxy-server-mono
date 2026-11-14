# Clash 订阅生成与纯净分流

## 1. 业务规则（当前生效）

- **固定订阅链接动态生效**：每条订阅绑定一组通用节点（`node_ids`）、协议白名单（`protocols`，空数组代表允许全部受支持协议：`hysteria2`、`vless`、`socks5`、`http`、`https`）及可选专属服务分流（`routes`）。增删节点、启停节点或修改订阅选择后，客户端刷新同一条订阅 URL 即可获得最新配置；节点短暂心跳超时离线不会自动将其从订阅中剔除，避免链路抖动引发配置震荡；空节点集合仍输出合法的 `DIRECT` 兜底 YAML。
- **客户端目标内核与安全参数**：生成配置面向 `mihomo` / Clash Meta：
  - `hysteria2` 强制输出 `fingerprint`（服务端证书 SHA-256 固定指纹），不开启跳过证书校验。
  - `vless` 采用 TCP + REALITY（`xtls-rprx-vision`、公钥 `public-key` 与 `short-id`）。
- **国内 DoH、防污染 DNS 与纯 IP 域名嗅探（Sniffer）架构**：
  - 启用 `fake-ip`（`28.0.0.1/8`），默认 `nameserver` 与 `proxy-server-nameserver` 使用国内 DoH，配合 `nameserver-policy` 前置分流，避免节点域名死锁与明文 UDP DNS 抢答污染。
  - 默认开启 `sniffer`（`enable: true`、`force-dns-mapping: true`、`parse-pure-ip: true`、`override-destination: true`），嗅探 `TLS / QUIC (443, 8443)` SNI 与 `HTTP (80, 8080-8880)` Host：即使桌面/移动端应用（如 Go 编写的 Antigravity 后台进程）在开启 TUN 虚拟网卡前已缓存了被 GFW 污染的伪造 IP，也能自动从 TLS SNI 还原真实目标域名（如 `oauth2.googleapis.com`）并覆写目标地址，精准命中专属分流规则并由远端节点解析真实 IP。
- **简洁节点命名与自动托管分组标识**：
  - 导出的 `proxies[].name` 统一采用 `<节点名> <协议名>` 简洁格式（`Hysteria2` / `VLESS` / `SOCKS5` / `HTTP` / `HTTPS`，例如 `sg01 Hysteria2`、`sg01 VLESS`），不拼接内部节点/端点随机 ID；仅在同名同协议冲突时追加 `#2`、`#3` 消歧。
  - 所有无需用户手动干预的 `url-test` 策略组统一带 `[自动托管测速-无需手选]` 前缀（`[自动托管测速-无需手选] ⚡ 通用`、`[自动托管测速-无需手选] 🔒 Claude`、`[自动托管测速-无需手选] 🤖 ChatGPT`、`[自动托管测速-无需手选] 🔍 Google`），且**不设置 `hidden: true`**，既明确提示用户仅需操作 `🌍 代理` 主组，又保留手机端与桌面端查看各分组当前自动命中节点与测速延迟的能力。
- **`GLOBAL` 全局模式对齐**：显式输出 `GLOBAL` `select` 策略组，并将 `🌍 代理` 置于首位（而非默认的 `DIRECT`），确保用户在客户端切换至 Global 模式时默认复用与 Rule 模式一致的代理节点。
- **三大纯净 IP 专属分流组（Claude / ChatGPT / Google）**：
  - 分别对应 `[自动托管测速-无需手选] 🔒 Claude`（Anthropic / Claude 全系域名）、`[自动托管测速-无需手选] 🤖 ChatGPT`（OpenAI / ChatGPT / Codex / Sora 全系域名）与 `[自动托管测速-无需手选] 🔍 Google`（Google / Gemini / Antigravity / YouTube 全系域名）。
  - 当某分流未开启独立指定（`custom: false`）时，默认使用通用节点池自动测速。
  - 当开启独立指定（`custom: true`）时，渲染为 **`REJECT` 置顶的 `url-test` 测速组**（间隔 30s，`tolerance: 0`），自动在绑定的纯净节点全协议端点间选择最低延迟链路，并额外注入非 UDP 兜底规则防止 QUIC/HTTP3 旁路泄漏。

## 2. 跨端代码锚点

- **YAML 渲染与规则编排**：`internal/clash/render.go`（`Render`）
- **渲染回归与 mihomo 语法校验测试**：`internal/clash/render_test.go`、`internal/agent/mihomo_integration_test.go`
- **订阅路由选择存储校验**：`internal/manager/store.go`
- **前端订阅表单与专属分流选择器**：`web/src/App.tsx`

## 3. 模块红线

- 开启 `custom: true` 的专属分流组（Claude / ChatGPT / Google）在所绑定节点全部停用、删除或不可用时，**必须严格落入 `REJECT` 阻断**，严禁回退到通用 `🌍 代理` 池（可能含非纯净 IP）或 `DIRECT`（直接暴露用户真实本地 IP 导致封号）。
- 撤销或轮换订阅 URL 仅阻止后续再次拉取配置，不能将其向用户表述为“已吊销客户端本地已下载的代理连接密码”。
