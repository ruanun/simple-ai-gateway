import { useMemo, useState } from 'react'

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// prettyJSON 尝试格式化 JSON，失败原样返回。
function prettyJSON(s: string): string {
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
}

// highlightJSON 轻量着色：转义后给字符串/键/数字/布尔/null 包 span。
function highlightJSON(s: string): string {
  const esc = escapeHtml(s)
  return esc.replace(
    /("(\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(\.\d+)?([eE][+-]?\d+)?/g,
    (m, str, _q, colon, kw) => {
      if (str) {
        const cls = colon ? 'text-[var(--primary)]' : 'text-emerald-600 dark:text-emerald-400'
        return `<span class="${cls}">${str}</span>${colon || ''}`
      }
      if (kw) return `<span class="text-amber-600 dark:text-amber-400">${m}</span>`
      return `<span class="text-sky-600 dark:text-sky-400">${m}</span>`
    },
  )
}

function isSSE(s: string): boolean {
  return /(^|\n)(event:|data:)/.test(s.slice(0, 2000))
}

// parseSSE 把 SSE 文本切成事件 {event,data}。
function parseSSE(s: string): { event: string; data: string }[] {
  return s.split(/\n\n+/).map((block) => {
    let event = 'message'; const dataLines: string[] = []
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
    }
    return { event, data: dataLines.join('\n') }
  }).filter((e) => e.data !== '' || e.event !== 'message')
}

const BIG = 200 * 1024

export function CodeView({ content, filename = 'body.txt' }: { content: string; filename?: string }) {
  const [expandBig, setExpandBig] = useState(false)
  const sse = useMemo(() => isSSE(content), [content])
  const tooBig = content.length > BIG

  function copy() { navigator.clipboard.writeText(content) }
  function download() {
    const blob = new Blob([content], { type: 'text/plain' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob); a.download = filename; a.click()
    URL.revokeObjectURL(a.href)
  }

  const toolbar = (
    <div className="mb-1 flex items-center justify-end gap-2 text-xs">
      <span className="mr-auto text-[var(--muted-foreground)]">{sse ? 'SSE 事件流' : 'JSON'} · {(content.length / 1024).toFixed(1)} KB</span>
      <button className="text-[var(--primary)] hover:underline" onClick={copy}>复制</button>
      <button className="text-[var(--primary)] hover:underline" onClick={download}>下载</button>
    </div>
  )

  if (!content) return <div className="text-sm text-[var(--muted-foreground)]">（空）</div>
  if (tooBig && !expandBig) {
    return <div>{toolbar}
      <div className="rounded-md bg-[var(--muted)] p-4 text-sm text-[var(--muted-foreground)]">
        内容较大（{(content.length / 1024).toFixed(0)} KB），已折叠。
        <button className="ml-2 text-[var(--primary)] hover:underline" onClick={() => setExpandBig(true)}>仍要展开</button>
      </div>
    </div>
  }

  if (sse) {
    const events = parseSSE(content)
    return <div>{toolbar}
      <div className="max-h-[60vh] space-y-1 overflow-auto rounded-md bg-[var(--muted)] p-2 font-mono text-xs">
        {events.map((e, i) => (
          <details key={i} className="rounded border border-[var(--border)] bg-[var(--card)]">
            <summary className="cursor-pointer select-none px-2 py-1">
              <span className="text-[var(--muted-foreground)]">#{i + 1}</span>{' '}
              <span className="text-[var(--primary)]">{e.event}</span>{' '}
              <span className="text-[var(--muted-foreground)]">{e.data.slice(0, 100)}</span>
            </summary>
            <pre className="overflow-auto px-3 py-2" dangerouslySetInnerHTML={{ __html: highlightJSON(prettyJSON(e.data)) }} />
          </details>
        ))}
        {events.length === 0 && <div className="p-2 text-[var(--muted-foreground)]">无可解析事件</div>}
      </div>
    </div>
  }

  return <div>{toolbar}
    <pre
      className="max-h-[60vh] overflow-auto rounded-md bg-[var(--muted)] p-3 font-mono text-xs"
      dangerouslySetInnerHTML={{ __html: highlightJSON(prettyJSON(content)) }}
    />
  </div>
}
