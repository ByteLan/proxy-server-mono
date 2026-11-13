# Clash 订阅生成与纯净分流

## 1. 业务规则（当前生效）

- **固定订阅链接动态生效**：每条订阅绑定一组通用节点（`node_ids`）、协议白名单（`protocols`，空数组代表允许全部受支持协议：`hysteria2`、`vless`、`socks5`、`http`、`https`）及可选专属服务分流（`routes`）。增删节点、启停节点或修改订阅选择后，客户端刷新同一条订阅 URL 即可获得最新配置；节点短暂心跳超时离线不会自动将其从订阅中剔除，避免链路抖动引发配置震荡；空节点集合仍输出合法的 `DIRECT` 兜底 YAML。
- **客户端目标内核与安全参数**：生成配置面向 `mihomo` / Clash Meta：
  - `hysteria2` 强制输出 `fingerprint`（服务端证书 SHA-256 固定指纹），不开启跳过证书校验。
  - `vless` 采用 TCP + REALITY（`xtls-rprx-vision`、公钥 `public-key` 与 `short-id`）。
- **国内 DoH 与防污染 DNS 架构**：启用 `fake-ip`（`28.0.0.1/8`），默认 `nameserver` 与 `proxy-server-nameserver` 使用国内 DoH，配合 `nameserver-policy` 对海外及 AI 域名走代理解析，避免节点域名死锁与 DNS 抢答污染。
- **`GLOBAL` 全局模式对齐**：显式输出 `GLOBAL` `select` 策略组，并将 `🌍 代理` 置于首位（而非默认的 `DIRECT`），确保用户在客户端切换至 Global 模式时默认复用与 Rule 模式一致的代理节点。
- **三大纯净 IP 专属分流组（Claude / ChatGPT / Google）**：
  - 分别对应 `🤖 Claude`（Anthropic / Claude 全系域名）、`🧠 ChatGPT`（OpenAI / ChatGPT / Codex / Sora 全系域名）与 `🔍 Google`（Google / Gemini / Antigravity / YouTube 全系域名）。
  - 当某分流未开启独立指定（`custom: false`）时，默认跟随通用 `🌍 代理` 组。
  - 当开启独立指定（`custom: true`）时，渲染为 **`REJECT` 置顶的 `url-test` 测速组**（间隔 30s，`tolerance: 0`），自动在绑定的纯净节点全协议端点间选择最低延迟链路，并额外注入非 UDP 兜底规则防止 QUIC/HTTP3 旁路泄漏。

## 2. 跨端代码锚点

- **YAML 渲染与规则编排**：`internal/clash/render.go`（`Render`）
- **渲染回归与 mihomo 语法校验测试**：`internal/clash/render_test.go`、`internal/agent/mihomo_integration_test.go`
- **订阅路由选择存储校验**：`internal/manager/store.go`
- **前端订阅表单与专属分流选择器**：`web/src/App.tsx`

## 3. 模块红线

- 开启 `custom: true` 的专属分流组（Claude / ChatGPT / Google）在所绑定节点全部停用、删除或不可用时，**必须严格落入 `REJECT` 阻断**，严禁回退到通用 `🌍 代理` 池（可能含非纯净 IP）或 `DIRECT`（直接暴露用户真实本地 IP 导致封号）。
- 撤销或轮换订阅 URL 仅阻止后续再次拉取配置，不能将其向用户表述为“已吊销客户端本地已下载的代理连接密码”。
