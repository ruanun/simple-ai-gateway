import { useState } from 'react'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { portalApi } from '../lib/api'
import { usd, fmtDateTime } from '../lib/format'
import { Table, Th, Td } from '../components/ui/table'
import { Badge } from '../components/ui/badge'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { PaginationBar } from '../components/ui/pagination'
import type { App, RequestLogRow } from '../lib/types'
import { useI18n } from '../lib/i18n'

type Log = Pick<RequestLogRow, 'id' | 'created_at' | 'app_id' | 'model' | 'status' | 'prompt_tokens' | 'completion_tokens' | 'cost_micros' | 'latency_ms' | 'request_type'>

// PortalLogs 门户请求日志：仅自己名下 App 的调用记录（服务端强制归属过滤）。
export function PortalLogs() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [page, setPage] = useState(0)
  const [pageSize, setPageSize] = useState(20)
  const [appID, setAppID] = useState(0)

  const { data: apps = [] } = useQuery({
    queryKey: ['/portal/api/apps'],
    queryFn: () => portalApi.get<App[]>('/portal/api/apps'),
    meta: { silentError: true },
  })
  // 分页/筛选参数进 queryKey；keepPreviousData 让翻页时保留旧数据避免闪空
  const { data: logs } = useQuery({
    queryKey: ['/portal/api/request-logs', { page, pageSize, appID }],
    queryFn: () => {
      const q = new URLSearchParams({ limit: String(pageSize), offset: String(page * pageSize) })
      if (appID) q.set('app_id', String(appID))
      return portalApi.get<{ total: number; items: Log[] }>(`/portal/api/request-logs?${q}`)
    },
    placeholderData: keepPreviousData,
  })
  const rows = logs?.items ?? []
  const total = logs?.total ?? 0
  // 刷新：前缀失效覆盖所有分页/筛选变体
  const load = () => qc.invalidateQueries({ queryKey: ['/portal/api/request-logs'] })

  function appName(id: number) { return apps.find(a => a.id === id)?.name ?? `#${id}` }

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('请求日志')}</h2>
      <div className="flex items-center gap-2">
        <SimpleSelect value={String(appID)} onChange={v => { setAppID(Number(v)); setPage(0) }}
          options={[{ value: '0', label: t('全部应用') }, ...apps.map(a => ({ value: String(a.id), label: a.name }))]} />
        <RefreshButton onRefresh={load} />
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('时间')}</Th><Th>{t('应用')}</Th><Th>{t('模型')}</Th><Th>{t('类型')}</Th><Th>{t('状态')}</Th><Th>{t('Tokens(入/出)')}</Th><Th>{t('费用')}</Th><Th>{t('耗时')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td className="tnum">{fmtDateTime(r.created_at)}</Td>
        <Td>{appName(r.app_id)}</Td>
        <Td>{r.model}</Td>
        <Td className="tnum">{r.request_type}</Td>
        <Td>{r.status < 400 ? <Badge status="ok">{r.status}</Badge> : <Badge status="down">{r.status}</Badge>}</Td>
        <Td className="tnum">{r.prompt_tokens}/{r.completion_tokens}</Td>
        <Td className="tnum">{usd(r.cost_micros)}</Td>
        <Td className="tnum">{r.latency_ms}ms</Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无请求记录')}</Td></tr>}
    </Table>
    <PaginationBar total={total} page={page} pageSize={pageSize} onPageChange={setPage} showPageInfo
      onPageSizeChange={n => { setPageSize(n); setPage(0) }} showJumper showEdges />
  </div>
}
