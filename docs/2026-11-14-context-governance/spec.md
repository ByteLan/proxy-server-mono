# 上下文工程治理与集群运维技能沉淀

日期：2026-11-14。

## 1. 背景与问题

- 此前根 `AGENTS.md` 同时塞入了工程构建红线、订阅 Clash YAML 分流业务规则、以及 `--adopt-existing` / `add-manager` / Release 安装升级等服务器部署知识，每次会话常驻加载造成上下文噪音。
- 原 `docs/` 目录混放“当前生效的系统/接口/部署设计”（`architecture.md`、`api.md`、`deployment.md`、`releases.md`）与“单次时间点的验证/评估记录”（`verification.md`、`source-build.md`），缺少清晰的信息生命周期分层。
- 多台真实云服务器（GCP `sg01` 与独立 VPS `us01`）纳管过程中积累的关键排障经验（`SSH_ASKPASS` 规避伪终端行缓冲卡死、纯 IP 私有 CA + Clash HTTP/HTTPS 双端口、无 `polkitd` 精简云镜像下的 `systemd.path` 10 次退避自愈）需要沉淀为按需加载的专用 Skill。

## 2. 治理重构方案

按信息生命周期将仓库文档资产拆分为三层，并将运维操作流程收拢至 `skills/`：

1. **常驻工程宪法（`AGENTS.md`，≤ 150 行）**：仅保留目录拓扑、构建测试红线、上下文读写路由、Skills 索引与相对路径规范，彻底剔除服务器安装步骤与业务细节。
2. **模块当前设计（`product-spec/<模块>/<文档>.md`）**：按模块收敛当前生效的业务规则、跨端代码锚点与模块红线，无静态版本号，随需求演进原地更新：
   - `product-spec/架构与安全边界/系统拓扑与进程边界.md`
   - `product-spec/架构与安全边界/接口契约.md`
   - `product-spec/订阅与分流/Clash订阅生成与纯净分流.md`
   - `product-spec/节点Agent/收编托管与多管理端自愈.md`
   - `product-spec/发布与安装升级/免编译发布安装与自更新.md`
3. **按需操作流程（`skills/*/SKILL.md`）**：新增 `skills/proxy-cluster-ops/SKILL.md`（同步建立 `.agents/skills`、`.claude/skills`、`.codex/skills` 软链），并与 `install-proxy-manage-server`、`install-proxy-server`、`proxy-server-setup` 互链对齐。
4. **单次迭代与评估归档（`docs/YYYY-MM-DD-<topic>/`）**：将历史验证与源码构建评估归档至带日期前缀目录，后续单次方案统一写入日期目录，免搬家留存。
5. **自动化门禁（`scripts/check-context.sh`，接入 `make check`）**：自动校验 `CTX-001`（`AGENTS.md` 行数上限）、`CTX-002`（`product-spec/` 层级与禁静态版本号）、`CTX-003`（`docs/` 日期目录格式）与 `CTX-004`（禁止硬编码个人本机绝对路径）。
