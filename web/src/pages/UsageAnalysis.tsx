import { useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, portalApi } from '../lib/api'
import { usd, num, microsToUsd } from '../lib/format'
import { Card } from '../components/ui/card'
import { Button } from '../components/ui/button'
import { Segmented } from '../components/ui/segmented'
import { Table, Th, Td } from '../components/ui/table'
import { Skeleton } from '../components/ui/skeleton'
import { ArrowLeft } from 'lucide-react'
import { Tip } from '../components/ui/tooltip'
import { LineChart, Line, BarChart, Bar, PieChart, Pie, Cell, XAxis, YAxis, Tooltip, Legend, ResponsiveContainer, CartesianGrid } from 'recharts'
import type { UsageAnalysisData, ChannelUsageAnalysisData, ChannelTrendRow } from '../lib/types'
import { type Metric, METRIC_OPTIONS, metricValue, tooltipStyle, PALETTE } from '../lib/metrics'
import { useI18n } from '../lib/i18n'

// 迁移前的存量聚合行 model 为空串，展示为占位名
const modelName = (m: string) => m || '（历史未细分）'

function Kpi({ label, value, hint }: { label: string; value: string; hint?: string }) {
  const head = <div className="text-xs text-[var(--muted-foreground)]">
    {label}{hint ? <span className="ml-1 cursor-help underline decoration-dotted">?</span> : null}
  </div>
  return <Card className="p-4">
    {hint ? <Tip content={hint}>{head}</Tip> : head}
    <div className="mt-1 text-xl font-semibold tnum">{value}</div>
  </Card>
}

