import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { num, usd } from '../lib/format'
import type { ChannelHealthRow } from '../lib/types'
import { Card } from '../components/ui/card'
import { Skeleton } from '../components/ui/skeleton'
import { Segmented } from '../components/ui/segmented'
import { Table, Th, Td } from '../components/ui/table'
import { Pill } from '../components/ui/badge'
import { useI18n } from '../lib/i18n'

const dayOptions = [{ label: '7天', value: 7 }, { label: '14天', value: 14 }, { label: '30天', value: 30 }]

function ErrorRate({ rate }: { rate: number }) {
  const pct = rate * 100
  if (pct >= 10) return <Pill status="down">{pct.toFixed(1)}%</Pill>
  if (pct >= 5) return <Pill status="warn">{pct.toFixed(1)}%</Pill>
  return <span className="tnum">{pct.toFixed(1)}%</span>
}

// 渠道健康：按渠道聚合最近 N 天成功率/加权延迟/首 Token/费用（数据源 usage_daily_channel）。
export function ChannelHealth() {
  const { t } = useI18n()
  const [days, setDays] = useState(7)
  const { data = [], isLoading } = useQuery({
    queryKey: ['/admin/channels/health', days],
    queryFn: () => api.get<ChannelHealthRow[]>(`/admin/channels/health?days=${days}`),
  })

  if (isLoading) return <div className="space-y-4">
    <Skeleton className="h-8 w-40" />
    <Card className="p-4"><Skeleton className="h-64 w-full" /></Card>
  </div>

  const totalReq = data.reduce((s, r) => s + r.requests, 0)
  const totalErr = data.reduce((s, r) => s + r.errors, 0)
  const totalCost = data.reduce((s, r) => s + r.cost_micros, 0)

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('渠道健康')} <span className="text-xs font-normal text-[var(--muted-foreground)]">{t('最近 {days} 天', { days })}</span></h2>
      <Segmented options={dayOptions.map(o => ({ ...o, label: t(o.label) }))} value={days} onChange={setDays} />
    </div>
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <Card className="p-4"><div className="text-xs text-[var(--muted-foreground)]">{t('请求数')}</div><div className="mt-1 text-2xl font-semibold tnum">{num(totalReq)}</div></Card>
      <Card className="p-4"><div className="text-xs text-[var(--muted-foreground)]">{t('错误数')}</div><div className="mt-1 text-2xl font-semibold tnum">{num(totalErr)}</div></Card>
      <Card className="p-4"><div className="text-xs text-[var(--muted-foreground)]">{t('整体错误率')}</div><div className="mt-1 text-2xl font-semibold tnum">{totalReq > 0 ? `${(totalErr / totalReq * 100).toFixed(1)}%` : '0%'}</div></Card>
      <Card className="p-4"><div className="text-xs text-[var(--muted-foreground)]">{t('费用')}</div><div className="mt-1 text-2xl font-semibold tnum">{usd(totalCost)}</div></Card>
    </div>
    <Card className="p-4">
      <Table head={<><Th>{t('渠道')}</Th><Th>{t('请求')}</Th><Th>{t('错误')}</Th><Th>{t('错误率')}</Th><Th>{t('平均延迟')}</Th><Th>{t('首 Token')}</Th><Th>{t('费用')}</Th></>}>
        {data.map(r => <tr key={r.channel_id} className="hover:bg-[var(--muted)]">
          <Td>{r.name}</Td>
          <Td className="tnum">{num(r.requests)}</Td>
          <Td className="tnum">{r.errors > 0 ? <span className="text-[var(--down)]">{num(r.errors)}</span> : '0'}</Td>
          <Td><ErrorRate rate={r.error_rate} /></Td>
          <Td className="tnum">{num(r.avg_latency_ms)} ms</Td>
          <Td className="tnum">{r.avg_first_token_ms > 0 ? `${num(r.avg_first_token_ms)} ms` : '—'}</Td>
          <Td className="tnum">{usd(r.cost_micros)}</Td>
        </tr>)}
        {data.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('该时间范围内暂无渠道流量')}</Td></tr>}
      </Table>
    </Card>
  </div>
}
