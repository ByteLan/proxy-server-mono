import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert, Badge, Button, Card, Checkbox, Empty, Input, Message, Modal, Radio, Select,
  Space, Spin, Switch, Table, Tag, Tooltip,
} from '@arco-design/web-react';
import type { TableColumnProps } from '@arco-design/web-react';
import {
  IconApps, IconArrowRight, IconCheckCircle, IconCopy, IconDesktop,
  IconLink, IconPlus, IconPoweroff, IconRefresh, IconSearch, IconSettings,
} from '@arco-design/web-react/icon';
import { APIError, request } from './api';
import type { ProxyNode, SiteRoutes, Subscription, SubscriptionInput } from './types';

const TOKEN_KEY = 'proxy-console-admin-token';
const protocolOptions = [
  { label: 'Hysteria2', value: 'hysteria2' }, { label: 'VLESS / REALITY', value: 'vless' },
  { label: 'SOCKS5', value: 'socks5' }, { label: 'HTTP', value: 'http' },
  { label: 'HTTPS', value: 'https' },
];
const siteRouteMeta: Array<{ key: keyof SiteRoutes; label: string; badge: string; desc: string; color: string }> = [
  { key: 'claude', label: '🔒 Claude / Anthropic', badge: 'Claude', desc: 'claude.ai · anthropic.com · Claude Code', color: 'arcoblue' },
  { key: 'chatgpt', label: '🤖 ChatGPT / Codex', badge: 'ChatGPT', desc: 'chatgpt.com · openai.com · GPT · Codex · Sora', color: 'green' },
  { key: 'google', label: '🔍 Google / Gemini', badge: 'Google', desc: 'google · gemini · antigravity · youtube', color: 'purple' },
];
const emptyRoutes = (): SiteRoutes => ({
  claude: { custom: false, node_ids: [] },
  chatgpt: { custom: false, node_ids: [] },
  google: { custom: false, node_ids: [] },
});
const normalizeRoutes = (routes?: Partial<SiteRoutes>): SiteRoutes => ({
  claude: { custom: Boolean(routes?.claude?.custom), node_ids: routes?.claude?.custom ? [...(routes.claude.node_ids || [])] : [] },
  chatgpt: { custom: Boolean(routes?.chatgpt?.custom), node_ids: routes?.chatgpt?.custom ? [...(routes.chatgpt.node_ids || [])] : [] },
  google: { custom: Boolean(routes?.google?.custom), node_ids: routes?.google?.custom ? [...(routes.google.node_ids || [])] : [] },
});
const protocolLabel = (value: string) => protocolOptions.find((item) => item.value === value)?.label || value;
const endpoint = (host: string, port: number) => `${host.includes(':') ? `[${host}]` : host}:${port}`;
const formatTime = (seconds: number) => seconds ? new Date(seconds * 1000).toLocaleString('zh-CN', { hour12: false }) : '尚无心跳';
const messageOf = (error: unknown) => error instanceof Error ? error.message : '操作失败，请稍后重试。';

