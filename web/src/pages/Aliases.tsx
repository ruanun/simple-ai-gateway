import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { Card } from '../components/ui/card'
import { FormDialog } from '../components/ui/form-dialog'
import { Badge, Pill } from '../components/ui/badge'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { Combobox } from '../components/ui/combobox'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '../components/ui/collapsible'
import { confirmDelete } from '../components/ui/confirm'
import { Switch } from '../components/ui/switch'
import { Tip } from '../components/ui/tooltip'
import { Plus, Trash2, Pencil } from 'lucide-react'
import type { Channel, ModelAlias, AliasRoute, ChannelModel, ModelPrice } from '../lib/types'
import { useI18n } from '../lib/i18n'

export function Aliases() {
  const { t } = useI18n()
  const qc = useQueryClient()
  // 列表数据统一走 TanStack Query（挂载即拉取，同 key 跨页面共享缓存）
  const { data: aliases = [] } = useQuery({ queryKey: ['/admin/aliases'], queryFn: () => api.get<ModelAlias[]>('/admin/aliases') })
  const { data: channels = [] } = useQuery({ queryKey: ['/admin/channels'], queryFn: () => api.get<Channel[]>('/admin/channels') })
  // 选中态只存 id，sel 从 aliases 派生：列表刷新后自动同步、被删后自动清空（替代原 loadAliases 里的手动 find 重设）
  const [selId, setSelId] = useState(0)
  const sel = aliases.find(a => a.id === selId) ?? null
  // 选中别名的路由边：依赖选中 id 的查询，未选中不拉取
  const { data: routes = [] } = useQuery({
    queryKey: ['/admin/aliases/routes', selId],
    queryFn: () => api.get<AliasRoute[]>(`/admin/aliases/${selId}/routes`),
    enabled: selId > 0,
  })
  const [newAlias, setNewAlias] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [search, setSearch] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [edge, setEdge] = useState({ channel_id: 0, model: '', weight: 1, priority: 0 })
  // 「加路由边」弹窗内按渠道拉模型：弹窗打开且已选渠道才拉，选择渠道即随 queryKey 自动刷新
  const { data: channelModels = [] } = useQuery({
    queryKey: ['/admin/channel-models', { channel_id: edge.channel_id }],
    queryFn: () => api.get<Pick<ChannelModel, 'id' | 'model'>[]>(`/admin/channel-models?channel_id=${edge.channel_id}`),
    enabled: addOpen && edge.channel_id > 0,
  })
  // 边编辑（渠道/模型不可改，只调权重/优先级）
  const [editEdge, setEditEdge] = useState<AliasRoute | null>(null)
  // 别名编辑（说明/启停；别名名是对外契约不可改）
  const [editAlias, setEditAlias] = useState<ModelAlias | null>(null)
  // 价格集合：键 "channelID:model"（渠道覆盖价）与 "0:model"（全局价），从 /admin/prices 查询派生
  const { data: prices = [] } = useQuery({
    queryKey: ['/admin/prices'],
    queryFn: () => api.get<Pick<ModelPrice, 'channel_id' | 'model' | 'enabled'>[]>('/admin/prices'),
  })
  const priceKeys = useMemo(
    () => new Set(prices.filter(p => p.enabled !== false).map(p => `${p.channel_id}:${p.model}`)),
    [prices],
  )

  // 失效对应查询触发重拉（保留原函数名，mutation 后的调用与 RefreshButton 均不用改）
  const loadAliases = () => qc.invalidateQueries({ queryKey: ['/admin/aliases'] })
  const loadChannels = () => qc.invalidateQueries({ queryKey: ['/admin/channels'] })
  const loadPrices = () => qc.invalidateQueries({ queryKey: ['/admin/prices'] })
  const loadRoutes = (id: number) => qc.invalidateQueries({ queryKey: ['/admin/aliases/routes', id] })

  // 新建别名：原 catch 只是 toast 错误 → 删除交全局兜底（如 409「别名已存在」，文案更准）
  const createAliasM = useMutation({
    mutationFn: (payload: { alias: string; description: string }) => api.post<ModelAlias>('/admin/aliases', payload),
    onSuccess: (a) => { setNewAlias(''); setNewDesc(''); loadAliases(); setSelId(a.id) },
  })
  function createAlias() {
    if (!newAlias.trim()) return
    createAliasM.mutate({ alias: newAlias.trim(), description: newDesc })
  }
  const delAliasM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/aliases/${id}`),
    onSuccess: (_data, id) => { if (sel?.id === id) setSelId(0); loadAliases() },
  })
  async function delAlias(a: ModelAlias) {
    if (!(await confirmDelete(`删除别名 ${a.alias} 及其全部 ${a.route_count ?? 0} 条路由边？`))) return
    delAliasM.mutate(a.id)
  }
  // 编辑别名说明/启停：原 catch 只是 toast 错误 → 删除交全局兜底
  const saveAliasM = useMutation({
    mutationFn: (a: ModelAlias) => api.post(`/admin/aliases/${a.id}`, { description: a.description, status: a.status }),
    onSuccess: () => { setEditAlias(null); loadAliases() },
  })
  function saveAliasEdit() {
    if (!editAlias) return
    saveAliasM.mutate(editAlias)
  }
  // 别名一键启停：复用 Update 端点指针字段局部更新（只发 status，不影响说明），写后服务端热刷路由表
  const toggleAliasM = useMutation({
    mutationFn: (a: ModelAlias) => api.post(`/admin/aliases/${a.id}`, { status: a.status === 1 ? 0 : 1 }),
    onSuccess: () => loadAliases(),
  })
  // 加路由边：原 catch 只是 toast 错误 → 删除交全局兜底；onSuccess 用 variables 里的 alias_id，避免弹窗关闭期间 sel 漂移
  const addEdgeM = useMutation({
    mutationFn: (payload: { alias_id: number; channel_id: number; model: string; weight: number; priority: number }) =>
      api.post('/admin/alias-routes', payload),
    onSuccess: (_data, vars) => { setAddOpen(false); setEdge({ channel_id: 0, model: '', weight: 1, priority: 0 }); loadRoutes(vars.alias_id); loadAliases() },
  })
  function addEdge() {
    if (!sel || !edge.channel_id || !edge.model) return
    addEdgeM.mutate({ alias_id: sel.id, ...edge, channel_id: Number(edge.channel_id), weight: Number(edge.weight), priority: Number(edge.priority) })
  }
  // 编辑路由边权重/优先级：原 catch 只是 toast 错误 → 删除交全局兜底
  const saveEdgeM = useMutation({
    mutationFn: (edgeRow: AliasRoute) => api.post(`/admin/alias-routes/${edgeRow.id}`, { weight: Number(edgeRow.weight), priority: Number(edgeRow.priority) }),
    onSuccess: (_data, edgeRow) => { setEditEdge(null); loadRoutes(edgeRow.alias_id) },
  })
  function saveEdgeEdit() {
    if (!sel || !editEdge) return
    saveEdgeM.mutate(editEdge)
  }
  const delEdgeM = useMutation({
    mutationFn: (r: AliasRoute) => api.del(`/admin/alias-routes/${r.id}`),
    onSuccess: (_data, r) => { loadRoutes(r.alias_id); loadAliases() },
  })
  async function delEdge(r: AliasRoute) {
    if (!sel || !(await confirmDelete(`删除路由边 ${cname(r.channel_id)} / ${r.model}？`))) return
    delEdgeM.mutate(r)
  }

  const cfind = (id: number) => channels.find(c => c.id === id)
  const cname = (id: number) => cfind(id)?.name || id
  // 渠道协议（一渠道一协议），返回单元素数组以复用按协议分组逻辑
  const cprotos = (id: number) => [cfind(id)?.type || 'openai']
  // 计费按真名：全局价或该渠道覆盖价任一存在即视为已定价
  const isPriced = (channelID: number, m: string) => priceKeys.has(`0:${m}`) || priceKeys.has(`${channelID}:${m}`)
  // 层内对协议 t 的转换兜底候选：异协议但开了 allow_convert 的边（派发时恒排在透传候选之后）
  const convFallback = (inLayer: AliasRoute[], t: string) =>
    inLayer.filter(r => !cprotos(r.channel_id).includes(t) && cfind(r.channel_id)?.allow_convert)
  // 按 priority 降序分层
  const layers = Array.from(new Set(routes.map(r => r.priority))).sort((a, b) => b - a)
  // 检查是否涉及多个入站协议（需要分组展示，各协议候选集独立、权重占比分别计算）
  const hasMultiProto = new Set(routes.flatMap(r => cprotos(r.channel_id))).size > 1
  const protoLabel: Record<string, string> = { openai: 'OpenAI 入站候选', anthropic: 'Anthropic 入站候选' }
  const filteredAliases = aliases.filter(a => !search
    || a.alias.toLowerCase().includes(search.toLowerCase())
    || (a.description || '').toLowerCase().includes(search.toLowerCase()))

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('别名路由')}</h2>
      <RefreshButton onRefresh={() => Promise.all([loadAliases(), loadChannels(), loadPrices()])} />
    </div>
    <Collapsible className="rounded-md border border-[var(--border)] bg-[var(--card)] px-4 py-2 text-sm">
      <CollapsibleTrigger className="font-medium text-[var(--muted-foreground)]">{t('路由机制说明')}</CollapsibleTrigger>
      <CollapsibleContent className="mt-2 space-y-1 text-[var(--muted-foreground)]">
        <p>· <b>优先级 = 故障降级层级</b>：数值大的层先试，整层失败（熔断/上游错误用尽重试）才降级到下一层</p>
        <p>· <b>权重 = 同层负载均衡</b>：同一层内按权重加权随机分配流量，占比 ≈ 权重/层内权重和</p>
        <p>· <b>计费按命中的真实模型</b>：别名只是入口名，价格要配在路由边指向的真实模型上（⚠未定价的边会被 400 拒绝）</p>
        <p>· <b>协议过滤</b>：chat 入站只命中 OpenAI 协议渠道的边、messages 只命中 Anthropic 的；开了协议转换的异协议渠道作兜底</p>
        <p>· 两种用法：<b>{t('规范名')}</b>（1 条边，把 glm-5.2-20260505 映射为 glm-5.2）与<b>{t('智能路由')}</b>（多条边，smart/fast 按情况分发）</p>
      </CollapsibleContent>
    </Collapsible>
    <div className="grid grid-cols-[280px_1fr] gap-4">
      {/* 左：别名列表 */}
      <Card className="p-3 space-y-2">
        <div className="space-y-1">
          <div className="flex gap-2">
            <Input value={newAlias} onChange={e => setNewAlias(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') createAlias() }} placeholder={t('别名，如 smart')} />
            <Tip content={t('新建别名')}><Button onClick={createAlias} disabled={createAliasM.isPending} aria-label={t('新建别名')}><Plus size={16} /></Button></Tip>
          </div>
          <Input value={newDesc} onChange={e => setNewDesc(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') createAlias() }} placeholder={t('说明，如 聪明通用')} className="text-xs" />
        </div>
        {aliases.length > 3 && <Input value={search} onChange={e => setSearch(e.target.value)} placeholder={t('搜索别名/说明...')} className="text-xs" />}
        <div className="space-y-1">
          {/* 按边数自动分组：≥2边=智能路由，1边=规范名(1:1重命名)，0边=未配置 */}
          {[
            { label: t('智能路由'), items: filteredAliases.filter(a => (a.route_count ?? 0) >= 2) },
            { label: t('规范名'), items: filteredAliases.filter(a => (a.route_count ?? 0) === 1) },
            { label: t('未配置'), items: filteredAliases.filter(a => !(a.route_count ?? 0)) },
          ].filter(g => g.items.length > 0).map(g => (
            <div key={g.label}>
              <div className="px-2 pt-1.5 pb-0.5 text-xs font-medium text-[var(--muted-foreground)]">{g.label}（{g.items.length}）</div>
              {g.items.map(a => (
                <div key={a.id}
                  className={`flex items-center justify-between rounded-md px-3 py-2 text-sm cursor-pointer ${sel?.id === a.id ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'hover:bg-[var(--muted)]'} ${a.status !== 1 ? 'opacity-60' : ''}`}
                  onClick={() => setSelId(a.id)}>
                  <div className="min-w-0">
                    <span className="font-mono">{a.alias}</span>
                    <span className="ml-1.5 text-xs opacity-60">{a.route_count ?? 0} 边</span>
                    {a.status !== 1 && <span className="ml-1.5 rounded bg-[var(--muted)] px-1 py-0.5 text-xs text-[var(--muted-foreground)]">{t('停用')}</span>}
                    {a.description && <div className="truncate text-xs opacity-70">{a.description}</div>}
                  </div>
                  <Tip content={t('删除别名')}><button className="shrink-0 cursor-pointer opacity-70 hover:opacity-100" aria-label={t('删除别名')}
                    onClick={(e) => { e.stopPropagation(); delAlias(a) }}><Trash2 size={14} /></button></Tip>
                </div>
              ))}
            </div>
          ))}
          {aliases.length === 0 && <p className="px-2 text-xs text-[var(--muted-foreground)]">暂无别名</p>}
          {aliases.length > 0 && filteredAliases.length === 0 && <p className="px-2 text-xs text-[var(--muted-foreground)]">无匹配</p>}
        </div>
      </Card>

      {/* 右：分层 + 权重 */}
      <Card className="p-4">
        {!sel && <div className="space-y-2 text-sm text-[var(--muted-foreground)]">
          <p>从左侧选择一个别名以编辑其路由分层。</p>
          <p className="text-xs">优先级=故障降级层级（大者先试），权重=同层负载均衡；计费按路由边指向的真实模型。</p>
        </div>}
        {sel && <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <span className="text-sm text-[var(--muted-foreground)]">{t('别名 · 策略 优先级+权重')}</span>
              <div className="flex items-center gap-2">
                <span className="font-mono text-base font-semibold">{sel.alias}</span>
                {sel.status !== 1 && <Pill status="muted">{t('停用')}</Pill>}
                <Tip content={t('编辑说明/启停')}><button className="cursor-pointer text-[var(--muted-foreground)] hover:text-[var(--foreground)]" aria-label={t('编辑别名')}
                  onClick={() => setEditAlias({ ...sel })}><Pencil size={14} /></button></Tip>
                <Button variant="ghost" disabled={toggleAliasM.isPending} onClick={() => toggleAliasM.mutate(sel)}>{sel.status === 1 ? t('停用') : t('启用')}</Button>
              </div>
              {sel.description && <div className="text-xs text-[var(--muted-foreground)]">{sel.description}</div>}
            </div>
            <Button onClick={() => setAddOpen(true)} disabled={channels.length === 0}><Plus size={16} />{t('加路由边')}</Button>
          </div>
          {channels.length === 0 && <p className="text-sm text-[var(--warn)]">{t('请先创建渠道。')}</p>}
          {layers.length === 0 && <p className="text-sm text-[var(--muted-foreground)]">{t('暂无路由边，点右上角添加。')}</p>}
          {layers.map(p => {
            const inLayer = routes.filter(r => r.priority === p)
            const types = hasMultiProto
              ? [...new Set(inLayer.flatMap(r => cprotos(r.channel_id)))].sort()
              : [null]
            return <div key={p} className="rounded-md border border-[var(--border)] p-3">
              <div className="mb-2 text-xs font-medium text-[var(--muted-foreground)]">优先级 P{p}{p === layers[0] ? t('（主）') : t('（降级）')}</div>
              {types.map(proto => {
                const group = proto ? inLayer.filter(r => cprotos(r.channel_id).includes(proto)) : inLayer
                const sum = group.reduce((s, r) => s + (r.weight || 0), 0) || 1
                const conv = proto ? convFallback(inLayer, proto) : []
                return <div key={proto || 'all'} className={proto ? 'mb-3 last:mb-0' : ''}>
                  {proto && <div className="mb-1.5 text-xs text-[var(--muted-foreground)] font-medium">{t(protoLabel[proto] || proto)}</div>}
                  <div className="space-y-1">
                    {group.map(r => {
                      const pct = Math.round((r.weight / sum) * 100)
                      return (
                        <div key={r.id} className="flex items-center gap-3 text-sm">
                          <Badge status="ok">{cname(r.channel_id)}</Badge>
                          <span className="tnum text-[var(--muted-foreground)]">{r.model}
                            {!isPriced(r.channel_id, r.model) && <Tip content={t('目标真实模型未配价格，命中该边的请求会被拒绝(400)')}><span className="ml-1 text-xs text-[var(--destructive)]">⚠未定价</span></Tip>}
                          </span>
                          <span className="tnum">权重 {r.weight}</span>
                          <div className="h-1 w-16 shrink-0 overflow-hidden rounded bg-[var(--muted)]" aria-hidden>
                            <div className="h-1 rounded bg-[var(--primary)]" style={{ width: `${pct}%` }} />
                          </div>
                          <span className="tnum text-[var(--muted-foreground)]">≈{pct}%</span>
                          <div className="ml-auto flex items-center gap-1">
                            <Tip content={t('编辑权重/优先级')}><button className="cursor-pointer text-[var(--muted-foreground)] hover:text-[var(--foreground)]" aria-label={t('编辑路由边')}
                              onClick={() => setEditEdge({ ...r })}><Pencil size={14} /></button></Tip>
                            <Tip content={t('删除路由边')}><button className="cursor-pointer" aria-label={t('删除路由边')}
                              onClick={() => delEdge(r)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></button></Tip>
                          </div>
                        </div>
                      )
                    })}
                  </div>
                  {conv.length > 0 && (
                    <div className="mt-1.5 text-xs text-[var(--muted-foreground)]">
                      + {conv.length} 个转换兜底候选：{conv.map(r => `${cname(r.channel_id)}(${r.model})`).join('、')}（经 chat↔messages 转换，恒排在透传候选之后）
                    </div>
                  )}
                </div>
              })}
              {/* 单协议别名：另一协议入站无透传候选，提示是否有转换兜底 */}
              {!hasMultiProto && inLayer.length > 0 && (() => {
                const p = cprotos(inLayer[0].channel_id)[0]
                const other = p === 'openai' ? 'anthropic' : 'openai'
                const conv = convFallback(inLayer, other)
                if (conv.length === 0) return null
                return <div className="mt-1.5 text-xs text-[var(--muted-foreground)]">
                  {t(protoLabel[other])}：无透传候选，由 {conv.length} 个开转换的渠道兜底（{conv.map(r => `${cname(r.channel_id)}(${r.model})`).join('、')}）
                </div>
              })()}
            </div>
          })}
        </div>}
      </Card>
    </div>

    <FormDialog open={addOpen} onClose={() => setAddOpen(false)} title={t('添加路由边')}
      onSubmit={addEdge} submitDisabled={!edge.channel_id || !edge.model} submitting={addEdgeM.isPending} submitText="添加">
      <Field label={t('渠道')}>
        <SimpleSelect className="w-full" value={String(edge.channel_id)}
          onChange={v => setEdge({ ...edge, channel_id: Number(v), model: '' })}
          options={[
            { value: '0', label: t('请选择') },
            ...channels.map(c => ({ value: String(c.id), label: `${c.name}（${c.type === 'anthropic' ? 'Anthropic' : 'OpenAI'}）` })),
          ]} />
      </Field>
      <Field label={t('模型')}>
        <Combobox className="w-full" value={edge.model}
          onChange={v => setEdge({ ...edge, model: v })}
          placeholder={t('请选择模型')} searchPlaceholder="搜索模型…"
          options={channelModels.map(m => ({ value: m.model, label: `${m.model}${!isPriced(edge.channel_id, m.model) ? '（⚠未定价）' : ''}` }))} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('权重')}>
          <Input type="number" value={edge.weight} onChange={e => setEdge({ ...edge, weight: Number(e.target.value) })} />
          <p className="mt-1 text-xs text-[var(--muted-foreground)]">{t('同层负载均衡占比')}</p>
        </Field>
        <Field label={t('优先级')}>
          <Input type="number" value={edge.priority} onChange={e => setEdge({ ...edge, priority: Number(e.target.value) })} />
          <p className="mt-1 text-xs text-[var(--muted-foreground)]">{t('大者先试，层挂降级')}</p>
        </Field>
      </div>
    </FormDialog>

    {/* 编辑路由边（渠道/模型不可改，换目标请删边重建）。编辑对象是可空行而非 id，结构不典型于 useFormDialog 的新增/编辑三件套，
        故只借 FormDialog 统一壳与 footer（同 Apps.tsx 先例），children 仍需守卫 null */}
    <FormDialog open={editEdge !== null} onClose={() => setEditEdge(null)} title={t('编辑路由边')}
      onSubmit={saveEdgeEdit} submitDisabled={!editEdge} submitting={saveEdgeM.isPending}>
      {editEdge && <>
        <p className="text-sm text-[var(--muted-foreground)]">
          {cname(editEdge.channel_id)} / <span className="tnum">{editEdge.model}</span>（渠道与模型不可改，换目标请删边重建）
        </p>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('权重')}>
            <Input type="number" value={editEdge.weight} onChange={e => setEditEdge({ ...editEdge, weight: Number(e.target.value) })} />
            <p className="mt-1 text-xs text-[var(--muted-foreground)]">{t('同层负载均衡占比')}</p>
          </Field>
          <Field label={t('优先级')}>
            <Input type="number" value={editEdge.priority} onChange={e => setEditEdge({ ...editEdge, priority: Number(e.target.value) })} />
            <p className="mt-1 text-xs text-[var(--muted-foreground)]">{t('大者先试，层挂降级')}</p>
          </Field>
        </div>
      </>}
    </FormDialog>

    {/* 编辑别名（说明/启停；别名名是对外契约不可改），同上只借壳 */}
    <FormDialog open={editAlias !== null} onClose={() => setEditAlias(null)} title={`编辑别名 · ${editAlias?.alias ?? ''}`}
      onSubmit={saveAliasEdit} submitDisabled={!editAlias} submitting={saveAliasM.isPending}>
      {editAlias && <>
        <Field label={t('说明')}><Input value={editAlias.description || ''} onChange={e => setEditAlias({ ...editAlias, description: e.target.value })} /></Field>
        <Field label={t('状态')}>
          <label className="flex items-center gap-1.5 text-sm">
            <Switch checked={editAlias.status === 1} onCheckedChange={v => setEditAlias({ ...editAlias, status: v ? 1 : 0 })} />
            启用（停用后路由表不再包含该别名，请求按直调模型名解析）
          </label>
        </Field>
      </>}
    </FormDialog>
  </div>
}
