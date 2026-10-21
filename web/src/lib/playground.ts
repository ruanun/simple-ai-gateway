// Playground 双协议支撑库：协议原生请求体构造 + SSE 流式解析（P11）。
// openai -> /v1/chat/completions；anthropic -> /v1/messages。凭证为令牌明文。

export type Protocol = 'openai' | 'anthropic'

export type ToolCall = { id: string; name: string; args: string } // args 为 JSON 字符串

export type Turn =
  | { role: 'user'; text: string }
  | { role: 'assistant'; text: string; toolCalls: ToolCall[] }
  | { role: 'tool'; callId: string; name: string; result: string }

export type Params = { system: string; temperature: string; maxTokens: string; topP: string }

// 诊断信息 = RequestLog 关键列（GET /admin/request-logs?request_id= 回查）
export type Diag = {
  channel_id: number; account_id: number; model: string
  protocol: string; upstream_protocol: string
  latency_ms: number; first_token_ms: number
  prompt_tokens: number; completion_tokens: number
  cost_micros: number; status: number
}

// tool_use 参数安全解析：流截断可能产生非法 JSON，回退空对象
function safeParseArgs(s: string): unknown {
  try { return JSON.parse(s || '{}') } catch { return {} }
}

// 构造协议原生请求体；未填写的参数不下发。
// anthropic 的 max_tokens 必填，未填默认 4096（UI 有注明）。
// 注意 anthropic 消息必须 user/assistant 交替：连续 tool 轮合并为单条 user(tool_result[]) 消息。
export function buildBody(proto: Protocol, model: string, turns: Turn[], p: Params, toolsJSON: string): Record<string, unknown> {
  const body: Record<string, unknown> = { model, stream: true }
  if (p.temperature !== '') body.temperature = Number(p.temperature)
  if (p.topP !== '') body.top_p = Number(p.topP)
  if (toolsJSON.trim() !== '') body.tools = JSON.parse(toolsJSON)

  if (proto === 'openai') {
    if (p.maxTokens !== '') body.max_tokens = Number(p.maxTokens)
    const msgs: unknown[] = []
    if (p.system.trim() !== '') msgs.push({ role: 'system', content: p.system })
    for (const t of turns) {
      if (t.role === 'user') {
        msgs.push({ role: 'user', content: t.text })
      } else if (t.role === 'assistant') {
        const m: Record<string, unknown> = { role: 'assistant', content: t.text || null }
        if (t.toolCalls.length) {
          m.tool_calls = t.toolCalls.map(c => ({ id: c.id, type: 'function', function: { name: c.name, arguments: c.args } }))
        }
        msgs.push(m)
      } else {
        msgs.push({ role: 'tool', tool_call_id: t.callId, content: t.result })
      }
    }
    body.messages = msgs
  } else {
    body.max_tokens = p.maxTokens !== '' ? Number(p.maxTokens) : 4096
    if (p.system.trim() !== '') body.system = p.system
    const msgs: unknown[] = []
    for (let i = 0; i < turns.length; i++) {
      const t = turns[i]
      if (t.role === 'assistant') {
        const blocks: unknown[] = []
        if (t.text) blocks.push({ type: 'text', text: t.text })
        for (const c of t.toolCalls) {
          blocks.push({ type: 'tool_use', id: c.id, name: c.name, input: safeParseArgs(c.args) })
        }
        msgs.push({ role: 'assistant', content: blocks })
      } else {
        // 连续的 user/tool 轮合并为单条 user 消息（anthropic 要求 user/assistant 严格交替；
        // 错误轮被过滤后相邻的 user 轮也靠这里合并）。tool_result 块天然先于后续文本块。
        const blocks: unknown[] = []
        while (i < turns.length && turns[i].role !== 'assistant') {
          const tt = turns[i]
          if (tt.role === 'user') blocks.push({ type: 'text', text: tt.text })
          else if (tt.role === 'tool') blocks.push({ type: 'tool_result', tool_use_id: tt.callId, content: tt.result })
          i++
        }
        i--
        msgs.push({ role: 'user', content: blocks })
      }
    }
    body.messages = msgs
  }
  return body
}