// 三入口共用的用量分析视图：期间 KPI + 总量趋势 + 每日模型构成堆叠柱 + 占比环形 + 明细表
function AnalysisView({ title, backTo, keyBase, fetcher, extraKpis }: {
  title: string
  backTo: string
  keyBase: string // 查询缓存 key 前缀（含资源 id），days 追加其后
  fetcher: (days: number) => Promise<UsageAnalysisData>
  // 可选的额外 KPI（渠道页用于展示失败数/错误率/平均延迟）。传函数而非数组，因为要基于
  // 本组件内部才拿得到的 data 计算；不传则完全不渲染，应用/令牌两页行为零变化。
  extraKpis?: (data: UsageAnalysisData) => { label: string; value: string; hint?: string }[]
}) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [days, setDays] = useState(7)
  const [metric, setMetric] = useState<Metric>('tokens')
  const { data, isError, refetch } = useQuery({ queryKey: [keyBase, days], queryFn: () => fetcher(days) })

  const trend = useMemo(() => (data?.trend ?? []).map(t => ({ ...t, date: t.stat_date.slice(5), cost: microsToUsd(t.cost_micros) })), [data])
  const models = useMemo(() => data?.models ?? [], [data])
  const daily = useMemo(() => data?.daily_models ?? [], [data])

  // 模型 → 颜色映射：以 models（三块共用的模型全集，已按费用降序）的顺序为准，
  // 堆叠柱/环形图/明细表统一查这张表，避免因各自取数顺序不同导致同一模型跨图变色
  const colorMap = useMemo(() => {
    const m = new Map<string, string>()
    models.forEach((row, i) => m.set(modelName(row.model), PALETTE[i % PALETTE.length]))
    return m
  }, [models])

  // 期间汇总 KPI（前端由 trend 求和）
  const total = useMemo(() => trend.reduce((a, t) => ({
    requests: a.requests + t.requests,
    tokens: a.tokens + t.prompt_tokens + t.completion_tokens,
    cost: a.cost + t.cost_micros,
  }), { requests: 0, tokens: 0, cost: 0 }), [trend])

  // 堆叠柱状：daily_models 按日期透视成 { date, [模型名]: 指标值 }
  const stacked = useMemo(() => {
    const names = [...new Set(daily.map(r => modelName(r.model)))]
    const byDate = new Map<string, Record<string, number | string>>()
    for (const r of daily) {
      const row = byDate.get(r.stat_date) ?? { date: r.stat_date.slice(5) }
      const n = modelName(r.model)
      row[n] = ((row[n] as number) ?? 0) + metricValue(r, metric)
      byDate.set(r.stat_date, row)
    }
    return { names, rows: [...byDate.keys()].sort().map(k => byDate.get(k)!) }
  }, [daily, metric])

  // 环形图与明细表跟随同一指标；占比按当前指标计算
  const pie = useMemo(() => {
    const totalV = models.reduce((a, m) => a + metricValue(m, metric), 0)
    return models.map((m, i) => ({
      name: modelName(m.model),
      value: metricValue(m, metric),
      pct: totalV > 0 ? metricValue(m, metric) / totalV * 100 : 0,
      color: colorMap.get(modelName(m.model)) ?? PALETTE[i % PALETTE.length],
      row: m,
    }))
  }, [models, metric, colorMap])

  if (isError) return <div className="flex h-60 flex-col items-center justify-center gap-3 text-sm text-[var(--muted-foreground)]">
    <p>{t('加载失败')}</p>
    <div className="flex items-center gap-2">
      <Button variant="outline" onClick={() => navigate(backTo)}><ArrowLeft size={16} />{t('返回')}</Button>
      <Button variant="outline" onClick={() => refetch()}>{t('重试')}</Button>
    </div>
  </div>

  if (!data) return <div className="space-y-4">
    <Skeleton className="h-8 w-64" />
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">{Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="h-20" />)}</div>
    <Skeleton className="h-60 w-full" />
    <Skeleton className="h-72 w-full" />
  </div>

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <div className="flex items-center gap-2">
        <Button variant="ghost" onClick={() => navigate(backTo)}><ArrowLeft size={16} />{t('返回')}</Button>
        <h2 className="text-lg font-semibold">{title}</h2>
      </div>
      <Segmented options={[7, 14, 30].map(d => ({ label: `${d}天`, value: d }))} value={days} onChange={setDays} />
    </div>

    {/* 响应式：移动端 2 列、桌面 3 列。渠道页会多 3 个 KPI（共 6 个），桌面下正好两行三列，
        固定 grid-cols-3 会在窄屏把 6 张卡挤成三列、数值折行。 */}
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
      <Kpi label={t('期间请求数')} value={num(total.requests)} />
      <Kpi label={t('期间 Tokens')} value={num(total.tokens)} />
      <Kpi label={t('期间费用')} value={usd(total.cost)} />
      {extraKpis?.(data).map(k => <Kpi key={k.label} label={k.label} value={k.value} hint={k.hint} />)}
    </div>

    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">{t('Tokens 趋势')}</div>
        <ResponsiveContainer width="100%" height={220}>
          <LineChart data={trend}>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
            <XAxis dataKey="date" tick={{ fontSize: 11 }} />
            <YAxis tick={{ fontSize: 11 }} />
            <Tooltip contentStyle={tooltipStyle} />
            <Line type="monotone" dataKey="prompt_tokens" stroke={PALETTE[0]} name="输入" dot={false} />
            <Line type="monotone" dataKey="completion_tokens" stroke={PALETTE[1]} name="输出" dot={false} />
            <Line type="monotone" dataKey="cached_tokens" stroke={PALETTE[2]} name="缓存读" dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </Card>
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">{t('费用趋势（USD）')}</div>
        <ResponsiveContainer width="100%" height={220}>
          <LineChart data={trend}>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
            <XAxis dataKey="date" tick={{ fontSize: 11 }} />
            <YAxis tick={{ fontSize: 11 }} />
            <Tooltip contentStyle={tooltipStyle} />
            <Line type="monotone" dataKey="cost" stroke={PALETTE[2]} name="费用(USD)" dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </Card>
    </div>

    <Card className="p-4">
      <div className="mb-3 flex items-center gap-3">
        <span className="text-sm font-medium">{t('每日模型构成')}</span>
        <Segmented options={METRIC_OPTIONS} value={metric} onChange={setMetric} />
      </div>
      <ResponsiveContainer width="100%" height={260}>
        <BarChart data={stacked.rows}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis dataKey="date" tick={{ fontSize: 11 }} />
          <YAxis tick={{ fontSize: 11 }} />
          <Tooltip contentStyle={tooltipStyle} />
          <Legend wrapperStyle={{ fontSize: 12 }} />
          {stacked.names.map((n, i) => <Bar key={n} dataKey={n} stackId="m" fill={colorMap.get(n) ?? PALETTE[i % PALETTE.length]} />)}
        </BarChart>
      </ResponsiveContainer>
      {daily.length === 0 && <p className="py-6 text-center text-sm text-[var(--muted-foreground)]">{t('暂无数据')}</p>}
    </Card>

    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">模型占比 <span className="text-xs font-normal text-[var(--muted-foreground)]">（按{METRIC_OPTIONS.find(o => o.value === metric)!.label}）</span></div>
        <ResponsiveContainer width="100%" height={260}>
          <PieChart>
            <Pie data={pie} dataKey="value" nameKey="name" innerRadius={60} outerRadius={95} paddingAngle={2}>
              {pie.map(p => <Cell key={p.name} fill={p.color} />)}
            </Pie>
            <Tooltip contentStyle={tooltipStyle} />
            <Legend wrapperStyle={{ fontSize: 12 }} />
          </PieChart>
        </ResponsiveContainer>
        {models.length === 0 && <p className="py-6 text-center text-sm text-[var(--muted-foreground)]">{t('暂无数据')}</p>}
      </Card>
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">{t('模型明细')}</div>
        <Table head={<><Th>{t('模型')}</Th><Th>{t('请求数')}</Th><Th>{t('输入')}</Th><Th>{t('输出')}</Th><Th>费用</Th><Th>{t('占比')}</Th></>}>
          {pie.map(p => <tr key={p.name} className="hover:bg-[var(--muted)]">
            <Td><span className="mr-1.5 inline-block h-2 w-2 rounded-full" style={{ background: p.color }} />{p.name}</Td>
            <Td className="tnum">{num(p.row.requests)}</Td>
            <Td className="tnum">{num(p.row.prompt_tokens)}</Td>
            <Td className="tnum">{num(p.row.completion_tokens)}</Td>
            <Td className="tnum">{usd(p.row.cost_micros)}</Td>
            <Td className="tnum">{p.pct.toFixed(1)}%</Td>
          </tr>)}
          {models.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无数据')}</Td></tr>}
        </Table>
      </Card>
    </div>
  </div>
}

