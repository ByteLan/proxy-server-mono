---
name: proxy-cluster-ops
description: >-
  多服务器代理集群实战运维与排障指南：涵盖远程云服务器非交互连接避坑（gcloud 与 SSH_ASKPASS 免卡死）、
  存量代理内核只读探测与无感收编（--adopt-existing）判定、纯 IP 无域名管理端私有 CA/IP-SAN 与 Clash HTTP/HTTPS 双端口配置、
  systemd 启动顺序（Wants+After）与无 polkitd 环境下的 10 次退避自愈、单机双角色共存及多管理端（add-manager）接入。
  用于真实服务器集群配置、存量机器纳管、跨机注册与故障排查。
---

# 多服务器代理集群实战运维指南

本技能沉淀真实云服务器（GCP / 独立海外 VPS）部署与纳管 `proxy-manage-server`、`proxy-server` 的实战经验与避坑规范。执行具体单组件安装参数时配合阅读 [install-proxy-manage-server](../install-proxy-manage-server/SKILL.md) 与 [install-proxy-server](../install-proxy-server/SKILL.md)；模块设计边界见 [节点收编与自愈规范](../../product-spec/节点Agent/收编托管与多管理端自愈.md) 与 [发布安装规范](../../product-spec/发布与安装升级/免编译发布安装与自更新.md)。

---

## 1. 远程连接与凭据零泄漏铁律

1. **机密隔离**：SSH 密码、`ADMIN_TOKEN`、`ENROLL_TOKEN`、节点 Token、GitHub PAT 以及节点代理密码/私钥，**严禁**出现在命令行参数（`ps` 可见）、终端标准输出、日志或仓库文件中。
2. **云厂商主机**：如目标机属于已登录的云平台（例如 GCP），优先走平台原生通道（如 `gcloud compute ssh <instance> --zone=<zone> --command=...`）。
3. **密码认证的独立 VPS（规避伪终端卡死与回显）**：
   - **严禁用 `pty.fork()` 直接向远程伪终端灌入长文本或 JSON 负载**：Linux TTY 默认开启 `ICANON`（单行缓冲上限 4096 字节）与输入回显，极易造成进程永久挂起或将凭据回显到日志。
   - **标准做法**：使用 OpenSSH 8.4+ 原生 `SSH_ASKPASS` + `SSH_ASKPASS_REQUIRE=force` + `DISPLAY=dummy:0` 配合 `ssh -T`（禁用伪终端，保持干净的 8-bit 标准输入管道），并将远端执行脚本做 Base64 编码后交给 `python3 -c` 解码执行（避免 `ssh` 拼接远端参数引发 `bash -c` 括号语法错误），所有子进程调用必须设置显式 `timeout`：

   ```python
   # 本地自动化远程执行安全范式（密码从私密文件读取，不进 argv / 日志）
   import base64, json, os, subprocess, tempfile

   remote_py = """
   import json, sys
   payload = json.loads(sys.stdin.read())
   # 使用 payload['enroll'] 等放入子进程 env，不在 stdout 打印秘密
   """
   b64 = base64.b64encode(remote_py.encode()).decode("ascii")
   remote_cmd = f"python3 -c 'import base64; exec(base64.b64decode(\"{b64}\").decode())'"

   with tempfile.TemporaryDirectory() as tmp:
       askpass = os.path.join(tmp, "askpass.sh")
       with open(askpass, "w") as f:
           f.write("#!/bin/sh\ntr -d '\\r\\n' < \"$SSH_PASS_FILE\"\nprintf '\\n'\n")
       os.chmod(askpass, 0o700)
       env = {**os.environ, "SSH_ASKPASS": askpass, "SSH_ASKPASS_REQUIRE": "force", "DISPLAY": "dummy:0"}
       subprocess.run(
           ["ssh", "-T", "-p", str(port), "-o", "StrictHostKeyChecking=accept-new",
            "-o", "ConnectTimeout=15", "-o", "PubkeyAuthentication=no",
            f"{user}@{host}", remote_cmd],
           input=json.dumps(secret_payload), text=True, env=env, timeout=90, check=True,
       )
   ```

---

## 2. 上手先做只读探测（决定方案 A 收编还是方案 B 新装）

登录任何目标节点机后，**先做只读环境探测，绝不盲目下发安装命令**：

```bash
# 探测系统规格、已有代理服务状态、监听端口与 NAT 端口跳跃规则（不打印密码与私钥）
uname -a && free -m
systemctl is-enabled hysteria.service xray.service 2>/dev/null || true
systemctl is-active hysteria.service xray.service 2>/dev/null || true
ss -tulnp | grep -E 'hysteria|xray' || true
iptables -t nat -S PREROUTING 2>/dev/null || true
```

