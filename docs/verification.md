# 首版验证记录

验证日期：2026-11-08。开发机 macOS / arm64。

## 已执行

- `GOTOOLCHAIN=auto make build test check`：前端 TypeScript 检查、Vite 构建、两个 Go 二进制构建、Go 测试、vet 和安装脚本语法检查。
- `GOTOOLCHAIN=auto go test -race -tags stdjson,gjson ./internal/agent ./internal/manager`：Agent / 管理端并发检查。
- 管理端回归：节点 token 绑定、撤销后的 ID 不可重新注册、凭据密文存储与列表脱敏、数据库重启恢复、丢失主密钥拒绝启动、节点选择变化后同一订阅 URL 更新、禁用和删除、空配置、订阅 token 重置/删除。
- Agent 回归：配置生成、证书 SAN 和指纹、REALITY 公私钥对应、文件权限、注册 token 持久化、普通心跳不重复发送凭据、配置变化重报、401 不自动重新注册、子进程停止、TLS CA 验证和拒绝远端 HTTP。
- Chrome 与真实管理 API 联调：登录、节点管理、创建订阅、节点增删、启停、预览、链接重置与撤销；检查桌面和移动端布局。联调测试数据已删除。
- 固定官方 mihomo v1.19.17（darwin-arm64）对 Agent 初始化后生成的 HY2/REALITY 双协议配置执行真实 `-t` 校验通过；下载资产 SHA256 与官方 release digest 一致。可选回归命令：`MIHOMO_TEST_BIN=/path/to/verified/mihomo GOTOOLCHAIN=auto go test ./internal/agent -run TestGeneratedConfigurationWithMihomo`，测试不打开代理监听。
- HTTPS 进程集成：管理端直接加载测试证书，Agent 通过 CA 校验后注册并发送心跳，生成订阅和修改选择。该项使用 sleep 子进程代替协议核心，只验证控制平面。
- 安装脚本普通/TLS staging：目录、敏感文件权限、重装保留配置、错误 SHA256 拒绝、配置/证书复制；不调用本机 systemd。
- 两个服务的 Linux amd64 / arm64（CGO_ENABLED=0）交叉构建通过，产物为静态 ELF。
- 三份 skill 通过 skill-creator 的 `quick_validate.py`；九个发现路径指向同一组 canonical skills。

## 本地管理端资源抽样

管理端为一个 Go 进程，SQLite 页缓存上限约 4 MiB，Go 内存软目标默认 192 MiB。该软目标不等于操作系统硬内存上限。

一次本地顺序请求抽样：空闲 RSS 约 **25.0 MiB**；登记 101 个节点并连续获取包含全部节点的订阅 30 次后，RSS 约 **34.5 MiB**。配置约 29 KiB，HTTPS 拉取延迟中位数约 4.6 ms、95 分位约 5.3 ms。这些是开发机测量值，没有限制 CPU 或内存，不能视为 1 核 / 1 GiB VPS 的性能承诺，也不包含代理核心的资源消耗。

## 构建选择

管理端使用 Hertz 标准网络库，以及 `stdjson,gjson` build tags。`stdjson` 用于 JSON 编解码，`gjson` 用于 amd64 架构的 binding 检查；两者一起避免 Sonic 对 Go runtime 内部实现的版本绑定。构建命令见 Makefile。

SQLite 使用 modernc.org/sqlite v1.46.2（SQLite 3.51.3），单数据库连接、WAL、busy timeout；不需要 CGO 或独立数据库服务。

## 尚未完成的环境验证

未在真实 Linux systemd 主机安装运行，也未做 1 核 / 1 GiB 的长时间压力、流量吞吐或公网跨地域测试。实际上线前需要固定协议核心与客户端版本，逐协议验证真实数据流、证书、UDP/TCP 防火墙和网络可达性。管理端不运行协议核心。