// —— 三条路由薄包装：标题名称走各自列表缓存解析，未命中回退 #id ——

export function AppUsageAnalysisPage() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  const { data: apps = [] } = useQuery({ queryKey: ['/admin/apps'], queryFn: () => api.get<{ id: number; name: string }[]>('/admin/apps') })
  const name = apps.find(a => a.id === Number(id))?.name ?? `#${id}`
  return <AnalysisView title={`${t('用量分析')} · ${name}`} backTo="/apps" keyBase={`/admin/apps/${id}/usage-analysis`}
    fetcher={d => api.get<UsageAnalysisData>(`/admin/apps/${id}/usage-analysis?days=${d}`)} />
}

export function KeyUsageAnalysisPage() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  // 与令牌页共用同一 queryKey，通常直接命中缓存
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/api-keys', { tag_id: 0 }], queryFn: () => api.get<{ key: { id: number; name: string } }[]>('/admin/api-keys') })
  const name = rows.find(r => r.key.id === Number(id))?.key.name ?? `#${id}`
  return <AnalysisView title={`${t('令牌')} · ${t('用量分析')} · ${name}`} backTo="/api-keys" keyBase={`/admin/api-keys/${id}/usage-analysis`}
    fetcher={d => api.get<UsageAnalysisData>(`/admin/api-keys/${id}/usage-analysis?days=${d}`)} />
}