- **判定为方案 A（`--adopt-existing` 无感收编）**：`hysteria.service` 或 `xray.service` 已存在并正在运行（例如此前按 `proxy-server-setup` 手工搭建，含单端口或多端口入站 `hysteria2` / `vless` REALITY / `socks5` / `http` / `https`）。
  - 直接使用 `install.sh node -- --adopt-existing ...`：**不覆盖内核二进制、不改端口与密码、不重启内核、不中断现有客户端连接**。
  - 注意：若 `iptables -t nat` 存在 UDP 端口跳跃（如 `50000:55000 -> 24443`），`adopt` 会提取主监听端口（如 `24443`），原有跳跃规则继续生效不受影响。
- **判定为方案 B（全新托管安装）**：机器上无任何运行中的代理内核服务。需先准备固定版本的官方 `hysteria` 与 `xray` 二进制及可信 SHA256，再运行默认安装流程。

---

## 3. 纯 IP 无域名部署管理端：私有 CA + Clash 双端口方案

当 `proxy-manage-server` 部署在只有公网 IP、没有域名的云主机上时，需同时解决**节点 TLS 严格校验**与 **Clash 客户端快速导入拒绝自签 HTTPS** 两个问题：

### 3.1 签发含 IP SAN 的私有 CA 与服务端证书

管理端与节点间通信禁止关闭证书校验。在管理端生成私有根 CA（`ca.crt`）及包含公网 IP `subjectAltName` 的服务端证书：

```bash
sudo install -d -m 0700 /etc/proxy-manage-server/pki
sudo openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -days 3650 -nodes \
  -subj "/CN=proxy-manage-ca" \
  -keyout /etc/proxy-manage-server/pki/ca.key \
  -out /etc/proxy-manage-server/pki/ca.crt

sudo openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -days 825 -nodes \
  -subj "/CN=<MANAGER_PUBLIC_IP>" \
  -addext "subjectAltName=IP:<MANAGER_PUBLIC_IP>,IP:127.0.0.1,DNS:localhost" \
  -addext "extendedKeyUsage=serverAuth" \
  -keyout /etc/proxy-manage-server/pki/server.key \
  -out /tmp/manager.csr

sudo openssl x509 -req -in /tmp/manager.csr \
  -CA /etc/proxy-manage-server/pki/ca.crt -CAkey /etc/proxy-manage-server/pki/ca.key \
  -CAcreateserial -days 825 -copy_extensions copy \
  -out /etc/proxy-manage-server/pki/server.crt
sudo rm -f /tmp/manager.csr
```

### 3.2 启用 HTTPS 控制面 + HTTP 只读订阅面双端口

Clash Verge / mihomo 客户端通过 URL 导入自签 IP HTTPS 链接时会报证书校验失败。在管理端同时开启只读 HTTP 订阅监听：

```bash
sudo bash ./install.sh manager -- \
  --listen 0.0.0.0:9443 \
  --public-url https://<MANAGER_PUBLIC_IP>:9443 \
  --tls-cert /etc/proxy-manage-server/pki/server.crt \
  --tls-key /etc/proxy-manage-server/pki/server.key \
  --http-sub-listen 0.0.0.0:9080 \
  --http-sub-public-url http://<MANAGER_PUBLIC_IP>:9080
```

- **安全边界**：`9443`（HTTPS）承载管理后台 UI、`/api/admin/*` 与 `/api/agent/*`；`9080`（HTTP）在代码层硬隔离，**仅注册 `/sub/:token` 与 `/healthz`**。
- **浏览器密码管理器与自签证书限制**：Chrome / Edge / Safari 在未受信任的自签 HTTPS 页面（地址栏显示红色“不安全”）会从内核层禁用密码保存弹窗。可将公开根证书复制为 `/etc/proxy-manage-server/ca.crt`（`0644`，严禁混入 `ca.key`）并在 `manager.env` 设置 `TLS_CA_FILE=/etc/proxy-manage-server/ca.crt`，登录页即展示 `/ca.crt` 一键下载入口供导入系统钥匙串信任；同时登录页内置「在此浏览器记住登录状态」勾选项，免导入证书也可持久免输。
- **云防火墙必查**：在 GCP / AWS / 阿里云放行 `tcp:9443` 与 `tcp:9080` 时，务必核对实例自身的 `Network Tags` / 安全组是否与防火墙规则 `target-tags` 匹配。

---

## 4. 开机自启、启动顺序与内核异常自愈规范

### 4.1 必须核验的自启清单

部署完成后，在管理端与各节点机显式确认开机自启（`enabled`）与运行态（`active`）：

