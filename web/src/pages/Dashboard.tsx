import { memo, useMemo, useState } from 'react'
import type { ComponentType } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usd, num, microsToUsd } from '../lib/format'
import type { DashChannelToday, DashData, DashTop, DashTopApp, DashTrend } from '../lib/types'
import { Card } from '../components/ui/card'
import { Skeleton } from '../components/ui/skeleton'
import { RefreshButton } from '../components/ui/refresh'
import { Segmented } from '../components/ui/segmented'
import { Table, Th, Td } from '../components/ui/table'
import { Pill } from '../components/ui/badge'
import { useBreakerStatus } from '../lib/breaker'
import { Activity, Coins, Hash, AppWindow, Sparkles, AlertTriangle, Timer } from 'lucide-react'
import { AreaChart, Area, BarChart, Bar, Cell, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from 'recharts'
import { type Metric, METRIC_OPTIONS, metricValue, tooltipStyle } from '../lib/metrics'
import { useI18n } from '../lib/i18n'

// 长模型名/应用名中段截断：首尾都是区分信息（系列名/日期后缀），完整名悬停 Tooltip 可见
const truncModel = (s: string) => (s.length > 20 ? `${s.slice(0, 10)}…${s.slice(-9)}` : s)

function Stat({ icon: Icon, label, value, sub }: { icon: ComponentType<{ size?: number }>; label: string; value: string; sub?: string }) {
  return <Card className="p-4">
    <div className="flex items-center gap-2 text-[var(--muted-foreground)]"><Icon size={16} /><span className="text-xs">{label}</span></div>
    <div className="mt-2 text-2xl font-semibold tnum">{value}</div>
    {sub && <div className="mt-1 text-xs text-[var(--muted-foreground)] tnum">{sub}</div>}
  </Card>
}

const hourOptions = [{ label: '6小时', value: 6 }, { label: '24小时', value: 24 }, { label: '3天', value: 72 }, { label: '7天', value: 168 }]

// 渲染顺序与 METRIC_OPTIONS 不同（请求数在前），保留本地数组
const rankMetricOptions: { label: string; value: Metric }[] = [
  { label: '请求数', value: 'requests' }, { label: 'Tokens', value: 'tokens' }, { label: '费用', value: 'cost' },
]

// 图表数据视图型：在契约行上派生展示字段（hour/cost 由 stat_hour/cost_micros 换算）
type TrendPoint = DashTrend & { hour: string; cost: number }
type TopPoint = DashTop & { value: number }

// 图表拆为 memo 组件：熔断状态 10s 轮询触发整页重渲时，三张图的数据与指标引用均不变，整体跳过重绘
const TrendChart = memo(function TrendChart({ data, metric, t }: { data: TrendPoint[]; metric: Metric; t: (k: string) => string }) {
  return <ResponsiveContainer width="100%" height={240}>
    <AreaChart data={data}>
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
      <XAxis dataKey="hour" tick={{ fontSize: 11 }} />
      <YAxis tick={{ fontSize: 11 }} />
      <Tooltip contentStyle={tooltipStyle} />
      {metric === 'tokens' && <>
        <Area type="monotone" dataKey="prompt_tokens" stroke="var(--primary)" fill="var(--primary)" fillOpacity={0.2} name={t('输入 Tokens')} />
        <Area type="monotone" dataKey="completion_tokens" stroke="var(--accent)" fill="var(--accent)" fillOpacity={0.2} name={t('输出 Tokens')} />
        <Area type="monotone" dataKey="cached_tokens" stroke="var(--ok)" fill="var(--ok)" fillOpacity={0.15} name={t('缓存读')} />
      </>}
      {metric === 'cost' && <Area type="monotone" dataKey="cost" stroke="var(--ok)" fill="var(--ok)" fillOpacity={0.2} name={t('费用(USD)')} />}
      {metric === 'requests' && <Area type="monotone" dataKey="requests" stroke="var(--primary)" fill="var(--primary)" fillOpacity={0.2} name={t('请求数')} />}
    </AreaChart>
  </ResponsiveContainer>
})

const TopModelsChart = memo(function TopModelsChart({ data, metric, t }: { data: TopPoint[]; metric: Metric; t: (k: string) => string }) {
  return <ResponsiveContainer width="100%" height={288}>
    <BarChart data={data} layout="vertical">
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
      <XAxis type="number" tick={{ fontSize: 11 }} />
      {/* interval=0 强制渲染全部类目刻度：默认自动抽稀在类目少时仍可能吃掉第一名的模型名 */}
      <YAxis type="category" dataKey="model" width={140} tick={{ fontSize: 11 }} tickFormatter={truncModel} interval={0} />
      <Tooltip contentStyle={tooltipStyle} />
      {/* 按名次做主色深浅层次：第一名最实，逐名递减（下限 0.35 保证可读） */}
      <Bar dataKey="value" fill="var(--primary)" name={t(rankMetricOptions.find(o => o.value === metric)!.label)}>
        {data.map((_, i) => <Cell key={i} fillOpacity={Math.max(1 - i * 0.11, 0.35)} />)}
      </Bar>
    </BarChart>
  </ResponsiveContainer>
})

const TopAppsChart = memo(function TopAppsChart({ data, t }: { data: DashTopApp[]; t: (k: string) => string }) {
  return <ResponsiveContainer width="100%" height={288}>
    <BarChart data={data} layout="vertical">
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
      <XAxis type="number" tick={{ fontSize: 11 }} />
      <YAxis type="category" dataKey="name" width={140} tick={{ fontSize: 11 }} tickFormatter={truncModel} interval={0} />
      <Tooltip contentStyle={tooltipStyle} />
      <Bar dataKey="requests" fill="var(--accent)" name={t('请求数')}>
        {data.map((_, i) => <Cell key={i} fillOpacity={Math.max(1 - i * 0.11, 0.35)} />)}
      </Bar>
    </BarChart>
  </ResponsiveContainer>
})

// top_apps 空值兜底用模块级常量：内联 [] 每次渲染换新引用会让 TopAppsChart 的 memo 失效
const EMPTY_TOP_APPS: DashTopApp[] = []

export function Dashboard() {
  const qc = useQueryClient()
  const { t } = useI18n()
  const [hours, setHours] = useState(24)
  const [trendMetric, setTrendMetric] = useState<Metric>('tokens')
  const [rankMetric, setRankMetric] = useState<Metric>('requests')
  const { data: brk } = useBreakerStatus()
  // hours 进 queryKey：切换时间范围自动按新 key 重新请求
  const { data: d } = useQuery({ queryKey: ['/admin/dashboard', hours], queryFn: () => api.get<DashData>(`/admin/dashboard?hours=${hours}`) })
  // 派生数据 memo 化（必须在 if (!d) 早退之前，hooks 不可条件调用）：熔断状态 10s 轮询
  // （useBreakerStatus）触发整页重渲时，与轮询无关的派生数组保持引用稳定，上方 memo 图表得以跳过重绘。
  // 趋势按 stat_hour 升序（后端 desc，反转）；无数据时后端可能返回 null
  const trend = useMemo<TrendPoint[]>(() => [...(d?.trend ?? [])].reverse().map(t => ({ ...t, hour: t.stat_hour.slice(5), cost: microsToUsd(t.cost_micros) })), [d])
  // 模型排行预计算 value：随 rankMetric 切换排行依据（请求数/Tokens/费用）
  const top = useMemo<TopPoint[]>(() => (d?.top ?? []).map(t => ({ ...t, value: metricValue(t, rankMetric) })), [d, rankMetric])
  const brkMap = useMemo(() => new Map((brk?.channels ?? []).map(c => [c.id, c.state])), [brk])
  // 渠道健康：熔断中 > 半开 > 正常排序，同级按错误数降序，异常渠道优先可见
  const channels = useMemo(() => [...(d?.channels ?? [])].sort((a, b) => {
    const w = (x: DashChannelToday) => brkMap.get(x.channel_id) === 'open' ? 0 : brkMap.get(x.channel_id) === 'half' ? 1 : 2
    return w(a) - w(b) || b.errors - a.errors
  }), [d, brkMap])
  // 首次加载骨架屏：占位结构（7 卡 KPI + 两行图表卡）与真实布局一致，避免切换加载态时页面高度跳动
  if (!d) return <div className="space-y-5">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">仪表盘</h2>
      <Skeleton className="h-8 w-20" />
    </div>
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      {Array.from({ length: 7 }, (_, i) => (
        <Card key={i} className="p-4">
          <Skeleton className="h-4 w-20" />
          <Skeleton className="mt-2 h-8 w-24" />
          <Skeleton className="mt-1 h-3 w-28" />
        </Card>
      ))}
    </div>
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4"><Skeleton className="h-4 w-24" /><Skeleton className="mt-3 h-60 w-full" /></Card>
      <Card className="p-4"><Skeleton className="h-4 w-24" /><Skeleton className="mt-3 h-72 w-full" /></Card>
    </div>
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4"><Skeleton className="h-4 w-24" /><Skeleton className="mt-3 h-72 w-full" /></Card>
      <Card className="p-4"><Skeleton className="h-4 w-24" /><Skeleton className="mt-3 h-60 w-full" /></Card>
    </div>
  </div>
  return <div className="space-y-5">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('仪表盘')} <span className="text-xs font-normal text-[var(--muted-foreground)]">{t('今日')}</span></h2>
      {/* 前缀失效：覆盖所有 hours 变体的缓存 */}
      <RefreshButton onRefresh={() => qc.invalidateQueries({ queryKey: ['/admin/dashboard'] })} />
    </div>
    {/* 口径说明：kpi.requests 来自聚合表 usage_daily，只含计费成功的请求；ops.requests 来自 request_logs 全量，
       含失败请求。两卡数字不同属预期——前者是"计费口径"，后者是"真实流量/错误率口径"。 */}
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <Stat icon={Activity} label={t('今日请求')} value={num(d.kpi.requests)} sub={`累计 ${num(d.kpi_total.requests)}`} />
      <Stat icon={AlertTriangle} label={t('今日错误率')} value={d.ops.requests > 0 ? `${(d.ops.errors / d.ops.requests * 100).toFixed(1)}%` : '0%'} sub={`错误 ${num(d.ops.errors)} / 共 ${num(d.ops.requests)}`} />
      <Stat icon={Timer} label={t('今日平均延迟')} value={`${num(d.ops.avg_latency_ms)} ms`} sub={`首 Token ${num(d.ops.avg_first_token_ms)} ms`} />
      <Stat icon={Hash} label={t('今日 Tokens')} value={num(d.kpi.prompt_tokens + d.kpi.completion_tokens)} sub={`累计 ${num(d.kpi_total.prompt_tokens + d.kpi_total.completion_tokens)}`} />
      <Stat icon={Coins} label={t('今日费用')} value={usd(d.kpi.cost_micros)} sub={`累计 ${usd(d.kpi_total.cost_micros)}`} />
      <Stat icon={Sparkles} label={t('今日缓存读')} value={num(d.kpi.cached_tokens)} sub={`累计 ${num(d.kpi_total.cached_tokens)}`} />
      <Stat icon={AppWindow} label={t('应用数')} value={num(d.app_count)} />
    </div>

    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4">
        <div className="mb-3 flex items-center gap-2">
          <span className="text-sm font-medium">{t('用量趋势')}</span>
          <Segmented options={hourOptions.map(o => ({ ...o, label: t(o.label) }))} value={hours} onChange={setHours} />
          <Segmented className="ml-auto" options={METRIC_OPTIONS} value={trendMetric} onChange={setTrendMetric} />
        </div>
        <TrendChart data={trend} metric={trendMetric} t={t} />
      </Card>
      <Card className="p-4">
        <div className="mb-3 flex items-center gap-2">
          <span className="text-sm font-medium">{t('模型用量排行')}</span>
          <Segmented className="ml-auto" options={rankMetricOptions.map(o => ({ ...o, label: t(o.label) }))} value={rankMetric} onChange={setRankMetric} />
        </div>
        <TopModelsChart data={top} metric={rankMetric} t={t} />
      </Card>
    </div>

    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">{t('应用用量排行')} <span className="text-xs font-normal text-[var(--muted-foreground)]">{t('（全时 · 请求数）')}</span></div>
        <TopAppsChart data={d.top_apps ?? EMPTY_TOP_APPS} t={t} />
      </Card>
      <Card className="p-4">
        <div className="mb-3 text-sm font-medium">{t('渠道健康')} <span className="text-xs font-normal text-[var(--muted-foreground)]">{t('（今日）')}</span></div>
        <Table head={<><Th>{t('渠道')}</Th><Th>{t('请求')}</Th><Th>{t('错误')}</Th><Th>{t('熔断状态')}</Th></>}>
          {channels.map(ch => {
            const st = brkMap.get(ch.channel_id)
            return <tr key={ch.channel_id} className="hover:bg-[var(--muted)]">
              <Td>{ch.name}</Td>
              <Td className="tnum">{num(ch.requests)}</Td>
              <Td className="tnum">{ch.errors > 0 ? <span className="text-[var(--down)]">{num(ch.errors)}</span> : '0'}</Td>
              <Td>{st === 'open' ? <Pill status="down">{t('熔断中')}</Pill> : st === 'half' ? <Pill status="warn">{t('半开')}</Pill> : <Pill status="ok">{t('正常')}</Pill>}</Td>
            </tr>
          })}
          {channels.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('今日暂无渠道流量')}</Td></tr>}
        </Table>
      </Card>
    </div>
  </div>
}