export function PortalAppUsageAnalysisPage() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  const { data: apps = [] } = useQuery({ queryKey: ['/portal/api/apps'], queryFn: () => portalApi.get<{ id: number; name: string }[]>('/portal/api/apps'), meta: { silentError: true } })
  const name = apps.find(a => a.id === Number(id))?.name ?? `#${id}`
  return <AnalysisView title={`${t('用量分析')} · ${name}`} backTo="/portal" keyBase={`/portal/api/apps/${id}/usage-analysis`}
    fetcher={d => portalApi.get<UsageAnalysisData>(`/portal/api/apps/${id}/usage-analysis?days=${d}`)} />
}

export function ChannelUsageAnalysisPage() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/channels'], queryFn: () => api.get<{ id: number; name: string }[]>('/admin/channels') })
  const name = rows.find(r => r.id === Number(id))?.name ?? `#${id}`
  return <AnalysisView title={`${t('渠道')} · ${t('用量分析')} · ${name}`} backTo="/channels" keyBase={`/admin/channels/${id}/usage-analysis`}
    // 取 ChannelUsageAnalysisData（字段是 UsageAnalysisData 的超集），TS 结构类型下可直接
    // 赋给 fetcher 要求的 Promise<UsageAnalysisData>，运行时拿到的仍是含 errors/延迟的完整数据。
    fetcher={d => api.get<ChannelUsageAnalysisData>(`/admin/channels/${id}/usage-analysis?days=${d}`)}
    extraKpis={(data) => {
      // 渠道额外指标：失败数、错误率、平均延迟。
      // ⚠️ 平均延迟必须按请求数加权——trend 每行存的是当天均值，直接求平均是「平均的平均」，
      // 各天请求数悬殊时会失真（与后端 sumColsChannel 同一口径）。
      const trend = data.trend as ChannelTrendRow[]
      const reqs = trend.reduce((a, r) => a + r.requests, 0)
      const errs = trend.reduce((a, r) => a + (r.errors ?? 0), 0)
      const totalCalls = reqs + errs
      // 三个 KPI 对「零」的处理必须一致：只有「没有数据」才显示 —，
      // 值确实为 0（如全部成功、亚毫秒延迟）要如实显示 0，否则会被读成「无数据」。
      const lat = reqs > 0 ? Math.round(trend.reduce((a, r) => a + (r.avg_latency_ms ?? 0) * r.requests, 0) / reqs) : 0
      // ⚠️ 文案必须如实：这里的失败数只含「打到上游且拿到非重试型 4xx」的请求。
      // 上游 5xx/429/403 属可重试，派发会 failover 到其他渠道、请求日志只记最终成功的那条；
      // 全候选失败时 channel_id=0 不归属任何渠道、被聚合过滤掉。故渠道真实健康要看熔断状态与仪表盘。
      const upstreamHint = '仅含打到本渠道且返回非重试型 4xx（如 400/401/404）的请求。上游 5xx/429/403 会被重试到其他渠道、只记最终成功的那条；全部候选失败时不归属任何渠道。渠道真实健康请看熔断状态与仪表盘。'
      return [
        { label: t('客户端错误数'), value: totalCalls > 0 ? num(errs) : '—', hint: upstreamHint },
        { label: t('客户端错误率'), value: totalCalls > 0 ? `${((errs / totalCalls) * 100).toFixed(2)}%` : '—', hint: upstreamHint },
        { label: t('平均延迟'), value: reqs > 0 ? `${num(lat)} ms` : '—', hint: '仅统计成功请求，按各日请求数加权。' },
      ]
    }} />
}