- **管理端主机**：`proxy-manage-server.service`
- **收编模式节点机**：`proxy-server.service`、`hysteria.service`、`xray.service` 以及 `proxy-server-recover-hysteria-service.path`、`proxy-server-recover-xray-service.path`

### 4.2 为什么用 `Wants=` + `After=`，严禁用 `Requires=` / `BindsTo=`

`install-proxy-server.sh --adopt-existing` 自动写入 `/etc/systemd/system/proxy-server.service.d/adopted-cores.conf`：

```ini
[Unit]
Wants=hysteria.service xray.service
After=hysteria.service xray.service
```

- **冷启动顺序**：系统重启时先拉起代理内核，再启动 `proxy-server`，避免首拍心跳误报内核离线；若内核漏设 `enable`，`Wants=` 也会顺带将其拉起。
- **故障隔离**：绝不使用 `Requires=` 或 `BindsTo=`——否则任一内核崩溃或人工停止时，systemd 会连带杀死 `proxy-server`，导致节点彻底失联且无法执行退避自愈。

### 4.3 精简云镜像（无 `polkitd`）下的最小权限 10 次退避自愈

- **云镜像陷阱**：Ubuntu 24.04 等精简云镜像默认**未安装 `polkitd`**，且 `proxy-server.service` 运行在 `User=proxy-node` + `NoNewPrivileges=true` 沙箱下（`sudo` 不可用，非 root 直接执行 `systemctl start` 会被拒绝）。
- **内置机制**：安装脚本在 `--adopt-existing` 模式下自动配置 root 级 `proxy-server-recover-<unit>.{path,service}`，监听 `/etc/proxy-server/recover/<unit>.trigger`（由 `ExecStopPost=-/bin/rm -f` 在启动结束时删除作为同步完成信号）。`proxy-server` 检测到内核停止后按指数退避（`立即第 1 次 -> 2s -> 4s -> ... -> 5m`）最多尝试拉起 **10 次**；恢复后计数清零，满 10 次仍失败则停止拉起并持续上报 `gave up after 10 recovery attempts`。
- **零中断自检方法**（对已运行服务执行 `systemctl start` 是幂等 no-op，不断连）：

  ```bash
  sudo -u proxy-node sh -c 'echo 1 > /etc/proxy-server/recover/hysteria.service.trigger'
  sleep 1 && test ! -e /etc/proxy-server/recover/hysteria.service.trigger && echo "RECOVERY_PATH_OK"
  ```

---

## 5. 单机双角色共存与跨机多管理端接入

1. **单机同时跑管理端 + 节点 Agent**：`proxy-manage-server`（`/opt/proxy-manage-server`、`/etc/proxy-manage-server`）与 `proxy-server`（`/opt/proxy-server`、`/etc/proxy-server`）用户、目录与端口完全隔离，可在同一台机器共存；本机节点传入 `--ca-file /etc/proxy-manage-server/pki/ca.crt` 即可接入本机管理端。
2. **远端节点跨机接入**：先将管理端 `ca.crt` 安全传输至节点临时文件（`0600`），通过环境变量注入 `ENROLL_TOKEN` 执行 `install.sh node -- --adopt-existing --manager https://<MANAGER_IP>:9443 --server <NODE_IP> --name <NAME> --ca-file /tmp/ca.pem`，完成后清理临时文件。
3. **同一节点同时上报多个管理端**：后续新增独立管理端时，在节点执行：

   ```bash
   sudo --preserve-env=ENROLL_TOKEN /opt/proxy-server/bin/proxy-server add-manager \
     --config /etc/proxy-server/config.json \
     --manager https://<SECOND_MANAGER>:9443 --ca-file /path/to/second-ca.crt
   sudo systemctl restart proxy-server
   ```

---

## 6. 发布、升级与验收排障速查

- **`make publish` 偶发 `Release is no longer a draft`**：GitHub API 创建 draft 后的读写副本秒级延迟所致，直接重跑同一条 `publish-release.sh` 命令即可幂等恢复同名 draft 并完成上传。
- **选择 `update` 还是重跑 `install.sh`**：
  - 仅更新前后端或 Agent 代码逻辑：`proxy-manage-server update` / `proxy-server update` + `systemctl restart`（最快，自动保留 `.previous`）。
  - 更新涉及 `systemd` unit、drop-in 或 `.path` 恢复单元变更：重新运行 `install.sh`（自动保留已有 `config.json`、`manager.env`、SQLite 与节点 Token）。
- **管理端 API 终态验收**：部署后在管理端本机用 `ADMIN_TOKEN` 请求 `https://127.0.0.1:9443/api/admin/nodes`（仅输出脱敏的 `id`、`name`、`online`、端口列表与 `cores` 状态），确认所有节点 `online: true` 且各核心 `running: true`。
