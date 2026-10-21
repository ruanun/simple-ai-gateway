import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Textarea } from '../components/ui/textarea'
import { Field } from '../components/ui/field'
import { SimpleSelect } from '../components/ui/select'
import { toast } from '../components/ui/sonner'
import { confirmDialog } from '../components/ui/confirm'
import { Combobox } from '../components/ui/combobox'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '../components/ui/collapsible'
import { Send, Trash2, ChevronDown, ChevronRight, RefreshCw } from 'lucide-react'
import { buildBody, streamChat, type Protocol, type Turn, type ToolCall, type Params, type Diag } from '../lib/playground'
import { usd } from '../lib/format'
import { useI18n } from '../lib/i18n'

type KeyOption = { id: number; name: string; prefix: string; key?: string }
type ChannelOption = { id: number; name: string }

// 页面展示轮 = 协议轮 + UI 附加字段（错误的 assistant 轮不进上下文）
type DisplayTurn = Turn & {
  error?: string
  requestId?: string
  rawRequest?: string
  rawResponse?: string
  diag?: Diag | null // undefined=未回查 null=未就绪
}

const emptyParams: Params = { system: '', temperature: '', maxTokens: '', topP: '' }

export function Playground() {
  const { t } = useI18n()
  // —— 配置面板状态 ——
  // 令牌列表：缓存存原始响应行（与令牌管理页共享 '/admin/api-keys' 缓存键），select 派生内嵌 key 对象
  const { data: keys = [] } = useQuery({
    queryKey: ['/admin/api-keys'],
    queryFn: () => api.get<{ key: KeyOption }[]>('/admin/api-keys'),
    select: rows => rows.map(r => r.key),
  })
  const { data: channels = [] } = useQuery({ queryKey: ['/admin/channels'], queryFn: () => api.get<ChannelOption[]>('/admin/channels') })
  const { data: cms = [] } = useQuery({ queryKey: ['/admin/channel-models'], queryFn: () => api.get<{ id: number; model: string }[]>('/admin/channel-models') })
  const { data: als = [] } = useQuery({ queryKey: ['/admin/aliases'], queryFn: () => api.get<{ id: number; alias: string }[]>('/admin/aliases') })
  // 模型建议 = 渠道模型 + 别名（与令牌白名单同源）
  const models = useMemo(() => {
    const set = new Set<string>()
    cms.forEach(m => set.add(m.model))
    als.forEach(a => set.add(a.alias))
    return [...set].sort()
  }, [cms, als])
  const [keyId, setKeyId] = useState(0)
  const [proto, setProto] = useState<Protocol>('openai')
  const [model, setModel] = useState('')
  const [params, setParams] = useState<Params>(emptyParams)
  const [toolsJSON, setToolsJSON] = useState('')

  // —— 对话状态（仅内存，刷新即清空）——
  const [turns, setTurns] = useState<DisplayTurn[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => { bottomRef.current?.scrollIntoView({ behavior: 'smooth' }) }, [turns])

  const selKey = keys.find(k => k.id === keyId)

  // 切协议：两协议消息结构不互转，清空对话（confirmDialog 异步，故改为 async）
  async function switchProto(p: Protocol) {
    if (busy) return // 请求进行中不允许切协议，避免 busy 被已放弃的旧请求锁死
    if (p === proto) return
    if (turns.length > 0 && !(await confirmDialog({ title: t('切换协议将清空当前对话，继续？'), confirmText: t('继续') }))) return
    setProto(p)
    setTurns([])
    setToolsJSON('') // 两协议 tools 格式不同，一并清空
  }

  // 核心执行：把 history 发出去并流式填充最后追加的 assistant 轮
  async function run(history: DisplayTurn[]) {
    const idx = history.length
    setTurns([...history, { role: 'assistant', text: '', toolCalls: [] }])
    setBusy(true)
    try {
      // 错误的 assistant 轮不进上下文
      const ctx = history.filter(t => !(t.role === 'assistant' && t.error)) as Turn[]
      const body = buildBody(proto, model.trim(), ctx, params, toolsJSON)
      const rawRequest = JSON.stringify(body, null, 2)
      const r = await streamChat(proto, selKey!.key!, body,
        full => setTurns(ts => ts.map((t, j) => j === idx ? { ...t, text: full } : t)))
      setTurns(ts => ts.map((t, j) => j === idx ? {
        ...t, text: r.text, toolCalls: r.toolCalls, error: r.errorBody,
        requestId: r.requestId, rawRequest, rawResponse: r.rawResponse,
      } : t))
      if (r.requestId) void pollDiag(r.requestId)
    } catch (e) {
      // 流中途断开等网络层异常：保留已渲染内容并标注
      setTurns(ts => ts.map((t, j) => j === idx ? { ...t, error: `请求中断: ${e instanceof Error ? e.message : String(e)}` } : t))
    } finally {
      setBusy(false)
    }
  }

  // 日志异步落库，最多轮询 5 次（500ms 间隔）；按 requestId 定位轮次（数组下标在清空/重发后不稳定）。
  // 未就绪或查询异常时置 null，由气泡显示手动刷新。
  async function pollDiag(requestId: string) {
    const write = (diag: Diag | null) => setTurns(ts => ts.map(t =>
      t.role === 'assistant' && t.requestId === requestId ? { ...t, diag } : t))
    for (let i = 0; i < 5; i++) {
      await new Promise(r => setTimeout(r, 500))
      try {
        const res = await api.get<{ total: number; items: Diag[] }>(`/admin/request-logs?request_id=${requestId}&limit=1`)
        if (res.total > 0) { write(res.items[0]); return }
      } catch { break } // 401/网络异常：中止轮询走兜底提示
    }
    write(null)
  }

  const [toolResults, setToolResults] = useState<Record<string, string>>({})

  // 末轮 assistant 带 tool_use 且无错误时，等待手动回填结果
  const last = turns[turns.length - 1]
  const pendingTools: ToolCall[] = !busy && last?.role === 'assistant' && !last.error ? last.toolCalls : []

  // tools JSON 合法性守卫：非法时 toast 并拦截发送（send / submitToolResults 共用）
  function toolsJSONValid(): boolean {
    if (toolsJSON.trim() === '') return true
    try { JSON.parse(toolsJSON); return true } catch { toast.error(t('tools JSON 非法，请检查')); return false }
  }

  function send() {
    // 待回填工具结果时禁止自由发送，否则 dangling tool_use 会导致上游 400（守卫防 Enter 键路径绕过按钮禁用）
    if (busy || !selKey?.key || !model.trim() || !input.trim() || pendingTools.length > 0) return
    if (!toolsJSONValid()) return
    const history = [...turns, { role: 'user', text: input } as DisplayTurn]
    setInput('')
    void run(history)
  }

  // 待回填工具结果时禁止自由发送，否则 dangling tool_use 会导致上游 400
  const canSend = !busy && !!selKey?.key && model.trim() !== '' && input.trim() !== '' && pendingTools.length === 0

  // 把每个 tool_use 的结果作为 tool 轮追加，然后继续请求
  function submitToolResults() {
    if (busy || !selKey?.key || !model.trim()) return // 与 send 同级守卫（selKey 可能已被切换）
    if (!toolsJSONValid()) return
    if (pendingTools.some(c => (toolResults[c.id] ?? '') === '')) { toast.error(t('请填写所有工具结果')); return }
    const toolTurns: DisplayTurn[] = pendingTools.map(c => ({ role: 'tool', callId: c.id, name: c.name, result: toolResults[c.id] }))
    setToolResults({})
    void run([...turns, ...toolTurns])
  }

  return <div className="flex h-full gap-4">
    {/* —— 左侧配置面板 —— */}
    <aside className="w-72 shrink-0 space-y-3 overflow-y-auto rounded-lg border border-[var(--border)] bg-[var(--card)] p-4">
      <div className="text-sm font-semibold">{t('调试配置')}</div>
      <Field label={t('令牌')}>
        <SimpleSelect value={String(keyId)} onChange={v => setKeyId(Number(v))} className="w-full"
          options={[
            { value: '0', label: t('选择令牌…') },
            ...keys.map(k => ({ value: String(k.id), label: `${k.name}（${k.prefix}…）${k.key ? '' : t('·不可回查')}`, disabled: !k.key })),
          ]} />
        {selKey && !selKey.key && <div className="mt-1 text-xs text-[var(--destructive)]">{t('该令牌未存明文，无法用于调试，请重建令牌')}</div>}
      </Field>
      <Field label={t('入站协议')}>
        <div className="flex gap-1">
          {(['openai', 'anthropic'] as Protocol[]).map(p =>
            <button key={p} onClick={() => switchProto(p)}
              className={`flex-1 rounded-md px-2 py-1.5 text-xs cursor-pointer transition ${proto === p ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'border border-[var(--border)] hover:bg-[var(--muted)]'}`}>
              {p === 'openai' ? 'openai · chat' : 'anthropic · messages'}
            </button>)}
        </div>
      </Field>
      <Field label={t('模型')}>
        {/* Input 支持自由输入任意模型名；Combobox 仅作为从已知模型列表挑选的快捷入口，两者共写 model 状态 */}
        <div className="flex gap-1.5">
          <Input className="flex-1" value={model} onChange={e => setModel(e.target.value)} placeholder={t('模型名或别名')} />
          <Combobox className="w-24 shrink-0" value={model} onChange={setModel}
            options={models.map(m => ({ value: m }))}
            placeholder={t('列表选…')} searchPlaceholder="搜索模型…" emptyText="无匹配模型" />
        </div>
      </Field>
      <Field label="System Prompt">
        <Textarea rows={3}
          value={params.system} onChange={e => setParams({ ...params, system: e.target.value })} placeholder={t('留空不下发')} />
      </Field>
      <div className="grid grid-cols-3 gap-2">
        <Field label="temperature">
          <Input value={params.temperature} onChange={e => setParams({ ...params, temperature: e.target.value })} placeholder={t('不下发')} />
        </Field>
        <Field label="max_tokens">
          <Input value={params.maxTokens} onChange={e => setParams({ ...params, maxTokens: e.target.value })}
            placeholder={proto === 'anthropic' ? t('默认4096') : t('不下发')} />
        </Field>
        <Field label="top_p">
          <Input value={params.topP} onChange={e => setParams({ ...params, topP: e.target.value })} placeholder={t('不下发')} />
        </Field>
      </div>
      <Collapsible>
        <CollapsibleTrigger className="text-xs text-[var(--muted-foreground)]">{t('tools（协议原生 JSON，留空不下发）')}</CollapsibleTrigger>
        <CollapsibleContent><Textarea className="mt-1 font-mono text-xs" rows={6}
          value={toolsJSON} onChange={e => setToolsJSON(e.target.value)}
          placeholder={proto === 'openai' ? '[{"type":"function","function":{"name":"...","parameters":{...}}}]' : '[{"name":"...","input_schema":{...}}]'} /></CollapsibleContent>
      </Collapsible>
      <Button variant="outline" className="w-full" onClick={() => setTurns([])} disabled={busy || turns.length === 0}>
        <Trash2 size={14} className="mr-1" />清空对话
      </Button>
    </aside>

    {/* —— 右侧对话区 —— */}
    <section className="flex min-w-0 flex-1 flex-col rounded-lg border border-[var(--border)] bg-[var(--card)]">
      <div className="flex-1 space-y-3 overflow-y-auto p-4">
        {turns.length === 0 && <div className="flex h-full items-center justify-center text-sm text-[var(--muted-foreground)]">
          选择令牌与模型后发送消息，请求走真实 /v1 全链路（会产生计费与日志）
        </div>}
        {turns.map((t, i) => <TurnBubble key={i} turn={t} channels={channels}
          onRefreshDiag={() => t.requestId && pollDiag(t.requestId)} />)}
        {pendingTools.length > 0 && <div className="space-y-2 rounded-lg border border-[var(--border)] p-3">
          <div className="text-xs font-semibold">模型请求了 {pendingTools.length} 个工具，手动填写结果后继续（首版不真正执行工具）</div>
          {pendingTools.map(c => <div key={c.id}>
            <div className="mb-0.5 text-xs text-[var(--muted-foreground)]">{c.name} · {c.id}</div>
            <Textarea className="font-mono text-xs" rows={2}
              value={toolResults[c.id] ?? ''} onChange={e => setToolResults({ ...toolResults, [c.id]: e.target.value })}
              placeholder={t('工具执行结果（文本或 JSON）')} />
          </div>)}
          <Button onClick={submitToolResults}>{t('提交结果并继续')}</Button>
        </div>}
        <div ref={bottomRef} />
      </div>
      <div className="flex gap-2 border-t border-[var(--border)] p-3">
        <Textarea className="flex-1 resize-none px-3 py-2" rows={2}
          value={input} onChange={e => setInput(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}
          disabled={pendingTools.length > 0}
          placeholder={pendingTools.length > 0 ? t('请先在上方填写工具结果并提交') : t('输入消息，Enter 发送，Shift+Enter 换行')} />
        <Button onClick={send} disabled={!canSend}><Send size={15} className="mr-1" />{busy ? t('生成中…') : t('发送')}</Button>
      </div>
    </section>
  </div>
}

// 单轮气泡
function TurnBubble({ turn, channels, onRefreshDiag }: {
  turn: DisplayTurn; channels: ChannelOption[]; onRefreshDiag: () => void
}) {
  const { t } = useI18n()
  if (turn.role === 'user') {
    return <div className="flex justify-end">
      <div className="max-w-[80%] whitespace-pre-wrap rounded-lg bg-[var(--primary)] px-3 py-2 text-sm text-[var(--primary-foreground)]">{turn.text}</div>
    </div>
  }
  if (turn.role === 'tool') {
    return <div className="flex justify-end">
      <div className="max-w-[80%] rounded-lg border border-dashed border-[var(--border)] px-3 py-2 text-xs">
        <div className="mb-1 font-semibold text-[var(--muted-foreground)]">tool 结果 · {turn.name}</div>
        <pre className="whitespace-pre-wrap font-mono">{turn.result}</pre>
      </div>
    </div>
  }
  return <div className="flex justify-start">
    <div className="max-w-[85%] space-y-2">
      {turn.text !== '' && <div className="whitespace-pre-wrap rounded-lg bg-[var(--muted)] px-3 py-2 text-sm">{turn.text}</div>}
      {turn.toolCalls.map(c => <div key={c.id} className="rounded-lg border border-[var(--accent)] px-3 py-2 text-xs">
        <div className="mb-1 font-semibold">{t('工具调用')} · {c.name} <span className="font-normal text-[var(--muted-foreground)]">{c.id}</span></div>
        <pre className="max-h-40 overflow-auto whitespace-pre-wrap font-mono">{formatArgs(c.args)}</pre>
      </div>)}
      {turn.error && <div className="rounded-lg border border-[var(--destructive)] px-3 py-2 text-xs text-[var(--destructive)]">
        <div className="font-semibold">{t('请求失败')}</div>
        <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap font-mono">{turn.error}</pre>
      </div>}
      {turn.diag && <DiagBar diag={turn.diag} channels={channels} />}
      {turn.diag === null && <div className="flex items-center gap-1 text-xs text-[var(--muted-foreground)]">
        {t('日志未就绪')} <button className="cursor-pointer underline" onClick={onRefreshDiag}><RefreshCw size={11} className="inline" /> {t('刷新')}</button>
      </div>}
      {(turn.rawRequest || turn.rawResponse) && <RawDrawer rawRequest={turn.rawRequest} rawResponse={turn.rawResponse} />}
    </div>
  </div>
}

// 诊断条：渠道/账号/真实模型/协议/耗时/token/成本（来自 RequestLog 回查）
function DiagBar({ diag: d, channels }: { diag: Diag; channels: ChannelOption[] }) {
  const { t } = useI18n()
  const chName = channels.find(c => c.id === d.channel_id)?.name || `#${d.channel_id}`
  const items: [string, string][] = [
    [t('渠道'), chName],
    [t('账号'), `#${d.account_id}`],
    [t('模型'), d.model],
    [t('协议'), `${d.protocol}→${d.upstream_protocol}`],
    [t('耗时'), `${d.latency_ms}ms`],
    [t('首token'), d.first_token_ms > 0 ? `${d.first_token_ms}ms` : '-'],
    ['tokens', `${d.prompt_tokens}+${d.completion_tokens}`],
    [t('成本'), usd(d.cost_micros)],
  ]
  return <div className="flex flex-wrap gap-x-3 gap-y-1 rounded-md border border-[var(--border)] px-2 py-1 text-xs text-[var(--muted-foreground)]">
    {items.map(([k, v]) => <span key={k}><span className="opacity-70">{k}</span> <span className="tnum font-medium text-[var(--foreground)]">{v}</span></span>)}
  </div>
}

// 原始报文抽屉：实际发出的请求 JSON 与原始响应（SSE 帧原文）
function RawDrawer({ rawRequest, rawResponse }: { rawRequest?: string; rawResponse?: string }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  return <div className="text-xs">
    <button className="flex cursor-pointer items-center gap-0.5 text-[var(--muted-foreground)] hover:text-[var(--foreground)]" onClick={() => setOpen(!open)}>
      {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}原始报文
    </button>
    {open && <div className="mt-1 space-y-2">
      {rawRequest && <div>
        <div className="mb-0.5 text-[var(--muted-foreground)]">{t('请求')}</div>
        <pre className="max-h-60 overflow-auto rounded-md bg-[var(--muted)] p-2 font-mono">{rawRequest}</pre>
      </div>}
      {rawResponse && <div>
        <div className="mb-0.5 text-[var(--muted-foreground)]">{t('响应')}</div>
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md bg-[var(--muted)] p-2 font-mono">{rawResponse}</pre>
      </div>}
    </div>}
  </div>
}

// tool 参数美化：能 parse 就 pretty-print，不能就原样展示
function formatArgs(args: string): string {
  try { return JSON.stringify(JSON.parse(args || '{}'), null, 2) } catch { return args }
}
