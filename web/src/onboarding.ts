// 节点接入指南与 Agent 执行模板（Canonical 前端镜像）
// 同步约束（见仓库根目录 AGENTS.md）：修改 skills/install-proxy-server/SKILL.md 或
// skills/proxy-cluster-ops/SKILL.md 中的安装、收编、启动顺序、退避自愈、多管理端或自更新步骤时，必须同步更新本文件。

import type { OnboardingDefaults, ProxyNode } from './types';

export type OnboardingMode = 'adopt' | 'fresh' | 'add-manager';
export type ReleaseChannel = 'main' | 'stable' | 'pinned';

export interface OnboardingForm {
  mode: OnboardingMode;
  managerUrl: string;
  enrollToken: string;
  usePrivateCA: boolean;
  caCertPem: string;
  repo: string;
  channel: ReleaseChannel;
  pinnedVersion: string;
  privateRepo: boolean;
  nodeName: string;
  nodeHost: string;
  sshUser: string;
  sshPort: string;
  // 方案 A：存量收编可选覆盖项
  hysteriaConfig: string;
  hysteriaUnit: string;
  xrayConfig: string;
  xrayUnit: string;
  // 方案 B：纯新机器核心与端口参数
  hy2Port: string;
  vlessPort: string;
  hysteriaVersion: string;
  xrayVersion: string;
}

export interface OnboardingStep {
  step: number;
  title: string;
  summary: string;
  code: string;
  note?: string;
}

const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]', '::1']);

export function resolveDefaultManagerURL(serverPublicURL?: string, browserOrigin?: string): string {
  const cleanServer = (serverPublicURL || '').trim().replace(/\/+$/, '');
  const cleanBrowser = (browserOrigin || '').trim().replace(/\/+$/, '');
  try {
    if (cleanServer) {
      const parsed = new URL(cleanServer);
      if (!LOOPBACK_HOSTS.has(parsed.hostname)) {
        return cleanServer;
      }
    }
  } catch {
    // Fall back to browser origin when server URL cannot be parsed.
  }
  return cleanBrowser || cleanServer || 'https://manager.example.com';
}

export function suggestNextNodeName(nodes: ProxyNode[]): string {
  const existing = new Set(nodes.map((n) => n.name.trim().toLowerCase()));
  for (let idx = Math.max(1, nodes.length + 1); idx <= 999; idx += 1) {
    const candidate = `node-${String(idx).padStart(2, '0')}`;
    if (!existing.has(candidate)) {
      return candidate;
    }
  }
  return 'node-new';
}

export function createInitialOnboardingForm(
  defaults: OnboardingDefaults | null,
  nodes: ProxyNode[],
): OnboardingForm {
  const origin = typeof window !== 'undefined' ? window.location.origin : '';
  const ver = defaults?.version && defaults.version !== 'dev' ? defaults.version : '';
  return {
    mode: 'adopt',
    managerUrl: resolveDefaultManagerURL(defaults?.public_url, origin),
    enrollToken: defaults?.enroll_token || '',
    usePrivateCA: Boolean(defaults?.ca_cert_available),
    caCertPem: (defaults?.ca_cert_pem || '').trim(),
    repo: defaults?.default_repo || 'ByteLan/proxy-server-mono',
    channel: 'main',
    pinnedVersion: ver,
    privateRepo: false,
    nodeName: suggestNextNodeName(nodes),
    nodeHost: '',
    sshUser: 'root',
    sshPort: '22',
    hysteriaConfig: '/etc/hysteria/config.yaml',
    hysteriaUnit: 'hysteria.service',
    xrayConfig: '/etc/xray/config.json',
    xrayUnit: 'xray.service',
    hy2Port: '443',
    vlessPort: '443',
    hysteriaVersion: 'v2.6.1',
    xrayVersion: 'v25.1.30',
  };
}

