import { useState } from 'react'
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usd, fmtDateTime } from '../lib/format'
import type { RequestLogRow, LogDetail } from '../lib/types'
import { Table, Th, Td } from '../components/ui/table'
import { Badge } from '../components/ui/badge'
import { Dialog } from '../components/ui/dialog'
import { Drawer } from '../components/ui/drawer'
import { RequestDetail } from '../components/detail/RequestDetail'
import { CodeView } from '../components/ui/code-view'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { Tip } from '../components/ui/tooltip'
import { Input } from '../components/ui/input'
import { PaginationBar } from '../components/ui/pagination'
import { LogFilterBar } from '../components/log/LogFilterBar'
import { REQUEST_TYPE_OPTIONS } from '../components/log/requestTypes'
import { TokensCell } from '../components/log/TokensCell'
import { useI18n } from '../lib/i18n'

type Log = RequestLogRow

export function Logs() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [page, setPage] = useState(0)
  const [pageSize, setPageSize] = useState(20)

  const [filterChannel, setFilterChannel] = useState('')
  const [filterApp, setFilterApp] = useState('')
  // 初始筛选从 URL query 读（如令牌页跳转带的 ?api_key_id=5）
  const [filterKeyId, setFilterKeyId] = useState(() => new URLSearchParams(window.location.search).get('api_key_id') ?? '')
  const [filterModel, setFilterModel] = useState('')
  const [filterType, setFilterType] = useState('')
  const [filterSession, setFilterSession] = useState('')
  const [startTime, setStartTime] = useState('')
  const [endTime, setEndTime] = useState('')
  const [headersView, setHeadersView] = useState('')
  const [errorView, setErrorView] = useState('')

  // 详情抽屉：点击 ID 按需拉取单条日志详情（含审计正文，若该应用开启审计）
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data: detail, isError: detailError } = useQuery({
    queryKey: ['/admin/request-logs', 'detail', detailId],
    queryFn: () => api.get<LogDetail>(`/admin/request-logs/${detailId}`),
    enabled: detailId !== null,
  })

  // 下拉选项：渠道/应用列表（多页共享同 key，自动去重缓存）
  const { data: channels = [] } = useQuery({
    queryKey: ['/admin/channels'],
    queryFn: () => api.get<{ id: number; name: string }[]>('/admin/channels'),
  })
  const { data: apps = [] } = useQuery({
    queryKey: ['/admin/apps'],
    queryFn: () => api.get<{ id: number; name: string }[]>('/admin/apps'),
  })

  // 列表查询：筛选/分页参数全部进 queryKey，变化自动重取
  const params = { page, pageSize, channel: filterChannel, app: filterApp, keyId: filterKeyId, model: filterModel, type: filterType, session: filterSession, start: startTime, end: endTime }
  const { data } = useQuery({
    queryKey: ['/admin/request-logs', params],
    queryFn: () => {
      const q = new URLSearchParams({ limit: String(pageSize), offset: String(page * pageSize) })
      if (filterChannel) q.set('channel_id', filterChannel)
      if (filterApp) q.set('app_id', filterApp)
      if (filterKeyId) q.set('api_key_id', filterKeyId)
      if (filterModel) q.set('model', filterModel)
      if (filterType) q.set('request_type', filterType)
      if (filterSession) q.set('session_id', filterSession)
      if (startTime) q.set('start_time', startTime)
      if (endTime) q.set('end_time', endTime)
      return api.get<{ total: number; items: Log[] }>(`/admin/request-logs?${q}`)
    },
    placeholderData: keepPreviousData, // 翻页/改筛选时保留旧数据防闪空
  })
  const items = data?.items ?? []
  const total = data?.total ?? 0
  // 手动刷新：前缀失效全部日志查询
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/request-logs'] })

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('请求日志')}</h2>
      <RefreshButton onRefresh={load} />
    </div>
    {/* 筛选栏（含会话筛选高亮条；模型名/接口类型为本页独有，走 after 插槽） */}
    <LogFilterBar
      channels={channels} apps={apps}
      channel={filterChannel} onChannelChange={v => { setFilterChannel(v); setPage(0) }}
      app={filterApp} onAppChange={v => { setFilterApp(v); setPage(0) }}
      keyId={filterKeyId} onKeyIdChange={v => { setFilterKeyId(v); setPage(0) }}
      start={startTime} end={endTime} onTimeChange={(s, e) => { setStartTime(s); setEndTime(e); setPage(0) }}
      session={filterSession} onSessionClear={() => { setFilterSession(''); setPage(0) }}
      after={<>
        <Input className="w-40" placeholder={t('模型名')} value={filterModel}
          onChange={e => { setFilterModel(e.target.value); setPage(0) }} />
        <SimpleSelect value={filterType} onChange={v => { setFilterType(v); setPage(0) }}
          options={REQUEST_TYPE_OPTIONS.map(o => ({ ...o, label: t(o.label) }))} />
      </>}
    />
    <Table head={<><Th>ID</Th><Th>{t('时间')}</Th><Th>{t('Request ID')}</Th><Th>{t('模型')}</Th><Th>{t('协议')}</Th><Th>{t('渠道')}</Th><Th>{t('应用')}</Th><Th>{t('接口类型')}</Th><Th>{t('会话')}</Th><Th>{t('状态')}</Th><Th>{t('延迟')}</Th><Th>{t('Tokens')}</Th><Th>{t('费用')}</Th><Th>{t('流式')}</Th><Th>{t('IP')}</Th><Th>{t('请求头')}</Th></>}>
      {items.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">
          <button className="text-[var(--primary)] hover:underline" onClick={() => setDetailId(r.id)}>{r.id}</button>
        </Td>
        <Td className="tnum">{fmtDateTime(r.created_at)}</Td>
        <Td>{r.request_id ? (
          <Tip content={r.request_id}>
            <span className="font-mono text-xs">{r.request_id.slice(0, 10)}…</span>
          </Tip>
        ) : <span className="text-[var(--muted-foreground)]">—</span>}</Td>
        <Td className="tnum">{r.model}</Td>
        <Td>{r.protocol ? (r.upstream_protocol && r.upstream_protocol !== r.protocol ? `${r.protocol}→${r.upstream_protocol}` : r.protocol) : '-'}</Td>
        <Td>{channels.find(c => c.id === r.channel_id)?.name || r.channel_id || '—'}</Td>
        <Td>{apps.find(a => a.id === r.app_id)?.name || r.app_id || '—'}</Td>
        <Td><span className="rounded bg-[var(--muted)] px-1.5 py-0.5 text-xs">{r.request_type || 'chat'}</span></Td>
        <Td>{r.session_id ? (
          <Tip content={r.session_id}>
            <button className="text-xs text-[var(--primary)] hover:underline font-mono"
              onClick={() => { setFilterSession(r.session_id); setPage(0) }}>{r.session_id.slice(0, 8)}…</button>
          </Tip>
        ) : <span className="text-[var(--muted-foreground)]">—</span>}</Td>
        {/* 状态：正常直接显示徽标；异常（有 error_msg）时叠加提示交互——悬停看摘要、点击看完整错误 */}
        <Td>{r.error_msg ? (
          <Tip content={r.error_msg}>
            <button className="cursor-pointer hover:opacity-80" onClick={() => setErrorView(r.error_msg)}>
              <Badge status="down">{r.status}</Badge>
            </button>
          </Tip>
        ) : (
          r.status < 400 ? <Badge status="ok">{r.status}</Badge> : <Badge status="down">{r.status}</Badge>
        )}</Td>
        <Td>
          <div className="leading-snug">
            <div className="tnum">{r.latency_ms.toLocaleString()}ms</div>
            {r.first_token_ms > 0 && <div className="text-xs text-[var(--muted-foreground)] tnum">首字 {r.first_token_ms.toLocaleString()}ms</div>}
          </div>
        </Td>
        <Td><TokensCell prompt={r.prompt_tokens} completion={r.completion_tokens} cached={r.cached_tokens} /></Td>
        <Td className="tnum">{usd(r.cost_micros || 0)}{r.usage_missing ? ' ⚠' : ''}</Td>
        <Td>{r.stream ? t('是') : t('否')}</Td>
        <Td className="tnum">{r.client_ip || '—'}</Td>
        <Td>
          {r.headers ? (
            <button
              className="text-xs text-[var(--primary)] hover:underline"
              onClick={() => setHeadersView(r.headers)}
            >{t('查看')}</button>
          ) : '—'}
        </Td>
      </tr>)}
      {items.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无日志')}</Td></tr>}
    </Table>
    <PaginationBar total={total} page={page} pageSize={pageSize} onPageChange={setPage}
      onPageSizeChange={n => { setPageSize(n); setPage(0) }} showJumper showEdges />
    <Dialog open={!!headersView} title={t('请求头')} onClose={() => setHeadersView('')} width="max-w-2xl">
      <CodeView content={headersView} filename="headers.json" />
    </Dialog>
    <Dialog open={!!errorView} title={t('错误详情')} onClose={() => setErrorView('')} width="max-w-2xl">
      <pre className="text-xs bg-[var(--muted)] p-3 rounded-md overflow-auto max-h-[60vh] whitespace-pre-wrap break-all">{errorView}</pre>
    </Dialog>
    <Drawer open={detailId !== null} onClose={() => setDetailId(null)}
      title={detail ? `${t('请求日志')} #${detail.log.id}` : (detailError ? t('拉取详情失败') : t('加载中…'))}>
      {detail && <RequestDetail
        meta={{ ...detail.log, user_agent: detail.user_agent }}
        requestBody={detail.request_body}
        responseBody={detail.response_body}
        hasBody={detail.has_audit}
        requestTruncated={detail.request_truncated}
        requestOrigSize={detail.request_orig_size}
        responseTruncated={detail.response_truncated}
        responseOrigSize={detail.response_orig_size}
        requestSha256={detail.request_sha256}
        responseSha256={detail.response_sha256}
      />}
      {detail && !detail.has_audit && (
        <div className="mt-3 rounded-md bg-[var(--muted)] px-3 py-2 text-xs text-[var(--muted-foreground)]">
          {t('该请求所属应用未开启审计，无请求/响应正文。')}
        </div>
      )}
      {detailError && <div className="text-sm text-[var(--destructive)]">{t('拉取详情失败')}</div>}
    </Drawer>
  </div>
}
