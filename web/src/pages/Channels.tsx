import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { useFormDialog, FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { Badge } from '../components/ui/badge'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { toast } from '../components/ui/sonner'
import { confirmDelete, promptDialog } from '../components/ui/confirm'
import { SimpleSelect } from '../components/ui/select'
import { Switch } from '../components/ui/switch'
import { Tip } from '../components/ui/tooltip'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '../components/ui/collapsible'
import { AccountsDialog } from '../components/channels/AccountsDialog'
import { ChannelPricesDialog } from '../components/channels/ChannelPricesDialog'
import { ChannelModelsDialog } from '../components/channels/ChannelModelsDialog'
import { useBreakerStatus, useBreakerReset } from '../lib/breaker'
import { BreakerBadge } from '../components/breaker/BreakerBadge'
import { useAuth } from '../lib/auth'
import { Plus, Trash2, KeyRound, Pencil, DollarSign, Boxes, BarChart3 } from 'lucide-react'
import type { Channel } from '../lib/types'
import { useI18n } from '../lib/i18n'

// 渠道编辑表单初始值（新建/编辑/保存后重置三处共用，避免漂移）
const emptyForm = { type: 'openai', name: '', base_url: '', api_key: '', timeout: 60, remark: '', priority: 0, weight: 1, max_retries: 0, rpm_limit: 0, rate_multiplier: 1, allow_convert: false, strip_thinking: false, disable_stream_usage: false, error_passthrough: false, auth_style: 'auto', custom_headers: '' }

// 自定义请求头：JSON 字符串 ↔ 键值对行数组互转
type HdrRow = { k: string; v: string }
function parseHeaders(s: string): HdrRow[] {
  if (!s) return []
  try { return Object.entries(JSON.parse(s) as Record<string, string>).map(([k, v]) => ({ k, v: String(v) })) } catch { return [] }
}
function rowsToJSON(rows: HdrRow[]): string {
  const o: Record<string, string> = {}
  rows.forEach(r => { if (r.k.trim()) o[r.k.trim()] = r.v })
  return Object.keys(o).length ? JSON.stringify(o) : ''
}

export function Channels() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const navigate = useNavigate()
  // 页面级列表数据走 TanStack Query（挂载即加载,缓存 key = 接口路径）
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/channels'], queryFn: () => api.get<Channel[]>('/admin/channels') })
  const { data: allModels = [] } = useQuery({ queryKey: ['/admin/channel-models'], queryFn: () => api.get<{ channel_id: number; model: string }[]>('/admin/channel-models') })
  const { data: allPrices = [] } = useQuery({ queryKey: ['/admin/prices'], queryFn: () => api.get<{ model: string; channel_id: number }[]>('/admin/prices') })
  // 熔断状态（10s 轮询）：渠道级映射 + 各渠道账号熔断计数
  const { data: brk } = useBreakerStatus()
  const chBrkMap = useMemo(() => new Map((brk?.channels ?? []).map(c => [c.id, c])), [brk])
  const acctBrkCount = useMemo(() => {
    const m: Record<number, number> = {}
    ;(brk?.accounts ?? []).forEach(a => { m[a.channel_id] = (m[a.channel_id] || 0) + 1 })
    return m
  }, [brk])
  const { role } = useAuth()
  const resetBreaker = useBreakerReset()
  const fd = useFormDialog(emptyForm)
  // 已有渠道的 Base URL 仅超管可改（后端同样拦截）：改地址会把该渠道账号的上游密钥发往新地址
  const baseUrlLocked = fd.editId !== 0 && role !== 'super'
  const [hdrRows, setHdrRows] = useState<HdrRow[]>([])
  // 三个下钻面板（密钥池/覆盖价/模型）互斥：谁打开谁关掉别人，子组件不感知彼此
  const [expandedId, setExpandedId] = useState(0)
  const [pricePanelId, setPricePanelId] = useState(0)
  const [modelPanelId, setModelPanelId] = useState(0)
  // 由查询结果派生：全局已定价模型集合、各渠道模型计数/未定价计数
  const pricedModels = useMemo(() => new Set(allPrices.filter(p => p.channel_id === 0).map(p => p.model)), [allPrices])
  const { modelCounts, unpricedCounts } = useMemo(() => {
    const counts: Record<number, number> = {}
    const unpriced: Record<number, number> = {}
    allModels.forEach(m => {
      counts[m.channel_id] = (counts[m.channel_id] || 0) + 1
      if (!pricedModels.has(m.model)) unpriced[m.channel_id] = (unpriced[m.channel_id] || 0) + 1
    })
    return { modelCounts: counts, unpricedCounts: unpriced }
  }, [allModels, pricedModels])
  // 失效页面级列表查询触发重取（mutation 后刷新与手动刷新共用）
  const load = () => Promise.all([
    qc.invalidateQueries({ queryKey: ['/admin/channels'] }),
    qc.invalidateQueries({ queryKey: ['/admin/channel-models'] }),
    qc.invalidateQueries({ queryKey: ['/admin/prices'] }),
  ])
  function openNew() {
    fd.openNew()
    setHdrRows([])
  }
  function openEdit(r: Channel) {
    fd.openEdit(r.id, { type: r.type || 'openai', name: r.name, base_url: r.base_url, api_key: '', timeout: r.timeout, remark: r.remark || '', priority: r.priority, weight: r.weight, max_retries: r.max_retries, rpm_limit: r.rpm_limit, rate_multiplier: r.rate_multiplier ?? 1, allow_convert: r.allow_convert || false, strip_thinking: r.strip_thinking || false, disable_stream_usage: r.disable_stream_usage || false, error_passthrough: r.error_passthrough || false, auth_style: r.auth_style || 'auto', custom_headers: r.custom_headers || '' })
    setHdrRows(parseHeaders(r.custom_headers || ''))
  }
  // 渠道保存（新建/编辑合一）：payload 由当前表单态在调用时构建，editId 决定走新建还是编辑端点
  const saveM = useMutation({
    mutationFn: () => {
      const payload = {
        type: fd.form.type, name: fd.form.name, base_url: fd.form.base_url, timeout: Number(fd.form.timeout),
        priority: Number(fd.form.priority), weight: Number(fd.form.weight),
        max_retries: Number(fd.form.max_retries), rpm_limit: Number(fd.form.rpm_limit),
        rate_multiplier: Number(fd.form.rate_multiplier), remark: fd.form.remark,
        allow_convert: fd.form.allow_convert,
        strip_thinking: fd.form.strip_thinking,
        disable_stream_usage: fd.form.disable_stream_usage,
        auth_style: fd.form.type === 'anthropic' ? fd.form.auth_style : 'auto',
        custom_headers: rowsToJSON(hdrRows),
      }
      return fd.editId ? api.post(`/admin/channels/${fd.editId}`, payload) : api.post('/admin/channels', { ...payload, api_key: fd.form.api_key })
    },
    onSuccess: () => { fd.close(); setHdrRows([]); load() },
  })
  const multiplierM = useMutation({
    mutationFn: (v: { id: number; rate_multiplier: number }) => api.post(`/admin/channels/${v.id}`, { rate_multiplier: v.rate_multiplier }),
    onSuccess: () => load(),
  })
  // 启停切换：Update 端点 status 为指针字段，只发 { status } 即局部更新
  const toggleM = useMutation({
    mutationFn: (r: Channel) => api.post(`/admin/channels/${r.id}`, { status: r.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })
  async function editMultiplier(r: Channel) {
    const v = await promptDialog({ title: t('渠道倍率'), description: t('实际成本 = 有效价 × 倍率'), defaultValue: String(r.rate_multiplier ?? 1) })
    if (v === null) return
    const m = Number(v)
    if (!(m > 0)) { toast.error(t('倍率需为正数')); return }
    multiplierM.mutate({ id: r.id, rate_multiplier: m })
  }
  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/channels/${id}`),
    onSuccess: (_data, id) => { if (expandedId === id) setExpandedId(0); load() },
  })
  async function del(id: number) {
    if (await confirmDelete(t('确认删除该渠道？'))) delM.mutate(id)
  }
  // 下钻面板开关（互斥）
  function toggleAccounts(channelId: number) {
    if (expandedId === channelId) { setExpandedId(0); return }
    setExpandedId(channelId); setPricePanelId(0); setModelPanelId(0)
  }
  function togglePrices(channelId: number) {
    if (pricePanelId === channelId) { setPricePanelId(0); return }
    setPricePanelId(channelId); setExpandedId(0); setModelPanelId(0)
  }
  function toggleModels(channelId: number) {
    if (modelPanelId === channelId) { setModelPanelId(0); return }
    setModelPanelId(channelId); setExpandedId(0); setPricePanelId(0)
  }

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('渠道管理')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={load} />
        <Button onClick={openNew}><Plus size={16} />{t('新增渠道')}</Button>
      </div>
    </div>
    <Collapsible className="text-xs text-[var(--muted-foreground)] border border-[var(--border)] rounded-md p-3">
      <CollapsibleTrigger className="font-semibold">{t('请求完整链路（鉴权 → 治理 → 路由 → 粘连 → 派发）')}</CollapsibleTrigger>
      <CollapsibleContent className="mt-2 space-y-1.5 leading-relaxed">
        <p className="font-semibold">一、鉴权与令牌治理（派发前）</p>
        <p className="pl-3">1. API Key 鉴权（Bearer / x-api-key）→ 识别令牌 → 归属应用</p>
        <p className="pl-3">2. <b>令牌模型映射</b>：如配了 gpt-4→gpt-4o,先改写请求模型名</p>
        <p className="pl-3">3. <b>令牌模型白名单</b>：改写后的模型不在白名单 → 403</p>
        <p className="pl-3">4. <b>令牌日成本上限</b>：当日累计超限 → 402</p>
        <p className="pl-3">5. <b>应用配额</b>：总额度用完 → 402</p>
        <p className="pl-3">6. <b>价格校验</b>：目标模型未配价格 → 400</p>
        <p className="font-semibold mt-2">二、路由选择</p>
        <p className="pl-3">· 用改写后的模型名查路由表（直连模型 / 别名路由）→ 候选渠道列表</p>
        <p className="pl-3">· <b>协议过滤</b>：按渠道协议路由：/v1/chat/completions → OpenAI 协议渠道；/v1/messages → Anthropic 协议渠道（同协议透传）；开了协议转换的异协议渠道作兜底候选</p>
        <p className="pl-3">· <b>应用渠道授权</b>：按应用的 allowed_channels 过滤（空=不限）→ 无权限 → 403</p>
        <p className="pl-3">· 候选按<b>优先级分层</b>（高优先先试）→ 同层按<b>权重加权随机</b></p>
        <p className="font-semibold mt-2">三、会话粘连（账号选择）</p>
        <p className="pl-3">· 同一会话自动路由到同一账号（利用上游 prompt cache 省成本,5 分钟滑动窗口）</p>
        <p className="pl-3">· Anthropic：从 <code>metadata.user_id</code> 提取 session_id（Claude Code 自动传）</p>
        <p className="pl-3">· Responses：从 <code>previous_response_id</code> 提取（Codex CLI 自动传）</p>
        <p className="pl-3">· Chat：用 clientIP + UserAgent + 首条消息内容的 FNV hash 自动生成</p>
        <p className="pl-3">· <b>逃逸</b>：粘连账号不健康（熔断/停用）时自动解绑,重选新账号</p>
        <p className="font-semibold mt-2">四、派发与重试</p>
        <p className="pl-3">· 选中渠道 → 密钥池选账号 → 经代理池出站（如绑代理组）→ 发送到上游</p>
        <p className="pl-3">· 上游失败（5xx/429）→ 渠道内换账号重试（max_retries）→ 用尽则降级下一渠道</p>
        <p className="pl-3">· 出站 body 的 model 自动改写为候选真实模型名（别名/映射后的目标）</p>
      </CollapsibleContent>
    </Collapsible>
    <Table head={<><Th>ID</Th><Th>{t('名称')}</Th><Th>{t('协议')}</Th><Th>{t('模型数')}</Th><Th>{t('超时')}</Th><Th>{t('优先级/权重')}</Th><Th>{t('倍率')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td><Td>{r.name}</Td>
        <Td><div className="flex gap-1">
          <Badge status={r.type === 'anthropic' ? 'warn' : 'ok'}>{r.type === 'anthropic' ? 'Anthropic' : 'OpenAI'}</Badge>
          {r.allow_convert && <Badge status="info">{t('转换')}</Badge>}{r.error_passthrough && <Badge status="warn">{t('透传错误')}</Badge>}</div></Td>
        {/* 模型数为 0 = 该渠道不会被任何请求命中，标红并直达模型配置面板 */}
        <Td className="tnum">{modelCounts[r.id] ? <>
          {modelCounts[r.id]}
          {unpricedCounts[r.id] ? (
            <Tip content={t('点击去价格管理页，自动从 models.dev 匹配这些模型的官方价')}>
              <button className="ml-1 text-xs text-[var(--destructive)] hover:underline" onClick={() => navigate('/prices?sync=1')}>({unpricedCounts[r.id]}未定价)</button>
            </Tip>
          ) : null}
        </> : (
          <Tip content={t('该渠道未声明任何模型，不会进入路由候选（等同不可用）；点击去添加')}>
            <button className="font-medium text-[var(--destructive)] hover:underline" onClick={() => toggleModels(r.id)}>0 未配置模型</button>
          </Tip>
        )}</Td>
        <Td className="tnum">{r.timeout}s</Td>
        <Td className="tnum">{r.priority}/{r.weight}</Td>
        <Td className="tnum">
          <button className="hover:underline" onClick={() => editMultiplier(r)}>×{r.rate_multiplier ?? 1}</button>
        </Td>
        <Td><div className="flex items-center gap-2">
          <StatusPill value={r.status} />
          <BreakerBadge state={chBrkMap.get(r.id)?.state} openUntil={chBrkMap.get(r.id)?.open_until} canReset={role !== 'viewer'} onReset={() => resetBreaker('channel', r.id)} />
          {acctBrkCount[r.id] ? (
            <Tip content={t('该渠道有账号处于熔断/半开，打开密钥池查看并重置')}>
              <span className="text-xs text-[var(--down)] cursor-default">⚡账号{acctBrkCount[r.id]}</span>
            </Tip>
          ) : null}
        </div></Td>
        <Td className="flex gap-1">
          <Tip content={t('密钥池')}><Button variant="ghost" onClick={() => toggleAccounts(r.id)}><KeyRound size={15} />{t('密钥池')}</Button></Tip>
          <Tip content={t('渠道价')}><Button variant="ghost" onClick={() => togglePrices(r.id)}><DollarSign size={15} />{t('定价')}</Button></Tip>
          <Tip content={t('渠道模型')}><Button variant="ghost" onClick={() => toggleModels(r.id)}><Boxes size={15} />{t('模型')}</Button></Tip>
          <Tip content={t('用量分析')}><Button variant="ghost" onClick={() => navigate(`/channels/${r.id}/usage`)}><BarChart3 size={15} />{t('分析')}</Button></Tip>
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          <Tip content={t('编辑')}><Button variant="ghost" onClick={() => openEdit(r)}><Pencil size={15} /></Button></Tip>
          <Button variant="ghost" onClick={() => del(r.id)}><Trash2 size={15} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无渠道')}</Td></tr>}
    </Table>

    {/* 下钻子域弹窗：密钥池 / 覆盖价 / 模型（各自自持状态与数据拉取） */}
    <AccountsDialog channelId={expandedId} onClose={() => setExpandedId(0)} />
    <ChannelPricesDialog channelId={pricePanelId} onClose={() => setPricePanelId(0)} />
    <ChannelModelsDialog channelId={modelPanelId} onClose={() => setModelPanelId(0)} />

    <FormDialog open={fd.open} onClose={fd.close} title={fd.editId ? t('编辑渠道') : t('新增渠道')}
      onSubmit={() => saveM.mutate()}
      submitDisabled={!fd.form.name} submitting={saveM.isPending}>
      <Field label={t('渠道名')}><Input value={fd.form.name} onChange={e => fd.setForm({ ...fd.form, name: e.target.value })} /></Field>
      <Field label={t('协议')}>
        <SimpleSelect className="w-full" value={fd.form.type} onChange={v => fd.setForm({ ...fd.form, type: v })}
          options={[
            { value: 'openai', label: t('OpenAI（/v1/chat/completions 等）') },
            { value: 'anthropic', label: 'Anthropic（/v1/messages）' },
          ]} />
        <p className="mt-1 text-xs text-[var(--muted-foreground)]">一渠道一协议；上游同时提供两种协议端点时，各建一条渠道</p>
      </Field>
      <Field label={t('协议转换')}>
        <label className="flex items-center gap-1.5 text-sm">
          <Switch checked={fd.form.allow_convert} onCheckedChange={v => fd.setForm({ ...fd.form, allow_convert: v })} />
          允许跨协议转换（chat↔messages）
        </label>
        <p className="mt-1 text-xs text-[var(--muted-foreground)]">开启后，异协议入站请求可经协议转换由本渠道兜底；转换候选始终排在同协议透传渠道之后</p>
      </Field>
      <Field label={t('错误透传')}>
        <label className="flex items-center gap-1.5 text-sm">
          <Switch checked={fd.form.error_passthrough} onCheckedChange={v => fd.setForm({ ...fd.form, error_passthrough: v })} />
          全候选失败时透传最后一个上游错误
        </label>
        <p className="mt-1 text-xs text-[var(--muted-foreground)]">开启后用上游状态码与脱敏文案代替通用 502/503 摘要；直接打到上游的非重试错误本来就脱敏透传，此开关只影响聚合兜底</p>
      </Field>
      <Field label={t('thinking 剥离')}>
        <label className="flex items-center gap-1.5 text-sm">
          <Switch checked={fd.form.strip_thinking} onCheckedChange={v => fd.setForm({ ...fd.form, strip_thinking: v })} />
          出站剥离 thinking / reasoning_effort
        </label>
        <p className="mt-1 text-xs text-[var(--muted-foreground)]">上游不支持扩展思考时开启：透传出站前删除 thinking/reasoning_effort，避免上游 400</p>
      </Field>
      {fd.form.type === 'openai' && (
        <Field label={t('流式 usage 注入')}>
          <label className="flex items-center gap-1.5 text-sm">
            <Switch checked={!fd.form.disable_stream_usage} onCheckedChange={v => fd.setForm({ ...fd.form, disable_stream_usage: !v })} />
            注入 stream_options.include_usage（流式补计费）
          </label>
          <p className="mt-1 text-xs text-[var(--muted-foreground)]">默认开启：流式请求出站注入 include_usage，让上游回真实 usage 避免漏计费；个别对 stream_options 报 400 的上游可关闭</p>
        </Field>
      )}
      {fd.form.type === 'anthropic' && (
        <Field label={t('Anthropic 鉴权头')}>
          <SimpleSelect className="w-full" value={fd.form.auth_style} onChange={v => fd.setForm({ ...fd.form, auth_style: v })}
            options={[
              { value: 'auto', label: t('auto（官方域名发 x-api-key，中转站双发）') },
              { value: 'x-api-key', label: t('仅 x-api-key') },
              { value: 'bearer', label: t('仅 Authorization: Bearer') },
              { value: 'both', label: t('两者都发（Bearer + x-api-key）') },
            ]} />
          <p className="mt-1 text-xs text-[var(--muted-foreground)]">上游只认某一种时按需固定；不确定选 auto</p>
        </Field>
      )}
      <Field label={t('自定义请求头')}>
        <div className="space-y-2">
          {hdrRows.map((r, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input className="flex-1" placeholder={t('Header 名（如 HTTP-Referer）')} value={r.k}
                onChange={e => { const rows = [...hdrRows]; rows[i] = { ...r, k: e.target.value }; setHdrRows(rows) }} />
              <Input className="flex-1" placeholder={t('值')} value={r.v}
                onChange={e => { const rows = [...hdrRows]; rows[i] = { ...r, v: e.target.value }; setHdrRows(rows) }} />
              <Button type="button" variant="ghost" onClick={() => setHdrRows(hdrRows.filter((_, j) => j !== i))}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
            </div>
          ))}
          <Button type="button" variant="outline" onClick={() => setHdrRows([...hdrRows, { k: '', v: '' }])}><Plus size={14} />加一行</Button>
          <p className="text-xs text-[var(--muted-foreground)]">合并进出站请求头；鉴权头（Authorization/x-api-key）等受保护名会被网关忽略</p>
        </div>
      </Field>
      <Field label="Base URL">
        <Input value={fd.form.base_url} onChange={e => fd.setForm({ ...fd.form, base_url: e.target.value })} placeholder="https://api.openai.com/v1"
          disabled={baseUrlLocked} />
        {baseUrlLocked && (
          <span className="mt-1 block text-xs text-[var(--muted-foreground)]">仅超级管理员可修改已有渠道的 Base URL</span>
        )}
      </Field>
      {fd.editId === 0 && (
        <Field label={t('上游 API Key')}><Input type="password" value={fd.form.api_key} onChange={e => fd.setForm({ ...fd.form, api_key: e.target.value })} /></Field>
      )}
      <Field label={t('超时(秒)')}><Input type="number" value={fd.form.timeout} onChange={e => fd.setForm({ ...fd.form, timeout: Number(e.target.value) })} /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('优先级')}><Input type="number" value={fd.form.priority} onChange={e => fd.setForm({ ...fd.form, priority: Number(e.target.value) })} /></Field>
        <Field label={t('权重')}><Input type="number" value={fd.form.weight} onChange={e => fd.setForm({ ...fd.form, weight: Number(e.target.value) })} /></Field>
        <Field label={t('最大重试')}><Input type="number" value={fd.form.max_retries} onChange={e => fd.setForm({ ...fd.form, max_retries: Number(e.target.value) })} /></Field>
        <Field label={t('渠道RPM(0不限)')}><Input type="number" value={fd.form.rpm_limit} onChange={e => fd.setForm({ ...fd.form, rpm_limit: Number(e.target.value) })} /></Field>
        <Field label={t('倍率(默认1)')}><Input type="number" value={fd.form.rate_multiplier} onChange={e => fd.setForm({ ...fd.form, rate_multiplier: Number(e.target.value) })} /></Field>
      </div>
    </FormDialog>
  </div>
}
