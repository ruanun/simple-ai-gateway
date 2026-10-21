import { useState } from 'react'
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api } from '../lib/api'
import { fmtDateTime } from '../lib/format'
import { Input } from '../components/ui/input'
import { Table, Th, Td } from '../components/ui/table'
import { Badge } from '../components/ui/badge'
import { RefreshButton } from '../components/ui/refresh'
import { PaginationBar } from '../components/ui/pagination'
import type { OpLog } from '../lib/types'
import { useI18n } from '../lib/i18n'

const ROLE_LABELS: Record<string, string> = { super: '超管', ops: '运营', viewer: '只读' }

export function OpLogs() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [page, setPage] = useState(0)
  const [pageSize, setPageSize] = useState(20)
  const [actor, setActor] = useState('')

  // 列表查询：筛选/分页参数全部进 queryKey，变化自动重取
  const params = { page, pageSize, actor }
  const { data } = useQuery({
    queryKey: ['/admin/op-logs', params],
    queryFn: () => {
      const q = new URLSearchParams({ limit: String(pageSize), offset: String(page * pageSize) })
      if (actor) q.set('actor', actor)
      return api.get<{ total: number; items: OpLog[] }>(`/admin/op-logs?${q}`)
    },
    placeholderData: keepPreviousData, // 翻页/改筛选时保留旧数据防闪空
  })
  const rows = data?.items ?? []
  const total = data?.total ?? 0
  // 手动刷新：前缀失效全部操作日志查询
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/op-logs'] })

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('操作日志')}</h2>
      <div className="flex items-center gap-2">
        <Input className="w-48" placeholder={t('按操作人筛选')} value={actor}
          onChange={e => { setActor(e.target.value); setPage(0) }} />
        <RefreshButton onRefresh={load} />
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('时间')}</Th><Th>{t('操作人')}</Th><Th>{t('角色')}</Th><Th>{t('方法')}</Th><Th>{t('路径')}</Th><Th>{t('结果')}</Th><Th>{t('IP')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td className="tnum">{fmtDateTime(r.created_at)}</Td>
        <Td>{r.actor}</Td>
        <Td>{t(ROLE_LABELS[r.role] ?? r.role)}</Td>
        <Td className="tnum">{r.method}</Td>
        <Td className="tnum">{r.path}</Td>
        <Td>{r.status < 400 ? <Badge status="ok">{r.status}</Badge> : <Badge status="down">{r.status}</Badge>}</Td>
        <Td className="tnum">{r.client_ip}</Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无操作记录（仅记录写操作）')}</Td></tr>}
    </Table>
    <PaginationBar total={total} page={page} pageSize={pageSize} onPageChange={setPage} showPageInfo
      onPageSizeChange={n => { setPageSize(n); setPage(0) }} showJumper showEdges />
  </div>
}