function shellQuote(val: string): string {
  if (/^[A-Za-z0-9._:/=-]+$/.test(val)) {
    return val;
  }
  return `'${val.replace(/'/g, `'\\''`)}'`;
}

function buildBootstrapPrefix(form: OnboardingForm): string {
  const args: string[] = ['node'];
  const repo = form.repo.trim() || 'ByteLan/proxy-server-mono';
  if (repo !== 'ByteLan/proxy-server-mono') {
    args.push('--repo', shellQuote(repo));
  }
  if (form.channel === 'stable') {
    args.push('--channel', 'stable');
  } else if (form.channel === 'pinned' && form.pinnedVersion.trim()) {
    args.push('--version', shellQuote(form.pinnedVersion.trim()));
  }
  if (form.privateRepo) {
    args.push('--token-file', '/root/.proxy-github-pat');
  }
  return args.join(' ');
}

export function buildOnboardingSteps(form: OnboardingForm): OnboardingStep[] {
  const managerUrl = form.managerUrl.trim().replace(/\/+$/, '') || 'https://<MANAGER_HOST>:9443';
  const nodeHost = form.nodeHost.trim() || '<NODE_PUBLIC_IP>';
  const nodeName = form.nodeName.trim() || 'node-01';
  const sshUser = form.sshUser.trim() || 'root';
  const sshPort = form.sshPort.trim() || '22';
  const caPath = '/etc/proxy-server/manager-ca.crt';
  const repo = form.repo.trim() || 'ByteLan/proxy-server-mono';

  const steps: OnboardingStep[] = [];
  let seq = 1;

  const sshHint = sshPort === '22'
    ? `ssh ${sshUser}@${nodeHost}`
    : `ssh -p ${sshPort} ${sshUser}@${nodeHost}`;

  steps.push({
    step: seq++,
    title: '目标主机只读环境探测（决定收编还是新装）',
    summary: `登录目标节点（${sshHint}）后先做只读探测，检查系统规格、存量 Hysteria2 / Xray 服务与监听端口，切勿盲目覆盖已有内核。`,
    code: [
      '# 探测系统架构/内存、存量代理服务、监听端口、UDP 端口跳跃 NAT 与当前拥塞控制',
      'uname -a && nproc && free -m',
      'systemctl is-enabled hysteria.service xray.service proxy-server.service 2>/dev/null || true',
      'systemctl is-active hysteria.service xray.service proxy-server.service 2>/dev/null || true',
      "ss -tulnp | grep -E 'hysteria|xray' || true",
      'sudo iptables -t nat -S PREROUTING 2>/dev/null || true',
      'sysctl net.ipv4.tcp_congestion_control',
      '# 如尚未确认本机对外公网 IPv4，可运行：curl -4fsSL ifconfig.me && echo',
    ].join('\n'),
    note: '若 hysteria.service / xray.service 已在运行，请选「方案 A：存量内核无感收编」；若为空白机器，选「方案 B：纯新机器全套部署」；若已运行 proxy-server 只需多连本管理端，选「场景 C：多管理端接入」。',
  });

  if (form.mode === 'fresh') {
    const hyVer = form.hysteriaVersion.trim() || 'v2.6.1';
    const xrayVer = form.xrayVersion.trim() || 'v25.1.30';
    steps.push({
      step: seq++,
      title: '开启系统 BBR 与准备官方固定版本双核心（纯新机器）',
      summary: '启用 fq + bbr 拥塞控制，并从官方渠道下载固定版本的 Hysteria2 与 Xray 可执行文件及官方 SHA256（禁止盲信未校验的 latest）。',
      code: [
        '# 1. 开启 BBR（幂等写入）',
        "grep -q '^net.ipv4.tcp_congestion_control=bbr' /etc/sysctl.conf || sudo tee -a /etc/sysctl.conf >/dev/null <<'EOF'",
        'net.core.default_qdisc=fq',
        'net.ipv4.tcp_congestion_control=bbr',
        'EOF',
        'sudo sysctl -p',
        '',
        '# 2. 下载固定版本核心并核验官方发布的 64 位 SHA256（以下以 amd64 为例，arm64 请替换为对应资产与官方哈希）',
        `HYSTERIA_VERSION=${shellQuote(hyVer)}`,
        `XRAY_VERSION=${shellQuote(xrayVer)}`,
        'WORK_CORES=$(mktemp -d /tmp/proxy-cores.XXXXXX)',
        'curl -fL "https://github.com/apernet/hysteria/releases/download/app%2F${HYSTERIA_VERSION}/hysteria-linux-amd64" -o "$WORK_CORES/hysteria"',
        'curl -fL "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-64.zip" -o "$WORK_CORES/xray.zip"',
        'python3 -m zipfile -e "$WORK_CORES/xray.zip" "$WORK_CORES/xray-dir"',
        'chmod +x "$WORK_CORES/hysteria" "$WORK_CORES/xray-dir/xray"',
        '',
        '# 请将下方变量替换为官方 Release 页面公布的真实 64 位 SHA256（勿仅用本地重算值代替核验）',
        'HYSTERIA_SHA256="<填入官方_HYSTERIA_64位_SHA256>"',
        'XRAY_SHA256="<填入官方_XRAY_64位_SHA256>"',
      ].join('\n'),
      note: '跨洲高延迟链路下 Hysteria2 (UDP/QUIC) + BBR 能显著提升吞吐，VLESS+REALITY (TCP) 提供 UDP 受限网络兜底。',
    });
  }

  if (form.usePrivateCA) {
    const pemBody = form.caCertPem.trim() || '-----BEGIN CERTIFICATE-----\n<粘贴管理端 /etc/proxy-manage-server/ca.crt 内容>\n-----END CERTIFICATE-----';
    steps.push({
      step: seq++,
      title: '写入管理端私有根证书（纯 IP / 自签 HTTPS 必做）',
      summary: '节点与管理端全程强制验证服务端 TLS 证书（严禁跳过校验）。将当前管理端公开根证书写入节点本地以建立信任链：',
      code: [
        'sudo install -d -m 0755 /etc/proxy-server',
        `cat <<'EOF' | sudo tee ${caPath} >/dev/null`,
        pemBody,
        'EOF',
        `sudo chmod 0644 ${caPath}`,
      ].join('\n'),
      note: form.caCertPem.trim()
        ? '上方证书内容已从当前管理端自动读取预填，只包含公开 CA 证书，不含任何私钥。'
        : '当前管理端未配置 TLS_CA_FILE 自动读取，请将管理端公开 ca.crt 内容粘贴到上方输入框后复制执行。',
    });
  }

  if (form.mode === 'add-manager') {
    const addArgs = [
      'sudo --preserve-env=ENROLL_TOKEN /opt/proxy-server/bin/proxy-server add-manager \\',
      '  --config /etc/proxy-server/config.json \\',
      `  --manager ${shellQuote(managerUrl)}` + (form.usePrivateCA ? ' \\' : ''),
    ];
    if (form.usePrivateCA) {
      addArgs.push(`  --ca-file ${caPath}`);
    }
    steps.push({
      step: seq++,
      title: '将本管理端追加到已有 proxy-server 节点（多管理端并发上报）',
      summary: '单个节点最多支持同时连接 16 个独立管理端，注册与心跳协程彼此隔离：',
      code: [
        '# 安全读取准入令牌（不在命令行明文暴露 ENROLL_TOKEN）',
        'read -rsp "输入当前管理端 ENROLL_TOKEN: " ENROLL_TOKEN && echo && export ENROLL_TOKEN',
        '',
        ...addArgs,
        'unset ENROLL_TOKEN',
        '',
        'sudo systemctl restart proxy-server',
        'sudo /opt/proxy-server/bin/proxy-server list-managers --config /etc/proxy-server/config.json',
      ].join('\n'),
      note: '后续如需解绑某个管理端，运行 proxy-server remove-manager --manager <URL> 并重启 proxy-server 即可。',
    });
  } else {
    const getInstallerLines = form.privateRepo
      ? [
          '# 私有仓库：将单仓库 Contents: Read-only 的 GitHub PAT 存入 0600 临时凭据文件（勿写入命令行参数）',
          'read -rsp "输入只读 GITHUB_TOKEN: " GH_PAT && echo',
          'umask 077 && printf "%s\\n" "$GH_PAT" | sudo tee /root/.proxy-github-pat >/dev/null && unset GH_PAT',
          `sudo sh -c 'curl -fsSL -H "Accept: application/vnd.github.raw" -H "Authorization: Bearer $(cat /root/.proxy-github-pat)" "https://api.github.com/repos/${repo}/contents/scripts/install.sh?ref=main"' -o install.sh`,
          'chmod +x install.sh',
        ]
      : [
          '# 获取零编译引导安装脚本 install.sh（自动核验 Release SHA256SUMS、ELF 架构头与白名单归档）',
          `curl -fsSL "https://raw.githubusercontent.com/${repo}/main/scripts/install.sh" -o install.sh`,
          'chmod +x install.sh',
        ];

    const bootstrapPrefix = buildBootstrapPrefix(form);
    const installCmdLines: string[] = [
      `sudo --preserve-env=ENROLL_TOKEN bash ./install.sh ${bootstrapPrefix} -- \\`,
    ];

    if (form.mode === 'adopt') {
      installCmdLines.push('  --adopt-existing \\');
      installCmdLines.push(`  --manager ${shellQuote(managerUrl)} \\`);
      installCmdLines.push(`  --server ${shellQuote(nodeHost)} \\`);
      const extraAdoptFlags: string[] = [];
      if (form.hysteriaConfig.trim() && form.hysteriaConfig.trim() !== '/etc/hysteria/config.yaml') {
        extraAdoptFlags.push(`  --hysteria-config ${shellQuote(form.hysteriaConfig.trim())}`);
      }
      if (form.hysteriaUnit.trim() && form.hysteriaUnit.trim() !== 'hysteria.service') {
        extraAdoptFlags.push(`  --hysteria-unit ${shellQuote(form.hysteriaUnit.trim())}`);
      }
      if (form.xrayConfig.trim() && form.xrayConfig.trim() !== '/etc/xray/config.json') {
        extraAdoptFlags.push(`  --xray-config ${shellQuote(form.xrayConfig.trim())}`);
      }
      if (form.xrayUnit.trim() && form.xrayUnit.trim() !== 'xray.service') {
        extraAdoptFlags.push(`  --xray-unit ${shellQuote(form.xrayUnit.trim())}`);
      }
      if (form.usePrivateCA) {
        extraAdoptFlags.push(`  --ca-file ${caPath}`);
      }
      if (extraAdoptFlags.length > 0) {
        installCmdLines.push(`  --name ${shellQuote(nodeName)} \\`);
        extraAdoptFlags.forEach((flagLine, idx) => {
          installCmdLines.push(idx < extraAdoptFlags.length - 1 ? `${flagLine} \\` : flagLine);
        });
      } else {
        installCmdLines.push(`  --name ${shellQuote(nodeName)}`);
      }
    } else {
      const hyPort = form.hy2Port.trim() || '443';
      const vlPort = form.vlessPort.trim() || '443';
      installCmdLines.push(`  --manager ${shellQuote(managerUrl)} \\`);
      installCmdLines.push(`  --server ${shellQuote(nodeHost)} \\`);
      installCmdLines.push(`  --name ${shellQuote(nodeName)} \\`);
      installCmdLines.push(`  --hy2-port ${shellQuote(hyPort)} --vless-port ${shellQuote(vlPort)} \\`);
      installCmdLines.push('  --hysteria-binary "$WORK_CORES/hysteria" --hysteria-sha256 "$HYSTERIA_SHA256" \\');
      installCmdLines.push('  --core-version "hysteria=$HYSTERIA_VERSION" \\');
      installCmdLines.push('  --xray-binary "$WORK_CORES/xray-dir/xray" --xray-sha256 "$XRAY_SHA256" \\');
      if (form.usePrivateCA) {
        installCmdLines.push('  --core-version "xray=$XRAY_VERSION" \\');
        installCmdLines.push(`  --ca-file ${caPath}`);
      } else {
        installCmdLines.push('  --core-version "xray=$XRAY_VERSION"');
      }
    }

    steps.push({
      step: seq++,
      title: form.mode === 'adopt'
        ? '下载校验 proxy-server 并无感收编存量内核'
        : '安装 proxy-server 并初始化双协议托管核心',
      summary: form.mode === 'adopt'
        ? '只读导入现有 Hysteria2 / Xray（含 VLESS REALITY / SOCKS5 / HTTP / HTTPS 入站），不覆盖二进制、不改端口密码、不断连：'
        : '安装 Agent 与双协议核心至 /opt/proxy-server/bin，自动生成节点专属凭据并向管理端完成首次注册：',
      code: [
        ...getInstallerLines,
        '',
        '# 通过环境变量安全注入 ENROLL_TOKEN（首次注册换取 0600 专属节点 Token，后续心跳不再使用 ENROLL_TOKEN）',
        'read -rsp "输入管理端 ENROLL_TOKEN: " ENROLL_TOKEN && echo && export ENROLL_TOKEN',
        '',
        ...installCmdLines,
        '',
        'unset ENROLL_TOKEN',
        ...(form.privateRepo ? ['sudo rm -f /root/.proxy-github-pat'] : []),
        ...(form.mode === 'fresh' ? ['rm -rf "$WORK_CORES"'] : []),
      ].join('\n'),
      note: '严禁直接克隆已初始化节点的 /etc/proxy-server 目录给第二台机器；每台节点必须独立执行注册生成唯一身份。',
    });
  }

  if (form.mode === 'adopt') {
    const hyUnit = form.hysteriaUnit.trim() || 'hysteria.service';
    const xrUnit = form.xrayUnit.trim() || 'xray.service';
    steps.push({
      step: seq++,
      title: '核验开机自启、启动顺序（Wants+After）与 10 次退避自愈',
      summary: '确保宿主机重启后先拉起代理内核再启动 Agent，并验证无 polkitd 云镜像下的最小权限恢复触发链路：',
      code: [
        '# 1. 确认存量核心、Agent 与 path 恢复单元均已设为开机自启（enabled）且正在运行（active）',
        `sudo systemctl enable --now ${shellQuote(hyUnit)} ${shellQuote(xrUnit)} proxy-server.service 2>/dev/null || true`,
        `systemctl is-enabled ${shellQuote(hyUnit)} ${shellQuote(xrUnit)} proxy-server.service`,
        `systemctl is-active ${shellQuote(hyUnit)} ${shellQuote(xrUnit)} proxy-server.service`,
        'systemctl list-unit-files "proxy-server-recover-*.path"',
        '',
        '# 2. 核验启动顺序 drop-in（必须为 Wants= + After=，严禁用 Requires=/BindsTo= 以防内核停机连带杀死 Agent）',
        'cat /etc/systemd/system/proxy-server.service.d/adopted-cores.conf',
        '',
        '# 3. 零中断自检恢复触发器（对已运行服务执行 systemctl start 为幂等空操作，不断连）',
        `if [ -d /etc/proxy-server/recover ]; then sudo -u proxy-node sh -c 'echo 1 > /etc/proxy-server/recover/${hyUnit}.trigger' && sleep 1 && test ! -e /etc/proxy-server/recover/${hyUnit}.trigger && echo "RECOVERY_PATH_OK"; fi`,
        'sudo journalctl -u proxy-server -n 20 --no-pager',
      ].join('\n'),
      note: '运行期若某个代理内核意外退出，proxy-server 不会崩溃，而是按指数退避（立即 -> 2s -> 4s ... -> 5m）最多尝试拉起 10 次；超限后停止拉起并在管理端展示告警。',
    });
  } else {
    steps.push({
      step: seq++,
      title: '核验 systemd 开机自启与 Agent 运行日志',
      summary: '确认 proxy-server.service 已开启开机自启且心跳上报正常：',
      code: [
        'sudo systemctl enable --now proxy-server.service',
        'systemctl is-enabled proxy-server.service && systemctl is-active proxy-server.service',
        'sudo journalctl -u proxy-server -n 25 --no-pager',
      ].join('\n'),
      note: '子进程托管模式下，任一核心异常退出（存活不足 1 分钟）会按指数退避最多自动重启 10 次，不会拖垮 Agent 主进程。',
    });
  }

  steps.push({
    step: seq++,
    title: '云防火墙核验、订阅端到端测速与日后升级回退速查',
    summary: '核对云平台安全组 / Network Tags 放行节点代理端口，在控制台加入订阅并用 Clash Verge 验证真实代理流量：',
    code: [
      '# 日后仅升级节点 Agent（原子替换，不改节点配置/Token，不覆盖已收编内核）',
      '/opt/proxy-server/bin/proxy-server version',
      'sudo /opt/proxy-server/bin/proxy-server update --check',
      'sudo /opt/proxy-server/bin/proxy-server update && sudo systemctl restart proxy-server',
      '',
      '# 如升级后异常，一键回退上一版二进制（.previous）',
      'sudo systemctl stop proxy-server && sudo cp /opt/proxy-server/bin/proxy-server.previous /opt/proxy-server/bin/proxy-server && sudo systemctl start proxy-server',
    ].join('\n'),
    note: `最后确认：① 云厂商防火墙（如 GCP Network Tags / 安全组）已放行节点代理 UDP/TCP 端口；② 本控制台「代理节点」列表已出现「${nodeName}」且心跳在线；③ 心跳在线不代表客户端到节点代理端口可达，请添加到「订阅管理」并用客户端实测。`,
  });

  return steps;
}

export function buildOnboardingMarkdown(form: OnboardingForm, includeSecret: boolean): string {
  const managerUrl = form.managerUrl.trim().replace(/\/+$/, '') || 'https://<MANAGER_HOST>:9443';
  const nodeHost = form.nodeHost.trim() || '<NODE_PUBLIC_IP>';
  const nodeName = form.nodeName.trim() || 'node-01';
  const sshUser = form.sshUser.trim() || 'root';
  const sshPort = form.sshPort.trim() || '22';
  const modeLabel = form.mode === 'adopt'
    ? '方案 A：存量内核无感收编 (--adopt-existing)'
    : form.mode === 'fresh'
      ? '方案 B：纯新机器全套部署 (BBR + 固定版本双核 + Agent 托管)'
      : '场景 C：已有 proxy-server 节点接入新管理端 (add-manager)';
  const channelLabel = form.channel === 'pinned' && form.pinnedVersion.trim()
    ? `固定版本 (${form.pinnedVersion.trim()})`
    : form.channel === 'stable'
      ? 'stable（正式版 Release）'
      : 'main（最新预发布 Release）';
  const tokenDisplay = includeSecret
    ? (form.enrollToken.trim() || '<YOUR_ENROLL_TOKEN>')
    : '<YOUR_ENROLL_TOKEN>';

  const steps = buildOnboardingSteps(form);

  const lines: string[] = [
    `# 代理节点接入任务书 · ${nodeName}`,
    '',
    '> 本文档由 `proxy-manage-server`「节点接入指南」根据当前管理端配置自动生成，可直接供运维人员按步执行，或完整粘贴给 AI Agent（Antigravity / Claude Code / Codex）自动完成节点纳管。',
    '',
    '## 1. 目标参数总览',
    '',
    '| 配置项 | 目标值 |',
    '|---|---|',
    `| 管理端地址 (\`--manager\`) | \`${managerUrl}\` |`,
    `| 节点名称 (\`--name\`) | \`${nodeName}\` |`,
    `| 节点公网 IP / 域名 (\`--server\`) | \`${nodeHost}\` |`,
    `| 目标机 SSH 连接 | \`${sshUser}@${nodeHost}:${sshPort}\` |`,
    `| 接入模式 | ${modeLabel} |`,
    `| 发布仓库与渠道 | \`${form.repo.trim() || 'ByteLan/proxy-server-mono'}\` · ${channelLabel}${form.privateRepo ? '（私有库只读 PAT）' : ''} |`,
    `| 管理端 TLS 信任方式 | ${form.usePrivateCA ? '私有根 CA（强制校验 `--ca-file /etc/proxy-server/manager-ca.crt`）' : '系统公共信任证书'} |`,
    `| 准入令牌 (\`ENROLL_TOKEN\`) | \`${tokenDisplay}\` |`,
    '',
    '## 2. AI Agent 自动执行安全红线（必读）',
    '',
    '1. **凭据零泄漏**：`ENROLL_TOKEN`、SSH 密码、GitHub PAT 以及节点代理密码/私钥，**严禁**拼入命令行明文参数（`ps` 可见）、打印到终端标准输出或提交进代码仓库；远端执行时仅通过进程环境变量 `env` 或标准输入传递。',
    '2. **非交互 SSH 防卡死范式**：',
    '   - 若目标机为已登录云平台实例（如 GCP），优先使用平台原生非交互命令（如 `gcloud compute ssh <instance> --zone=<zone> --ssh-flag="-o BatchMode=yes -T" --command=...`）。',
    '   - 若目标机为密码认证独立 VPS，**严禁使用 `pty.fork()` 直接灌入长脚本**（Linux TTY `ICANON` 4096 字节行缓冲会导致永久卡死并回显机密）；必须使用 OpenSSH 8.4+ `SSH_ASKPASS` + `SSH_ASKPASS_REQUIRE=force` + `DISPLAY=dummy:0` 配合 `ssh -T`（禁用伪终端），将远端脚本做 Base64 编码后交由 `python3 -c` 解码执行，并为子进程设置显式超时。',
    '3. **幂等与存量保护**：执行安装前必须先跑 Step 1 只读探测。若主机已有运行中的 `hysteria.service` / `xray.service`，必须采用 `--adopt-existing` 无感收编，严禁覆盖现有内核二进制或重启中断既有代理连接。',
    '',
    '## 3. 标准操作步骤',
    '',
  ];

  for (const s of steps) {
    lines.push(`### Step ${s.step}: ${s.title}`);
    lines.push('');
    lines.push(s.summary);
    lines.push('');
    lines.push('```bash');
    lines.push(s.code);
    lines.push('```');
    if (s.note) {
      lines.push('');
      lines.push(`> **提示**：${s.note}`);
    }
    lines.push('');
  }

  return lines.join('\n');
}
