# Proxy Monorepo 工程指引

## 1. 顶层结构与构建红线

- **目录拓扑**：
  - `cmd/proxy-manage-server`、`internal/manager`、`internal/clash`、`internal/webui`：Go / Hertz 控制面（SQLite 单实例，通过 `go:embed` 内嵌编译后的前端产物 `internal/webui/dist`）。
  - `cmd/proxy-server`、`internal/agent`：Go 节点 Agent（只负责上报控制面与本地监控/收编协议核心）。
  - `web/`：React / TypeScript / Vite / Arco Design 前端源码。
  - `scripts/`、`deploy/systemd/`：本地多架构打包发布、服务端零编译安装器与上下文门禁脚本。
- **架构与安全铁律**：
  - `proxy-manage-server` **仅作为控制面**，严禁在其中添加代理监听器、流量转发或协议核心。
  - 严禁提交任何运行凭据、节点配置、服务端私钥、SQLite 数据库文件或生成的客户端 YAML。
  - 仓库不配置 GitHub Actions 自动构建或自动发布；构建在开发机完成，目标 Linux 主机规格为 1 CPU / 1 GiB。
- **构建与校验命令**（Go module: `proxy-mono`，手动运行 `go build/test/vet` 必须带 `-tags stdjson,gjson`）：
  - 前后端完整构建：`GOTOOLCHAIN=auto make build`（仅构建 Go：`GOTOOLCHAIN=auto make build-go`）
  - 单元与脚本测试：`GOTOOLCHAIN=auto make test`
  - 静态与门禁检查：`GOTOOLCHAIN=auto make check`

## 2. 上下文读写路由（三层分离）

1. **读当前设计（`product-spec/`）**：修改业务逻辑、配置模型或接口前，先运行 `find product-spec/ -maxdepth 3 -type f`，按需阅读对应模块当前生效设计（`product-spec/<模块>/<文档>.md`）。
2. **同步回写设计**：若改动变更了业务规则、配置行为或 API 契约，合入前必须**原地更新**对应的 `product-spec/.../*.md`（文件名与标题严禁添加 `v1.0` 等静态版本号）。
3. **写单次方案（`docs/YYYY-MM-DD-<topic>/`）**：单次迭代的需求背景、技术评估与施工方案统一写入带日期前缀的 `docs/YYYY-MM-DD-<topic>/`，上线并回写 `product-spec/` 后原目录留存归档，免人工搬家。
4. **查历史背景**：对某处逻辑或历史演进存疑时，通过 `git log` / `git blame` 反查关联提交及对应的 `docs/YYYY-MM-DD-*` 文档。

## 3. 按需加载技能（Skills）与前端接入向导强同步

服务器安装、存量收编、跨机纳管与协议部署属于按需操作知识，不在本文件常驻展开。Canonical 技能位于 `skills/`（`.agents/skills`、`.claude/skills`、`.codex/skills` 通过相对软链对齐）：

- [proxy-cluster-ops](skills/proxy-cluster-ops/SKILL.md)：多服务器集群实战运维与排障（`gcloud`/`SSH_ASKPASS` 非交互连接避坑、存量内核探测与 `--adopt-existing` 判定、纯 IP 私有 CA 与 Clash 双端口订阅、`systemd` 启动顺序与 10 次退避自愈、多管理端接入）。
- [install-proxy-manage-server](skills/install-proxy-manage-server/SKILL.md)：下载、初始化、升级或回退 SQLite 管理端。
- [install-proxy-server](skills/install-proxy-server/SKILL.md)：下载、初始化/收编、升级或回退代理节点 Agent。
- [proxy-server-setup](skills/proxy-server-setup/SKILL.md)：底层 Hysteria2 / Xray 单机协议部署参数参考。

- **安装技能与管理端页面双向同步铁律**：管理后台「节点接入指南」页面（模板源码 `web/src/onboarding.ts`，产品设计 `product-spec/管理控制台/节点接入向导.md`）面向人工运维与 AI Agent 动态生成节点探测、安装/收编、开机自启与自愈核验步骤。凡修改 `skills/install-proxy-server/SKILL.md`、`skills/proxy-cluster-ops/SKILL.md` 或节点安装/启动参数时，**必须同步更新 `web/src/onboarding.ts` 并重新构建前端产物**；反之修改页面向导步骤时也必须同步回写对应 Skill。

## 4. 文档编写门禁

- **禁止硬编码本机个人路径**：文档与脚本一律使用仓库相对路径（或目标 Linux 标准服务路径如 `/etc/...`、`/opt/...`），严禁写入开发机个人主目录绝对路径。
- **控制常驻体积**：`AGENTS.md` 单文件保持 ≤ 150 行，禁止塞入服务器安装流水账或业务枚举大表；提交前运行 `make check` 校验。