export type StreamResult = {
  text: string; toolCalls: ToolCall[]
  requestId: string; rawResponse: string
  errorBody?: string
}

// 发起流式请求并逐行解析 SSE；onText 收到累计全文用于增量渲染。
// 非 2xx 时不抛异常，把响应体放进 errorBody 由页面渲染错误气泡。
export async function streamChat(
  proto: Protocol, key: string, body: Record<string, unknown>,
  onText: (full: string) => void,
): Promise<StreamResult> {
  const path = proto === 'openai' ? '/v1/chat/completions' : '/v1/messages'
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (proto === 'openai') headers['Authorization'] = `Bearer ${key}`
  else { headers['x-api-key'] = key; headers['anthropic-version'] = '2023-06-01' }

  const res = await fetch(path, { method: 'POST', headers, body: JSON.stringify(body) })
  const requestId = res.headers.get('X-SAIG-Request-Id') || ''
  if (!res.ok || !res.body) {
    const errText = await res.text()
    return { text: '', toolCalls: [], requestId, rawResponse: errText, errorBody: errText || `HTTP ${res.status}` }
  }

  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = '', raw = '', text = ''
  let errorBody: string | undefined // 流中错误帧（上游中断时转换器/上游发出）→ 填 errorBody 供页面渲染错误气泡
  const oaTools: ToolCall[] = []            // openai 按 delta.tool_calls[].index 聚合
  const anthTools: Record<number, ToolCall> = {} // anthropic 按 content_block index 聚合
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      const chunk = dec.decode(value, { stream: true })
      raw += chunk
      buf += chunk
      let nl: number
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim()
        buf = buf.slice(nl + 1)
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trim()
        if (data === '' || data === '[DONE]') continue
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        let ev: any
        try { ev = JSON.parse(data) } catch { continue }
        if (proto === 'openai') {
          // 流中错误帧检出（openai 风格：顶层 error 字段）
          if (ev.error) { errorBody = typeof ev.error === 'string' ? ev.error : JSON.stringify(ev.error); continue }
          const d = ev.choices?.[0]?.delta
          if (!d) continue
          if (typeof d.content === 'string' && d.content !== '') { text += d.content; onText(text) }
          for (const c of d.tool_calls ?? []) {
            const i = c.index ?? 0
            if (!oaTools[i]) oaTools[i] = { id: '', name: '', args: '' }
            if (c.id) oaTools[i].id = c.id
            if (c.function?.name) oaTools[i].name = c.function.name
            if (c.function?.arguments) oaTools[i].args += c.function.arguments
          }
        } else {
          // 流中错误帧检出（anthropic 风格：type === 'error' 事件）
          if (ev.type === 'error') { errorBody = JSON.stringify(ev.error ?? ev); continue }
          if (ev.type === 'content_block_start' && ev.content_block?.type === 'tool_use') {
            anthTools[ev.index] = { id: ev.content_block.id, name: ev.content_block.name, args: '' }
          } else if (ev.type === 'content_block_delta') {
            if (ev.delta?.type === 'text_delta') { text += ev.delta.text; onText(text) }
            else if (ev.delta?.type === 'input_json_delta' && anthTools[ev.index]) {
              anthTools[ev.index].args += ev.delta.partial_json
            }
          }
        }
      }
    }
  } catch (e) {
    // 流中途断开：保留 requestId 与已收内容正常返回，诊断条仍可按请求 ID 回查
    errorBody = `流中断: ${e instanceof Error ? e.message : String(e)}`
  }
  const toolCalls = proto === 'openai'
    ? oaTools.filter(Boolean)
    : Object.keys(anthTools).map(k => anthTools[Number(k)])
  return { text, toolCalls, requestId, rawResponse: raw, errorBody }
}
