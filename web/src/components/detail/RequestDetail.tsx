import { useState } from 'react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '../ui/tabs'
import { CodeView } from '../ui/code-view'
import { usd } from '../../lib/format'
import { IpInfoDialog } from './IpInfoDialog'
import type { RequestLogRow } from '../../lib/types'
import { useI18n } from '../../lib/i18n'

// user_agent 不在 RequestLogRow 里：它是 LogDetail 的顶层字段（非 log 子对象），需单独补上。
// 下列字段组件内直接读取（非 ?./|| 兜底），Partial 会打破这些位置的编译，故保留必填。
type RequiredDetailFields = 'model' | 'request_type' | 'status' | 'latency_ms' | 'first_token_ms' | 'stream' | 'prompt_tokens' | 'completion_tokens' | 'cost_micros'
export type DetailMeta = Partial<RequestLogRow> & Required<Pick<RequestLogRow, RequiredDetailFields>> & { user_agent?: string }

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="rounded-md border border-[var(--border)] bg-[var(--card)] px-3 py-2">
      <div className="text-xs text-[var(--muted-foreground)]">{label}</div>
      <div className={`tnum text-lg font-semibold ${tone || ''}`}>{value}</div>
    </div>
  )
}

function ms(n: number): string { return n >= 1000 ? (n / 1000).toFixed(2) + 's' : n + 'ms' }

// TruncatedBadge 正文被服务端截断存储时的提示徽章。
function TruncatedBadge({ truncated, origSize, storedLen }: { truncated?: boolean; origSize?: number; storedLen: number }) {
  const { t } = useI18n()
  if (!truncated) return null
  const kb = (n: number) => (n / 1024).toFixed(1) + ' KB'
  return (
    <div className="mb-2 rounded-md border border-[var(--destructive)]/40 bg-[var(--destructive)]/10 px-3 py-1.5 text-xs text-[var(--destructive)]">
      ⚠ {t('正文已截断存储：原始 {orig}，仅存前 {stored}（审计字节封顶）', { orig: kb(origSize || 0), stored: kb(storedLen) })}
    </div>
  )
}

// Sha256Line 取证完整性哈希：审计落库时对原文全文计算，截断存储也不影响哈希值。
function Sha256Line({ hash }: { hash?: string }) {
  if (!hash) return null
  return (
    <div className="mb-2 text-xs text-[var(--muted-foreground)]">
      SHA-256 <span className="font-mono break-all">{hash}</span>
    </div>
  )
}

