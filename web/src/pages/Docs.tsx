import { useState, useEffect } from 'react'
import { Card } from '../components/ui/card'
import { Check, Copy } from 'lucide-react'
import { useI18n } from '../lib/i18n'

const BASE = window.location.origin

function Code({ children, lang }: { children: string; lang?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(children)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="relative group">
      {lang && <div className="absolute top-2 left-3 text-[10px] text-[var(--muted-foreground)] select-none">{lang}</div>}
      <button
        onClick={copy}
        className="absolute top-2 right-2 p-1 rounded text-[var(--muted-foreground)] hover:bg-[var(--muted)] opacity-0 group-hover:opacity-100 transition cursor-pointer"
      >
        {copied ? <Check size={14} className="text-[var(--ok)]" /> : <Copy size={14} />}
      </button>
      <pre className="overflow-x-auto rounded-md bg-[var(--muted)] p-4 pt-8 text-xs leading-relaxed font-mono">
        <code>{children}</code>
      </pre>
    </div>
  )
}

function Section({ id, title, children }: { id: string; title: string; children: React.ReactNode }) {
  return (
    <section id={id} className="scroll-mt-6">
      <h2 className="text-lg font-semibold mb-3">{title}</h2>
      {children}
    </section>
  )
}

// 「字段/值」两列迷你配置表（客户端接入参数），数据驱动免复制表结构
function KVTable({ rows }: { rows: [string, string][] }) {
  return (
    <Card className="p-0 overflow-hidden">
      <table className="w-full text-sm">
        <tbody className="divide-y divide-[var(--border)]">
          {rows.map(([field, value]) => (
            <tr key={field} className="hover:bg-[var(--muted)] transition">
              <td className="px-4 py-2 text-xs font-medium w-32">{field}</td>
              <td className="px-4 py-2 text-xs font-mono">{value}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  )
}

// 大表统一表头行（端点表/错误码表共用样板）
function Thead({ cols }: { cols: string[] }) {
  return (
    <thead>
      <tr className="border-b border-[var(--border)] bg-[var(--muted)]">
        {cols.map(c => <th key={c} className="text-left px-4 py-2 text-xs font-medium text-[var(--muted-foreground)]">{c}</th>)}
      </tr>
    </thead>
  )
}

function TabGroup({ tabs }: { tabs: { label: string; content: string; lang?: string }[] }) {
  const [active, setActive] = useState(0)
  return (
    <div>
      <div className="flex gap-1 mb-2">
        {tabs.map((t, i) => (
          <button
            key={t.label}
            onClick={() => setActive(i)}
            className={`px-3 py-1 text-xs rounded-md cursor-pointer transition ${i === active ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'text-[var(--muted-foreground)] hover:bg-[var(--muted)]'}`}
          >{t.label}</button>
        ))}
      </div>
      <Code lang={tabs[active].lang}>{tabs[active].content}</Code>
    </div>
  )
}

const toc = [
  { id: 'quickstart', title: '快速开始' },
  { id: 'endpoints', title: 'API 端点' },
  { id: 'sdk', title: 'SDK 接入' },
  { id: 'stream', title: '流式响应' },
  { id: 'agents', title: '客户端/开发工具' },
  { id: 'aliases', title: '模型别名' },
  { id: 'billing', title: '计费说明' },
  { id: 'errors', title: '错误码' },
]

// portal=true 时为「使用方门户」版：令牌指引指向 /portal/keys，别名/价格的内部链接去掉
// （门户无这两页），仅保留概念说明。
export function Docs({ portal = false }: { portal?: boolean } = {}) {
  const { locale } = useI18n()
  const [activeId, setActiveId] = useState('')
  useEffect(() => {
    const els = toc.map(t => document.getElementById(t.id)).filter(Boolean) as HTMLElement[]
    if (!els.length) return
    const scrollRoot = document.querySelector('main')
    const ob = new IntersectionObserver(
      entries => {
        for (const e of entries) {
          if (e.isIntersecting) { setActiveId(e.target.id); break }
        }
      },
      { root: scrollRoot, rootMargin: '0px 0px -60% 0px', threshold: 0 },
    )
    els.forEach(el => ob.observe(el))
    return () => ob.disconnect()
  }, [])
  return (
    <div className="flex gap-8 max-w-6xl">
      <div className="flex-1 space-y-8 min-w-0">
        <div>
          <h1 className="text-2xl font-bold">接入文档</h1>
          <p className="mt-1 text-sm text-[var(--muted-foreground)]">本网关兼容 OpenAI 和 Anthropic 协议，可直接使用官方 SDK 或 CLI 工具接入。</p>
          {locale === 'en' && <p className="mt-2 text-xs text-[var(--warn)]">The API documentation is currently available in Chinese only.</p>}
        </div>

        {/* 快速开始 */}
        <Section id="quickstart" title="快速开始">
          <Card className="p-4 space-y-3">
            <div>
              <div className="text-xs text-[var(--muted-foreground)] mb-1">Base URL</div>
              <Code>{`${BASE}/v1`}</Code>
            </div>
            <div>
              <div className="text-xs text-[var(--muted-foreground)] mb-1">API Key</div>
              <p className="text-sm">在 <a href={portal ? '/portal/keys' : '/api-keys'} className="text-[var(--primary)] hover:underline">{portal ? '我的令牌' : '令牌管理'}</a> 页面创建，格式为 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">sk-saig-xxx</code>{portal ? '，创建后可随时复制。' : '（明文仅显示一次）。'}</p>
            </div>
            <div>
              <div className="text-xs text-[var(--muted-foreground)] mb-1">鉴权方式</div>
              <p className="text-sm">请求头 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">Authorization: Bearer sk-saig-xxx</code></p>
            </div>
          </Card>
        </Section>

        {/* API 端点 */}
        <Section id="endpoints" title="API 端点">
          <Card className="p-0 overflow-hidden">
            <table className="w-full text-sm">
              <Thead cols={['方法', '路径', '协议', '说明']} />
              <tbody className="divide-y divide-[var(--border)]">
                {[
                  ['POST', '/v1/chat/completions', 'OpenAI', '对话补全（流式/非流式）'],
                  ['POST', '/v1/embeddings', 'OpenAI', '文本向量化'],
                  ['POST', '/v1/images/generations', 'OpenAI', '图片生成'],
                  ['POST', '/v1/messages', 'Anthropic', 'Claude Messages（原生透传）'],
                  ['POST', '/v1/responses', 'OpenAI', 'Responses API（Codex 等）'],
                  ['GET', '/v1/models', '通用', '可用模型列表'],
                ].map(([method, path, proto, desc]) => (
                  <tr key={path} className="hover:bg-[var(--muted)] transition">
                    <td className="px-4 py-2"><span className={`text-xs font-medium px-1.5 py-0.5 rounded ${method === 'GET' ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' : 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'}`}>{method}</span></td>
                    <td className="px-4 py-2 font-mono text-xs">{path}</td>
                    <td className="px-4 py-2 text-xs text-[var(--muted-foreground)]">{proto}</td>
                    <td className="px-4 py-2 text-xs">{desc}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </Section>

        {/* SDK 接入 */}
        <Section id="sdk" title="SDK 接入">
          <div className="space-y-4">
            <div>
              <h3 className="text-sm font-medium mb-2">OpenAI SDK</h3>
              <TabGroup tabs={[
                {
                  label: 'Python',
                  lang: 'python',
                  content: `from openai import OpenAI

client = OpenAI(
    base_url="${BASE}/v1",
    api_key="sk-saig-xxx",
)

response = client.chat.completions.create(
    model="smart",  # 可使用别名或真实模型名
    messages=[{"role": "user", "content": "你好"}],
)
print(response.choices[0].message.content)`,
                },
                {
                  label: 'Node.js',
                  lang: 'typescript',
                  content: `import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${BASE}/v1",
  apiKey: "sk-saig-xxx",
});

const response = await client.chat.completions.create({
  model: "smart",
  messages: [{ role: "user", content: "你好" }],
});
console.log(response.choices[0].message.content);`,
                },
                {
                  label: 'curl',
                  lang: 'bash',
                  content: `curl ${BASE}/v1/chat/completions \\
  -H "Authorization: Bearer sk-saig-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "你好"}]
  }'`,
                },
              ]} />
            </div>
            <div>
              <h3 className="text-sm font-medium mb-2">Anthropic SDK（原生透传）</h3>
              <TabGroup tabs={[
                {
                  label: 'Python',
                  lang: 'python',
                  content: `import anthropic

client = anthropic.Anthropic(
    base_url="${BASE}",
    api_key="sk-saig-xxx",
)

message = client.messages.create(
    model="claude-sonnet-4-20250514",
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
print(message.content[0].text)`,
                },
                {
                  label: 'curl',
                  lang: 'bash',
                  content: `curl ${BASE}/v1/messages \\
  -H "Authorization: Bearer sk-saig-xxx" \\
  -H "Content-Type: application/json" \\
  -H "anthropic-version: 2023-06-01" \\
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "你好"}]
  }'`,
                },
              ]} />
            </div>
          </div>
        </Section>

        {/* 流式响应 */}
        <Section id="stream" title="流式响应">
          <p className="text-sm text-[var(--muted-foreground)] mb-3">在请求中设置 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">stream: true</code> 即可接收 SSE 流式响应。</p>
          <TabGroup tabs={[
            {
              label: 'Python',
              lang: 'python',
              content: `from openai import OpenAI

client = OpenAI(base_url="${BASE}/v1", api_key="sk-saig-xxx")

stream = client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "写一首诗"}],
    stream=True,
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="")`,
            },
            {
              label: 'Node.js',
              lang: 'typescript',
              content: `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${BASE}/v1", apiKey: "sk-saig-xxx" });

const stream = await client.chat.completions.create({
  model: "smart",
  messages: [{ role: "user", content: "写一首诗" }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content || "");
}`,
            },
            {
              label: 'curl',
              lang: 'bash',
              content: `curl ${BASE}/v1/chat/completions \\
  -H "Authorization: Bearer sk-saig-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "写一首诗"}],
    "stream": true
  }'`,
            },
          ]} />
        </Section>

        {/* 客户端/开发工具 */}
        <Section id="agents" title="客户端/开发工具">
          <div className="space-y-6">
            <Card className="p-4">
              <p className="text-sm mb-2"><strong>重要：模型配置</strong></p>
              <p className="text-sm text-[var(--muted-foreground)]">接入前请确认目标模型已在网关中配置（渠道模型 + 价格），否则请求会返回 404 或 400 错误。如果工具发送的模型名（如 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">claude-opus-4-8</code>）与网关中的模型名不同，需要在令牌的「模型映射」中配置映射关系，或使用环境变量指定实际模型名。</p>
            </Card>

            <div>
              <h3 className="text-sm font-medium mb-2">Claude Code（CLI / 桌面版 / IDE 插件）</h3>
              <p className="text-sm text-[var(--muted-foreground)] mb-2">Claude Code 使用 Anthropic 协议（<code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">/v1/messages</code>），请确保目标模型挂在支持 anthropic 协议的渠道上（渠道「支持协议」含 Anthropic）。</p>
              <p className="text-sm text-[var(--muted-foreground)] mb-3">推荐通过 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">settings.json</code> 配置，对 CLI、桌面版、IDE 插件均生效。</p>
              <TabGroup tabs={[
                {
                  label: 'settings.json',
                  lang: 'json',
                  content: `// ~/.claude/settings.json (Windows: C:\\Users\\<用户名>\\.claude\\settings.json)
{
  "env": {
    "ANTHROPIC_BASE_URL": "${BASE}",
    "ANTHROPIC_AUTH_TOKEN": "sk-saig-xxx",
    "ANTHROPIC_MODEL": "smart",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "fast",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "smart",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "max",
    "CLAUDE_CODE_SUBAGENT_MODEL": "smart"
  }
}`,
                },
                {
                  label: '环境变量',
                  lang: 'bash',
                  content: `export ANTHROPIC_BASE_URL="${BASE}"
export ANTHROPIC_API_KEY="sk-saig-xxx"

# 可选：指定模型（避免使用 Claude 默认模型名导致路由不到）
export ANTHROPIC_MODEL="smart"

claude`,
                },
              ]} />
              <div className="mt-3 p-3 rounded-md bg-[var(--muted)] text-xs text-[var(--muted-foreground)] space-y-1">
                <p><strong>注意事项：</strong></p>
                <p>1. <code className="font-mono">ANTHROPIC_BASE_URL</code> 填网关根地址（不带 <code className="font-mono">/v1</code>），网关会自动拼接 <code className="font-mono">/v1/messages</code></p>
                <p>2. 模型名需与网关中已配置的模型或别名一致，建议使用别名（如 smart/fast/max）</p>
                <p>3. 配置保存后需新开终端窗口才会生效，可用 <code className="font-mono">/status</code> 命令确认</p>
              </div>
            </div>

            <div>
              <h3 className="text-sm font-medium mb-2">CC Switch</h3>
              <p className="text-sm text-[var(--muted-foreground)] mb-2"><a href="https://github.com/farion1231/cc-switch" target="_blank" rel="noopener" className="text-[var(--primary)] hover:underline">CC Switch</a> 是桌面 GUI 工具，支持多供应商一键切换。添加本网关为供应商：</p>
              <KVTable rows={[
                ['供应商名称', '自定义，如"AI Gateway"'],
                ['API Key', 'sk-saig-xxx'],
                ['请求地址', BASE],
              ]} />
              <p className="text-xs text-[var(--muted-foreground)] mt-2">展开「高级选项」配置模型映射，将主模型及 Haiku/Sonnet/Opus 默认模型设置为网关中已有的模型或别名。</p>
            </div>

            <div>
              <h3 className="text-sm font-medium mb-2">Codex CLI</h3>
              <p className="text-sm text-[var(--muted-foreground)] mb-2">Codex CLI 使用 Responses API（<code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">/v1/responses</code>），配置 OpenAI 兼容端点即可。</p>
              <Code lang="bash">{`export OPENAI_BASE_URL="${BASE}/v1"
export OPENAI_API_KEY="sk-saig-xxx"

codex`}</Code>
            </div>

            <div>
              <h3 className="text-sm font-medium mb-2">OpenCode / Cursor / Cherry Studio 等</h3>
              <p className="text-sm text-[var(--muted-foreground)] mb-2">任何支持自定义 Base URL 的 OpenAI 兼容客户端均可接入。</p>
              <KVTable rows={[
                ['Base URL', `${BASE}/v1`],
                ['API Key', 'sk-saig-xxx'],
                ['模型', '填网关中的模型名或别名（如 smart）'],
              ]} />
              <p className="text-xs text-[var(--muted-foreground)] mt-2">通常在工具的设置/供应商配置中填入上述信息即可。部分工具支持通过 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">GET /v1/models</code> 自动获取可用模型列表。</p>
            </div>
          </div>
        </Section>

        {/* 模型别名 */}
        <Section id="aliases" title="模型别名">
          <Card className="p-4">
            <p className="text-sm mb-3">本网关支持模型别名路由。你可以在请求中使用别名代替真实模型名，网关会按优先级+权重策略自动选择最优渠道。</p>
            <p className="text-sm text-[var(--muted-foreground)] mb-2">例如使用别名 <code className="text-xs bg-[var(--muted)] px-1.5 py-0.5 rounded font-mono">smart</code> 发起请求，网关会根据路由规则自动分发到 GPT-4o、Claude Sonnet 等实际模型。</p>
            {portal
              ? <p className="text-sm text-[var(--muted-foreground)]">可用别名请咨询管理员。</p>
              : <p className="text-sm">当前已配置的别名及路由规则请查看 <a href="/aliases" className="text-[var(--primary)] hover:underline">别名路由</a> 页面。</p>}
          </Card>
        </Section>

        {/* 计费说明 */}
        <Section id="billing" title="计费说明">
          <Card className="p-4 space-y-2">
            <p className="text-sm">本网关按实际消耗的 Token 数计费（内部统一 USD 计价）：</p>
            <ul className="text-sm space-y-1.5 list-disc list-inside text-[var(--muted-foreground)]">
              <li>费用 = (输入 Token 数 x 输入单价 + 输出 Token 数 x 输出单价) x 渠道倍率</li>
              <li>图片生成类接口按次计费</li>
              <li>配额归属于应用（App），多个令牌共享同一应用配额</li>
              <li>令牌可独立设置日成本上限，超限当日返回 402</li>
            </ul>
            {!portal && <p className="text-sm">各模型价格详见 <a href="/prices" className="text-[var(--primary)] hover:underline">价格管理</a> 页面。</p>}
          </Card>
        </Section>

        {/* 错误码 */}
        <Section id="errors" title="错误码">
          <Card className="p-0 overflow-hidden">
            <table className="w-full text-sm">
              <Thead cols={['状态码', '含义', '处理建议']} />
              <tbody className="divide-y divide-[var(--border)]">
                {[
                  ['400', '请求格式错误 / 未配置模型价格', '检查请求体 JSON 格式；确认模型已在价格管理中配价'],
                  ['401', 'API Key 无效或已过期', '检查 Authorization 头；确认令牌状态为启用'],
                  ['402', '配额不足 / 日成本上限', '充值应用配额；或等次日日成本重置'],
                  ['403', '模型白名单拦截 / 渠道未授权', '检查令牌的模型白名单设置；确认应用授权了目标渠道'],
                  ['429', 'RPM 限流', '降低请求频率；或调大令牌/渠道的 RPM 限制'],
                  ['502', '已打到上游但全部失败', '上游报错 / 连接失败 / 协议转换失败，网关已自动重试其他渠道'],
                  ['503', '暂无可用渠道', '全部候选都没打到上游（熔断中 / 限流冷却 / 无可用代理）；按响应头 Retry-After 建议的秒数退避后重试'],
                ].map(([code, meaning, action]) => (
                  <tr key={code} className="hover:bg-[var(--muted)] transition">
                    <td className="px-4 py-2 font-mono text-xs font-medium">{code}</td>
                    <td className="px-4 py-2 text-xs">{meaning}</td>
                    <td className="px-4 py-2 text-xs text-[var(--muted-foreground)]">{action}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </Section>
      </div>

      {/* 右侧目录 */}
      <nav className="hidden xl:block w-44 shrink-0 sticky top-0 self-start pt-2">
        <div className="text-xs font-semibold text-[var(--muted-foreground)] mb-2">目录</div>
        <ul className="space-y-1.5">
          {toc.map(({ id, title }) => (
            <li key={id}>
              <a href={`#${id}`} className={`text-xs transition ${activeId === id ? 'text-[var(--primary)] font-medium' : 'text-[var(--muted-foreground)] hover:text-[var(--foreground)]'}`}>{title}</a>
            </li>
          ))}
        </ul>
      </nav>
    </div>
  )
}
