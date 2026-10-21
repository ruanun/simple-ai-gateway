import type { CSSProperties } from 'react'
import { microsToUsd } from './format'

// 仪表盘/用量分析共用的指标枚举与取值（此前两页各写一份、函数体逐字相同）
export type Metric = 'tokens' | 'cost' | 'requests'

export const METRIC_OPTIONS: { value: Metric; label: string }[] = [
  { value: 'tokens', label: 'Tokens' },
  { value: 'cost', label: '费用' },
  { value: 'requests', label: '请求数' },
]

// 指标取值：tokens=prompt+completion；cost 换算 USD；requests 原值
export function metricValue(r: { prompt_tokens: number; completion_tokens: number; cost_micros: number; requests: number }, m: Metric): number {
  return m === 'tokens' ? r.prompt_tokens + r.completion_tokens : m === 'cost' ? microsToUsd(r.cost_micros) : r.requests
}

// recharts Tooltip 统一样式（跟随站点 CSS 变量，亮暗两态自适配）
export const tooltipStyle: CSSProperties = { background: 'var(--card)', border: '1px solid var(--border)', borderRadius: 8, fontSize: 12 }

// 多系列图表配色（首二色=站点主蓝/强调琥珀，其余按区分度排布）
export const PALETTE = ['#1E40AF', '#D97706', '#059669', '#7C3AED', '#DC2626', '#0891B2', '#DB2777', '#65A30D', '#475569', '#EA580C']