// RequestDetail 请求详情主体：概览/请求体/响应体/性能 四个 Tab（正文来自关联审计记录）。
export function RequestDetail({
  meta, requestBody, responseBody, hasBody,
  requestTruncated, requestOrigSize, responseTruncated, responseOrigSize,
  requestSha256, responseSha256,
}: {
  meta: DetailMeta
  requestBody?: string
  responseBody?: string
  hasBody: boolean
  requestTruncated?: boolean
  requestOrigSize?: number
  responseTruncated?: boolean
  responseOrigSize?: number
  requestSha256?: string
  responseSha256?: string
}) {
  const { t } = useI18n()
  const [tab, setTab] = useState('overview')
  const [ipOpen, setIpOpen] = useState(false)
  const totalTok = (meta.prompt_tokens || 0) + (meta.completion_tokens || 0)
  const outRate = meta.first_token_ms > 0 && meta.latency_ms > meta.first_token_ms && meta.completion_tokens > 0
    ? (meta.completion_tokens / ((meta.latency_ms - meta.first_token_ms) / 1000)).toFixed(1) + ' tok/s'
    : '—'

  const tabs = [
    { key: 'overview', label: t('概览') },
    ...(hasBody ? [{ key: 'request', label: t('请求体') }, { key: 'response', label: t('响应体') }] : []),
    { key: 'perf', label: t('性能') },
  ]

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Stat label={t('状态')} value={String(meta.status)} tone={meta.status < 400 ? 'text-green-600' : 'text-[var(--destructive)]'} />
        <Stat label={t('费用')} value={usd(meta.cost_micros || 0)} />
        <Stat label={t('耗时')} value={ms(meta.latency_ms)} />
        <Stat label="Tokens" value={totalTok.toLocaleString()} />
      </div>

      <Tabs value={tab} onValueChange={setTab}>
      <TabsList>
        {tabs.map((t) => <TabsTrigger key={t.key} value={t.key}>{t.label}</TabsTrigger>)}
      </TabsList>

      <TabsContent value="overview">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          {meta.request_id && <><dt className="text-[var(--muted-foreground)]">Request ID</dt><dd className="tnum break-all">{meta.request_id}</dd></>}
          <dt className="text-[var(--muted-foreground)]">{t('模型')}</dt><dd className="tnum">{meta.model}</dd>
          <dt className="text-[var(--muted-foreground)]">{t('接口类型')}</dt><dd>{meta.request_type}</dd>
          {meta.protocol && <><dt className="text-[var(--muted-foreground)]">{t('协议')}</dt><dd>{meta.upstream_protocol && meta.upstream_protocol !== meta.protocol ? `${meta.protocol}→${meta.upstream_protocol}` : meta.protocol}</dd></>}
          <dt className="text-[var(--muted-foreground)]">{t('流式')}</dt><dd>{meta.stream ? t('是') : t('否')}</dd>
          <dt className="text-[var(--muted-foreground)]">{t('输入/输出')}</dt><dd className="tnum">↑{(meta.prompt_tokens || 0).toLocaleString()} / ↓{(meta.completion_tokens || 0).toLocaleString()}</dd>
          {((meta.cached_tokens || 0) > 0 || (meta.cache_creation_tokens || 0) > 0 || (meta.reasoning_tokens || 0) > 0) && (
            <><dt className="text-[var(--muted-foreground)]">{t('缓存/推理')}</dt><dd className="tnum space-x-2">
              {(meta.cached_tokens || 0) > 0 && <span>{t('缓存读')} {(meta.cached_tokens || 0).toLocaleString()}</span>}
              {(meta.cache_creation_tokens || 0) > 0 && <span>{t('缓存写')} {(meta.cache_creation_tokens || 0).toLocaleString()}</span>}
              {(meta.reasoning_tokens || 0) > 0 && <span>{t('推理')} {(meta.reasoning_tokens || 0).toLocaleString()}</span>}
            </dd></>
          )}
          {meta.session_id && <><dt className="text-[var(--muted-foreground)]">{t('会话')}</dt><dd className="tnum break-all">{meta.session_id}</dd></>}
          {meta.channel_id != null && <><dt className="text-[var(--muted-foreground)]">{t('渠道 ID')}</dt><dd className="tnum">{meta.channel_id || '—'}</dd></>}
          {meta.account_id != null && <><dt className="text-[var(--muted-foreground)]">{t('账号 ID')}</dt><dd className="tnum">{meta.account_id || '—'}</dd></>}
        </dl>

        {(meta.client_ip || meta.user_agent) && (
          <div className="mt-4">
            <div className="mb-1.5 text-xs font-medium text-[var(--muted-foreground)]">{t('客户端信息')}</div>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
              {meta.client_ip && <><dt className="text-[var(--muted-foreground)]">IP</dt><dd>
                <button className="text-[var(--primary)] hover:underline tnum" onClick={() => setIpOpen(true)}>{meta.client_ip}</button>
              </dd></>}
              {meta.user_agent && <><dt className="text-[var(--muted-foreground)]">User-Agent</dt><dd className="break-all text-xs">{meta.user_agent}</dd></>}
            </dl>
          </div>
        )}
      </TabsContent>

      {hasBody && <>
        <TabsContent value="request">
          <TruncatedBadge truncated={requestTruncated} origSize={requestOrigSize} storedLen={(requestBody || '').length} />
          <Sha256Line hash={requestSha256} />
          <CodeView content={requestBody || ''} filename="request.json" />
        </TabsContent>
        <TabsContent value="response">
          <TruncatedBadge truncated={responseTruncated} origSize={responseOrigSize} storedLen={(responseBody || '').length} />
          <Sha256Line hash={responseSha256} />
          <CodeView content={responseBody || ''} filename="response.txt" />
        </TabsContent>
      </>}

      <TabsContent value="perf">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-[var(--muted-foreground)]">{t('总耗时')}</dt><dd className="tnum">{ms(meta.latency_ms)}</dd>
          <dt className="text-[var(--muted-foreground)]">{t('首字延迟 (TTFB)')}</dt><dd className="tnum">{meta.first_token_ms > 0 ? ms(meta.first_token_ms) : '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">{t('生成耗时')}</dt><dd className="tnum">{meta.first_token_ms > 0 ? ms(meta.latency_ms - meta.first_token_ms) : '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">{t('输出速率')}</dt><dd className="tnum">{outRate}</dd>
        </dl>
      </TabsContent>
      </Tabs>

      {meta.client_ip && <IpInfoDialog ip={meta.client_ip} open={ipOpen} onClose={() => setIpOpen(false)} />}
    </div>
  )
}