function Login({ onLogin }: { onLogin: (token: string) => void }) {
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const formToken = String(new FormData(event.currentTarget).get('password') || '').trim();
    const trimmed = input.trim() || formToken;
    if (!trimmed) { setError('请输入管理令牌。'); return; }
    setBusy(true); setError('');
    try {
      await request(trimmed, '/health');
      try {
        const PasswordCred = (window as unknown as {
          PasswordCredential?: new (data: { id: string; password: string; name?: string }) => Credential;
        }).PasswordCredential;
        if (PasswordCred && navigator.credentials?.store) {
          await navigator.credentials.store(new PasswordCred({ id: 'admin', password: trimmed, name: 'Proxy Console' }));
        }
      } catch {
        // Ignore in non-secure contexts or browsers without PasswordCredential.
      }
      onLogin(trimmed);
    } catch (cause) { setError(messageOf(cause)); }
    finally { setBusy(false); }
  }
  return <div className="login-page">
    <div className="login-brand"><div className="brand-mark"><IconApps /></div><span>PROXY CONSOLE</span></div>
    <div className="login-layout">
      <div className="login-intro"><div className="eyebrow">YOUR NODES, ONE PLACE</div>
        <h1>连接每一个节点。<br /><span>管理每一份订阅。</span></h1>
        <p>查看代理服务器的运行状态，按需组合节点，<br />让 Clash Verge 持续获取最新配置。</p>
        <div className="login-feature"><IconCheckCircle /> 轻量部署 <span /> 动态订阅 <span /> 集中管理</div>
      </div>
      <Card className="login-card" bordered={false}>
        <div className="login-icon"><IconSettings /></div><h2>登录管理控制台</h2>
        <p>使用安装管理服务时生成的管理员令牌。</p>
        <form method="post" action="/" autoComplete="on" onSubmit={submit}>
          <input
            type="text"
            name="username"
            autoComplete="username"
            defaultValue="admin"
            readOnly
            tabIndex={-1}
            aria-hidden="true"
            className="login-username-anchor"
          />
          <label className="field-label" htmlFor="admin-token">管理令牌</label>
          <Input.Password
            id="admin-token"
            name="password"
            autoComplete="current-password"
            placeholder="输入 ADMIN_TOKEN"
            value={input}
            onChange={setInput}
            size="large"
          />
          {error && <div className="login-error" role="alert">{error}</div>}
          <Button type="primary" htmlType="submit" long size="large" loading={busy}>进入控制台 <IconArrowRight /></Button>
        </form>
        <div className="login-note">支持浏览器密码管理器保存与自动填充；运行态令牌仅保存在当前标签页会话中。</div>
      </Card>
    </div><div className="login-footer">Proxy Console · 节点与订阅管理</div>
  </div>;
}

