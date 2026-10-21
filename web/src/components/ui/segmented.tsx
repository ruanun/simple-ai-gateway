import { cn } from '../../lib/utils'

type Option<T> = { label: string; value: T }

type Props<T extends string | number> = {
  options: Option<T>[]
  value: T
  onChange: (v: T) => void
  className?: string
}

// 分段切换控件：一组互斥小按钮，选中项主蓝高亮（仪表盘时间范围/趋势弹窗天数等共用）
export function Segmented<T extends string | number>({ options, value, onChange, className }: Props<T>) {
  return <div className={cn('flex gap-1', className)}>
    {options.map(o => (
      <button key={String(o.value)}
        className={`rounded px-2 py-0.5 text-xs ${value === o.value ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'border border-[var(--border)] hover:bg-[var(--muted)]'}`}
        onClick={() => onChange(o.value)}>{o.label}</button>
    ))}
  </div>
}
