// micros(USD 微量, 1 USD = 1e6) → "$0.0123"
export function usd(micros: number): string {
  return '$' + microsToUsd(micros).toLocaleString('en-US', { minimumFractionDigits: 4, maximumFractionDigits: 4 })
}
// USD 数值/字符串（表单输入）→ micros 整数：Math.round 收敛浮点误差，非法输入兜底 0
export function usdToMicros(v: number | string): number {
  const n = Number(v)
  return Number.isFinite(n) ? Math.round(n * 1e6) : 0
}
// micros → USD 数值（表单回填/图表换算用）
export function microsToUsd(m: number): number {
  return (m || 0) / 1e6
}
// ISO 时间串 → 本地时间显示（各列表「时间」列统一入口，口径同 toLocaleString）
export function fmtDateTime(iso: string): string {
  return new Date(iso).toLocaleString()
}
// 千分位整数
export function num(n: number): string {
  return (n || 0).toLocaleString('en-US')
}
// API 错误 → 用户可读文案（Error 取 message，其余兜底）
export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : '操作失败'
}
// 本地日历日 "YYYY-MM-DD"：时间快捷筛选统一本地时区口径（与 date-range-picker 一致）。
// 不能用 toISOString——它取 UTC 日，正时区（如 GMT+8）凌晨会整整错一天。
function localYMD(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
// 今天零点，格式 "YYYY-MM-DDT00:00:00"（日志/审计时间快捷筛选用）
export function todayStart(): string {
  return localYMD(new Date()) + 'T00:00:00'
}
// n 天前零点，格式同上
export function daysAgo(n: number): string {
  const d = new Date(); d.setDate(d.getDate() - n)
  return localYMD(d) + 'T00:00:00'
}