export default function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_KEY) || '');
  const [page, setPage] = useState<'nodes' | 'subscriptions'>('nodes');
  const [nodes, setNodes] = useState<ProxyNode[]>([]);
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [updatedAt, setUpdatedAt] = useState('');
  const [search, setSearch] = useState('');
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [nodeEditor, setNodeEditor] = useState<ProxyNode | null>(null);
  const [nodeName, setNodeName] = useState('');
  const [nodeEnabled, setNodeEnabled] = useState(true);
  const [subEditor, setSubEditor] = useState<Subscription | 'new' | null>(null);
  const [subName, setSubName] = useState('');
  const [subNodeIDs, setSubNodeIDs] = useState<string[]>([]);
  const [subProtocols, setSubProtocols] = useState<string[]>([]);
  const [subRoutes, setSubRoutes] = useState<SiteRoutes>(emptyRoutes);
  const [subEnabled, setSubEnabled] = useState(true);
  const [saving, setSaving] = useState(false);
  const [pendingSubIDs, setPendingSubIDs] = useState<string[]>([]);
  const [linkSub, setLinkSub] = useState<Subscription | null>(null);
  const [preview, setPreview] = useState<{ name: string; yaml: string } | null>(null);
  const mounted = useRef(true);

  const logout = useCallback(() => {
    sessionStorage.removeItem(TOKEN_KEY); setToken(''); setNodes([]); setSubscriptions([]);
    setNodeEditor(null); setSubEditor(null); setLinkSub(null); setPreview(null); setSelectedIDs([]);
  }, []);

  const handleError = useCallback((error: unknown) => {
    Message.error(messageOf(error));
    if (error instanceof APIError && error.status === 401) logout();
  }, [logout]);

  const refresh = useCallback(async (visible = false) => {
    if (!token) return;
    if (visible) setLoading(true);
    try {
      const [nextNodes, nextSubs] = await Promise.all([
        request<ProxyNode[]>(token, '/nodes'), request<Subscription[]>(token, '/subscriptions'),
      ]);
      if (!mounted.current) return;
      setNodes(nextNodes); setSubscriptions(nextSubs); setLoadError('');
      setLinkSub((current) => (current ? nextSubs.find((item) => item.id === current.id) || null : null));
      setSelectedIDs((current) => current.filter((id) => nextNodes.some((node) => node.id === id)));
      setUpdatedAt(new Date().toLocaleTimeString('zh-CN', { hour12: false }));
    } catch (error) {
      if (!mounted.current) return;
      setLoadError(messageOf(error));
      if (error instanceof APIError && error.status === 401) logout();
    } finally { if (mounted.current) setLoading(false); }
  }, [token, logout]);

  useEffect(() => {
    mounted.current = true;
    if (!token) return;
    void refresh(true);
    const timer = window.setInterval(() => void refresh(), 15000);
    return () => { mounted.current = false; window.clearInterval(timer); };
  }, [token, refresh]);

  function login(value: string) { sessionStorage.setItem(TOKEN_KEY, value); setToken(value); }
  function openNode(node: ProxyNode) { setNodeEditor(node); setNodeName(node.name); setNodeEnabled(node.enabled); }
  function openSub(sub: Subscription | 'new') {
    setSubEditor(sub); setSubName(sub === 'new' ? '' : sub.name);
    setSubNodeIDs(sub === 'new' ? selectedIDs : sub.node_ids);
    setSubProtocols(sub === 'new' ? [] : sub.protocols);
    setSubRoutes(sub === 'new' ? emptyRoutes() : normalizeRoutes(sub.routes));
    setSubEnabled(sub === 'new' || sub.enabled);
  }
  function setRouteCustom(key: keyof SiteRoutes, custom: boolean) {
    setSubRoutes((prev) => ({
      ...prev,
      [key]: { custom, node_ids: custom ? prev[key].node_ids : [] },
    }));
  }
  function setRouteNodes(key: keyof SiteRoutes, ids: string[]) {
    setSubRoutes((prev) => ({
      ...prev,
      [key]: { custom: true, node_ids: ids },
    }));
  }
  async function saveNode() {
    if (!nodeEditor || !nodeName.trim()) { Message.warning('请输入节点名称。'); return; }
    setSaving(true);
    try {
      await request(token, `/nodes/${encodeURIComponent(nodeEditor.id)}`, { method: 'PATCH', body: JSON.stringify({ name: nodeName.trim(), enabled: nodeEnabled }) });
      setNodeEditor(null); Message.success('节点已更新'); await refresh();
    } catch (error) { handleError(error); } finally { setSaving(false); }
  }
  async function saveSub() {
    if (!subEditor || !subName.trim()) { Message.warning('请输入订阅名称。'); return; }
    setSaving(true);
    const body: SubscriptionInput = {
      name: subName.trim(),
      node_ids: subNodeIDs,
      protocols: subProtocols,
      routes: normalizeRoutes(subRoutes),
      enabled: subEnabled,
    };
    try {
      if (subEditor === 'new') {
        const result = await request<{ subscription: Subscription; url: string; http_url?: string }>(token, '/subscriptions', { method: 'POST', body: JSON.stringify(body) });
        setLinkSub({ ...result.subscription, url: result.url, http_url: result.http_url }); setPage('subscriptions');
      } else {
        await request(token, `/subscriptions/${encodeURIComponent(subEditor.id)}`, { method: 'PUT', body: JSON.stringify(body) });
        Message.success('订阅已更新，Clash Verge 下次刷新将获取新配置');
      }
      setSubEditor(null); await refresh();
    } catch (error) { handleError(error); } finally { setSaving(false); }
  }
  async function removeNode(node: ProxyNode) {
    try {
      await request(token, `/nodes/${encodeURIComponent(node.id)}`, { method: 'DELETE' });
      Message.success('节点已删除，订阅将在下次刷新时移除它'); await refresh();
    } catch (error) { handleError(error); throw error; }
  }
  async function toggleSub(sub: Subscription) {
    if (pendingSubIDs.includes(sub.id)) return;
    setPendingSubIDs((current) => [...current, sub.id]);
    try {
      await request(token, `/subscriptions/${encodeURIComponent(sub.id)}`, {
        method: 'PUT',
        body: JSON.stringify({
          name: sub.name,
          node_ids: sub.node_ids,
          protocols: sub.protocols,
          routes: normalizeRoutes(sub.routes),
          enabled: !sub.enabled,
        }),
      });
      Message.success(sub.enabled ? '订阅已停用' : '订阅已启用'); await refresh();
    } catch (error) { handleError(error); }
    finally { setPendingSubIDs((current) => current.filter((id) => id !== sub.id)); }
  }
  async function removeSub(sub: Subscription) {
    try {
      await request(token, `/subscriptions/${encodeURIComponent(sub.id)}`, { method: 'DELETE' });
      Message.success('订阅已删除'); await refresh();
    } catch (error) { handleError(error); throw error; }
  }
  async function rotateSub(sub: Subscription) {
    try {
      const result = await request<{ subscription: Subscription; url: string; http_url?: string }>(token, `/subscriptions/${encodeURIComponent(sub.id)}/rotate`, { method: 'POST' });
      setLinkSub({ ...result.subscription, url: result.url, http_url: result.http_url });
      Message.success('订阅链接已重置');
      await refresh();
    } catch (error) { handleError(error); throw error; }
  }
  function confirmRotateSub(sub: Subscription) {
    Modal.confirm({
      title: `重置「${sub.name}」的订阅链接？`,
      content: '原链接将立即失效，重置后生成的新链接仍可随时查看与复制。客户端已下载的节点凭据不受影响。',
      okText: '确认重置',
      onOk: () => rotateSub(sub),
    });
  }
  async function previewSub(sub: Subscription) {
    try {
      const yaml = await request<string>(token, `/subscriptions/${encodeURIComponent(sub.id)}/preview`);
      setPreview({ name: sub.name, yaml });
    } catch (error) { handleError(error); }
  }
  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value); Message.success('已复制'); }
    catch { Message.warning('当前浏览器无法自动复制，请手动选择文本复制。'); }
  }
  function downloadYAML() {
    if (!preview) return;
    const url = URL.createObjectURL(new Blob([preview.yaml], { type: 'application/yaml;charset=utf-8' }));
    const link = document.createElement('a'); link.href = url; link.download = 'proxy-config.yaml'; link.click(); URL.revokeObjectURL(url);
  }

  const nodeSelectOptions = useMemo(
    () => nodes.map((node) => ({
      value: node.id,
      label: `${node.name} (${node.online ? '在线' : '离线'}${!node.enabled ? ' · 已停用' : ''})`,
    })),
    [nodes],
  );

  const filteredNodes = useMemo(() => {
    const query = search.trim().toLowerCase();
    return nodes.filter((node) => [node.name, node.id, ...node.endpoints.map((ep) => ep.server)].some((value) => value.toLowerCase().includes(query)));
  }, [nodes, search]);
  const nodeColumns: TableColumnProps<ProxyNode>[] = [
    { title: '节点', dataIndex: 'name', width: 220, render: (_, node) => <div className="node-identity"><div className="node-icon"><IconDesktop /></div><div><strong>{node.name}</strong><div className="muted mono">{node.os || '—'} / {node.arch || '—'}</div></div></div> },
    { title: 'Agent 心跳', width: 125, render: (_, node) => <Badge status={node.online ? 'success' : 'default'} text={node.online ? '在线' : '离线'} /> },
    { title: '代理核心', width: 160, render: (_, node) => node.cores?.length ? <Space direction="vertical" size={5}>{node.cores.map((core) => <Tooltip key={core.name} content={core.error || core.version || '进程状态，不代表外部协议连通性'}><span className="core-state"><span className={`tiny-dot ${core.running ? 'green' : 'red'}`} />{core.name}<span className="muted">{core.running ? '运行中' : '未运行'}</span></span></Tooltip>)}</Space> : <span className="muted">等待上报</span> },
    { title: '协议入口', width: 260, render: (_, node) => <Space direction="vertical" size={5}>{node.endpoints.map((ep) => <div className="endpoint" key={ep.id}><Tag size="small" color={ep.type === 'hysteria2' ? 'arcoblue' : 'gray'}>{protocolLabel(ep.type)}</Tag><span className="mono">{endpoint(ep.server, ep.port)}</span></div>)}</Space> },
    { title: '最后心跳', width: 170, render: (_, node) => <span className="muted">{formatTime(node.last_seen)}</span> },
    { title: '订阅可用', width: 100, render: (_, node) => <Tag bordered={false} color={node.enabled ? 'green' : 'gray'}>{node.enabled ? '已启用' : '已停用'}</Tag> },
    { title: '操作', width: 135, fixed: 'right', render: (_, node) => <Space size={4}><Button type="text" size="small" onClick={() => openNode(node)}>编辑</Button><Button type="text" status="danger" size="small" onClick={() => Modal.confirm({ title: `删除节点「${node.name}」？`, content: '该节点将从订阅中移除，Agent 身份将被撤销。重新接入需要创建新的节点身份。', okText: '删除节点', okButtonProps: { status: 'danger' }, onOk: () => removeNode(node) })}>删除</Button></Space> },
  ];
  const subColumns: TableColumnProps<Subscription>[] = [
    { title: '订阅名称', dataIndex: 'name', width: 200, render: (_, sub) => <div className="node-identity"><div className="node-icon"><IconLink /></div><div><strong>{sub.name}</strong><div className="muted">动态节点与分流配置</div></div></div> },
    { title: '通用节点 (🌍 代理)', width: 230, render: (_, sub) => <Space wrap size={4}>{sub.node_ids.length ? sub.node_ids.map((id) => { const node = nodes.find((item) => item.id === id); return <Tag key={id} color={node?.enabled ? 'gray' : 'orange'}>{node?.name || '节点已移除'}{node && !node.enabled ? ' · 已停用' : ''}</Tag>; }) : <span className="muted">暂无节点 · DIRECT 配置</span>}</Space> },
    {
      title: '专属纯净分流 (自动测速 · 禁直连)',
      width: 280,
      render: (_, sub) => {
        const routes = normalizeRoutes(sub.routes);
        const anyCustom = siteRouteMeta.some((item) => routes[item.key].custom);
        if (!anyCustom) {
          return <span className="muted">同通用节点 · 自动最快协议 + REJECT</span>;
        }
        return <Space wrap size={4}>
          {siteRouteMeta.map((item) => {
            const rg = routes[item.key];
            if (!rg.custom) {
              return <Tag key={item.key} size="small" color="gray">{item.badge}: 同通用</Tag>;
            }
            const activeNames = rg.node_ids
              .map((id) => nodes.find((n) => n.id === id))
              .filter((n): n is ProxyNode => Boolean(n && n.enabled))
              .map((n) => n.name);
            if (!activeNames.length) {
              return <Tag key={item.key} size="small" color="orange">{item.badge}: 已阻断 (REJECT)</Tag>;
            }
            return <Tag key={item.key} size="small" color={item.color}>{item.badge}: {activeNames.join('、')}</Tag>;
          })}
        </Space>;
      },
    },
    { title: '协议范围', width: 160, render: (_, sub) => <span className="muted">{sub.protocols.length ? sub.protocols.map(protocolLabel).join(' / ') : '全部协议'}</span> },
    { title: '状态', width: 85, render: (_, sub) => <Switch aria-label={`${sub.enabled ? '停用' : '启用'}订阅 ${sub.name}`} checked={sub.enabled} loading={pendingSubIDs.includes(sub.id)} disabled={pendingSubIDs.includes(sub.id)} size="small" onChange={() => void toggleSub(sub)} /> },
    { title: '最后更新', width: 165, render: (_, sub) => <span className="muted">{formatTime(sub.updated_at)}</span> },
    { title: '操作', width: 300, fixed: 'right', render: (_, sub) => <Space size={1}><Button type="text" size="small" onClick={() => setLinkSub(sub)}>查看链接</Button><Button type="text" size="small" onClick={() => openSub(sub)}>编辑分流</Button><Button type="text" size="small" onClick={() => void previewSub(sub)}>预览</Button><Button type="text" size="small" onClick={() => confirmRotateSub(sub)}>重置链接</Button><Button type="text" status="danger" size="small" onClick={() => Modal.confirm({ title: `删除订阅「${sub.name}」？`, content: '订阅链接将失效，Clash Verge 将无法继续从这个链接更新。', okText: '删除订阅', okButtonProps: { status: 'danger' }, onOk: () => removeSub(sub) })}>删除</Button></Space> },
  ];

  if (!token) return <Login onLogin={login} />;
  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><div className="brand-mark"><IconApps /></div><div>Proxy Console<small>节点与订阅管理</small></div></div>
      <div className="nav-label">WORKSPACE</div>
      <nav aria-label="主导航"><button className={page === 'nodes' ? 'active' : ''} onClick={() => setPage('nodes')}><IconDesktop /><span>代理节点</span><small>{nodes.length}</small></button><button className={page === 'subscriptions' ? 'active' : ''} onClick={() => setPage('subscriptions')}><IconLink /><span>订阅管理</span><small>{subscriptions.length}</small></button></nav>
      <div className="sidebar-bottom"><div className="side-status"><span className="tiny-dot green" /> SQLite · 轻量运行</div><p>管理服务只负责节点和配置，<br />代理流量由节点直接处理。</p><Button type="text" icon={<IconPoweroff />} onClick={logout}>退出登录</Button></div>
    </aside>
    <div className="workspace">
      <header className="topbar"><span>工作空间 <span className="breadcrumb-slash">/</span> <strong>{page === 'nodes' ? '代理节点' : '订阅管理'}</strong></span><div className="admin-pill"><span className="avatar">A</span> 管理员</div></header>
      <main>
        <div className="page-heading"><div><div className="eyebrow">{page === 'nodes' ? 'NODE INVENTORY' : 'SUBSCRIPTION PROFILES'}</div><h1>{page === 'nodes' ? '代理节点' : '订阅管理'}</h1><p>{page === 'nodes' ? '集中查看节点状态，选择需要分发到客户端的代理服务器。' : '为通用访问与 Claude / ChatGPT / Google 分配专属纯净节点，同一订阅链接动态生效。'}</p></div><Button type="primary" size="large" icon={<IconPlus />} onClick={() => openSub('new')}>创建订阅</Button></div>
        <div className="stats-grid">
          {[{ title: '全部节点', value: nodes.length, note: '已接入管理服务', icon: <IconDesktop /> }, { title: '心跳在线', value: nodes.filter((node) => node.online).length, note: '以 Agent 上报为准', icon: <IconCheckCircle /> }, { title: '协议入口', value: nodes.reduce((sum, node) => sum + node.endpoints.length, 0), note: '一个节点支持多个入口', icon: <IconApps /> }, { title: '有效订阅', value: subscriptions.filter((sub) => sub.enabled).length, note: 'Clash Verge 可定期更新', icon: <IconLink /> }].map((stat) => <Card bordered={false} className="stat-card" key={stat.title}><div className="stat-top"><span>{stat.title}</span><span className="stat-icon">{stat.icon}</span></div><div className="stat-number">{stat.value.toString().padStart(2, '0')}</div><div className="muted">{stat.note}</div></Card>)}
        </div>
        {loadError && <Alert className="load-error" type="error" title={loadError} content="暂时无法刷新数据，下面可能是上次成功获取的状态。" />}
        <Card bordered={false} className="content-card">
          <div className="table-toolbar"><div><h2>{page === 'nodes' ? '节点列表' : '订阅列表'}<Tag bordered={false}>{page === 'nodes' ? nodes.length : subscriptions.length}</Tag></h2><p>{page === 'nodes' ? '心跳在线与代理核心状态独立展示。' : '编辑节点与专属分流后无需更换链接，在 Clash Verge 中更新订阅即可。'}</p></div><Space wrap>{page === 'nodes' && <Input aria-label="搜索节点" allowClear prefix={<IconSearch />} placeholder="搜索节点名称或地址" value={search} onChange={setSearch} style={{ width: 220 }} />}<Tooltip content="每 15 秒自动刷新"><Button aria-label="刷新数据" icon={<IconRefresh />} loading={loading} onClick={() => void refresh(true)}>刷新</Button></Tooltip></Space></div>
          {page === 'nodes' && selectedIDs.length > 0 && <div className="selection-bar"><span>已选择 <strong>{selectedIDs.length}</strong> 个节点</span><Space><Button size="small" type="text" onClick={() => setSelectedIDs([])}>取消选择</Button><Button size="small" type="primary" onClick={() => openSub('new')}>用所选节点创建订阅</Button></Space></div>}
          <Spin loading={loading} style={{ display: 'block' }}>
            {page === 'nodes' ? <Table rowKey="id" columns={nodeColumns} data={filteredNodes} scroll={{ x: 1170 }} pagination={{ pageSize: 10, sizeCanChange: true, showTotal: true }} rowSelection={{ selectedRowKeys: selectedIDs, onChange: (keys) => setSelectedIDs(keys.map(String)) }} noDataElement={<Empty description={search ? '没有匹配的节点，试试其他关键词。' : '暂无节点。安装 proxy-server 并注册后，节点会出现在这里。'} />} /> : <Table rowKey="id" columns={subColumns} data={subscriptions} scroll={{ x: 1375 }} pagination={{ pageSize: 10, showTotal: true }} noDataElement={<Empty description="还没有订阅。创建订阅，选择要分发到 Clash Verge 的节点。" />} />}
          </Spin>
          <div className="table-footer"><span><span className="tiny-dot green" /> 自动刷新 · 每 15 秒</span><span>{updatedAt ? `最后刷新 ${updatedAt}` : '正在连接管理服务'}</span></div>
        </Card>
        <div className="context-note"><IconCheckCircle /><span>{page === 'nodes' ? '心跳在线表示 Agent 可以连接管理端；真实代理可用性仍取决于客户端网络。临时离线不会自动移除订阅中的节点。' : '专属分组使用 url-test 自动选择最低延迟协议，且组内节点全部失效时自动 REJECT 禁止直连；全局模式（GLOBAL）默认跟随 🌍 代理节点。'}</span></div>
      </main>
      <footer className="workspace-footer"><span>Proxy Console</span><span>轻量部署，清晰管理。</span></footer>
    </div>

    <Modal title="编辑节点" visible={Boolean(nodeEditor)} onCancel={() => setNodeEditor(null)} onOk={() => void saveNode()} confirmLoading={saving} unmountOnExit>
      <label className="field-label" htmlFor="node-name">节点名称</label><Input id="node-name" value={nodeName} maxLength={100} onChange={setNodeName} />
      <div className="switch-field"><div><strong>允许进入订阅</strong><p>停用后，所有订阅（含专属分流组）将在下次刷新时排除此节点。</p></div><Switch checked={nodeEnabled} onChange={setNodeEnabled} /></div>
      <Alert type="info" content="这里只控制订阅分发，不会停止节点上的代理核心。" />
    </Modal>
    <Modal title={subEditor === 'new' ? '创建订阅' : '编辑订阅与专属分流'} visible={Boolean(subEditor)} onCancel={() => setSubEditor(null)} onOk={() => void saveSub()} confirmLoading={saving} style={{ width: 650 }} unmountOnExit>
      <label className="field-label" htmlFor="subscription-name">订阅名称</label><Input id="subscription-name" placeholder="例如：日常主力 / 团队共享" value={subName} maxLength={100} onChange={setSubName} />
      <label className="field-label field-spaced">通用代理节点 · 🌍 代理 <span className="muted">({subNodeIDs.length})</span></label>
      <div className="node-picker">{nodes.length ? <Checkbox.Group value={subNodeIDs} onChange={(values) => setSubNodeIDs(values.map(String))}>{nodes.map((node) => <div className="node-option" key={node.id}><Checkbox value={node.id}>{node.name}</Checkbox><Space size={6}><Badge status={node.online ? 'success' : 'default'} text={node.online ? '在线' : '离线'} />{!node.enabled && <Tag size="small">已停用</Tag>}</Space></div>)}</Checkbox.Group> : <Empty description="暂无节点，可先保存空订阅，节点接入后再添加。" />}</div>
      <p className="helper">用于常规海外网站，且 Clash 切换到「全局」模式时默认复用此组同款节点（不再误走直连）。</p>

      <label className="field-label field-spaced">协议范围</label>
      <Select aria-label="协议范围" mode="multiple" placeholder="留空表示全部协议（节点内自动测速切换最快协议）" allowClear options={protocolOptions} value={subProtocols} onChange={setSubProtocols} style={{ width: '100%' }} />

      <label className="field-label field-spaced">专属纯净分流（纯自动测速 · 全挂自动 REJECT 禁止直连）</label>
      <div className="route-box">
        {siteRouteMeta.map((item) => {
          const rg = subRoutes[item.key];
          return <div className="route-row" key={item.key}>
            <div className="route-header">
              <div>
                <strong>{item.label}</strong>
                <div className="muted">{item.desc}</div>
              </div>
              <Radio.Group
                type="button"
                size="mini"
                value={rg.custom ? 'custom' : 'default'}
                onChange={(val) => setRouteCustom(item.key, val === 'custom')}
                options={[
                  { label: '同通用节点', value: 'default' },
                  { label: '指定专属节点', value: 'custom' },
                ]}
              />
            </div>
            {rg.custom && (
              <div className="route-select">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="选择 1 台或多台纯净节点（留空则直接 REJECT 阻断该组访问）"
                  options={nodeSelectOptions}
                  value={rg.node_ids}
                  onChange={(vals) => setRouteNodes(item.key, vals.map(String))}
                  style={{ width: '100%' }}
                />
              </div>
            )}
          </div>;
        })}
      </div>
      <p className="helper">每组独立对所选节点的全部协议每 30 秒自动测速选最快线路；若所选节点全部失效或被停用/删除，自动 REJECT 阻断，绝不泄漏到其他脏 IP 节点或直连。</p>

      <div className="switch-field"><div><strong>启用订阅</strong><p>停用后，链接暂时无法获取配置。</p></div><Switch checked={subEnabled} onChange={setSubEnabled} /></div>
      {subEditor !== 'new' && <Alert type="success" content="修改通用或专属节点不会改变订阅链接。在 Clash Verge 中更新该订阅即可同步最新配置。" />}
    </Modal>
    <Modal
      title={linkSub ? `订阅链接 · ${linkSub.name}` : '订阅链接'}
      visible={Boolean(linkSub)}
      onCancel={() => setLinkSub(null)}
      footer={linkSub ? <Space><Button onClick={() => confirmRotateSub(linkSub)}>重置链接</Button><Button type="primary" onClick={() => setLinkSub(null)}>关闭</Button></Space> : null}
      style={{ width: 640 }}
      unmountOnExit
    >
      <Alert type="info" content="订阅链接可随时在此重复查看与复制；如链接泄露，点击下方「重置链接」即可立即作废旧链接并生成新链接。" />
      {linkSub?.http_url ? (
        <>
          <div className="url-section">
            <div className="url-section-header">
              <strong>HTTP 订阅链接（纯 IP 无域名环境推荐 · Clash 直接一键导入）</strong>
              <Tag size="small" color="green">免证书校验</Tag>
            </div>
            <div className="url-box">
              <Input.TextArea aria-label="HTTP 订阅链接" readOnly autoSize={{ minRows: 2, maxRows: 4 }} value={linkSub.http_url} />
              <Button type="primary" icon={<IconCopy />} onClick={() => void copy(linkSub.http_url || '')}>复制 HTTP 链接</Button>
            </div>
          </div>
          {linkSub.url && (
            <div className="url-section">
              <div className="url-section-header">
                <strong>HTTPS 订阅链接（已绑定域名公共证书，或客户端开启允许无效证书时使用）</strong>
                <Tag size="small" color="arcoblue">TLS</Tag>
              </div>
              <div className="url-box">
                <Input.TextArea aria-label="HTTPS 订阅链接" readOnly autoSize={{ minRows: 2, maxRows: 4 }} value={linkSub.url} />
                <Button icon={<IconCopy />} onClick={() => void copy(linkSub.url || '')}>复制 HTTPS 链接</Button>
              </div>
            </div>
          )}
        </>
      ) : linkSub?.url ? (
        <div className="url-section">
          <div className="url-box">
            <Input.TextArea aria-label="订阅链接" readOnly autoSize={{ minRows: 2, maxRows: 4 }} value={linkSub.url} />
            <Button type="primary" icon={<IconCopy />} onClick={() => void copy(linkSub.url || '')}>复制链接</Button>
          </div>
        </div>
      ) : (
        <div className="url-section">
          <Alert type="warning" content="该订阅创建于旧版本，尚未保存可查看链接。点击下方「重置链接」生成一次后即可永久反复查看。" />
        </div>
      )}
      <div className="subscription-steps"><strong>在 Clash Verge 中使用</strong><ol><li>打开「订阅」，粘贴上方链接直接导入（纯 IP 环境优先复制 HTTP 订阅链接）。</li><li>此后在控制台增删节点或调整 Claude / ChatGPT / Google 专属分流，无需更换链接，在 Clash 中点刷新即可同步。</li></ol></div>
    </Modal>
    <Modal title={`配置预览 · ${preview?.name || ''}`} visible={Boolean(preview)} onCancel={() => setPreview(null)} footer={<Space><Button onClick={downloadYAML}>下载 YAML</Button><Button icon={<IconCopy />} onClick={() => void copy(preview?.yaml || '')}>复制配置</Button><Button type="primary" onClick={() => setPreview(null)}>关闭</Button></Space>} style={{ width: 'min(820px, 94vw)' }} unmountOnExit>
      <Alert type="warning" content="配置包含真实代理凭据，请勿公开分享或粘贴到日志。" /><pre className="yaml-preview">{preview?.yaml}</pre>
    </Modal>
  </div>;
}
