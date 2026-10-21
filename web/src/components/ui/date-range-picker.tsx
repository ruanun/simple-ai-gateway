import { useState } from 'react'
import type { DateRange } from 'react-day-picker'
import { CalendarDays, X } from 'lucide-react'
import { Calendar } from './calendar'
import { Popover, PopoverContent, PopoverTrigger } from './popover'
import { cn } from '../../lib/utils'

// DateTimeRange：起止时间范围选择器（Popover + 双月 Calendar range + 时间微调），
// 替代两个原生 datetime-local。值语义与既有约定一致：'YYYY-MM-DDTHH:mm:ss' 本地时间字符串，空串=未设。

function pad(n: number) { return String(n).padStart(2, '0') }
function dateStr(d: Date) { return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` }
function toDate(s: string): Date | undefined { return s ? new Date(s.slice(0, 10) + 'T00:00:00') : undefined }
function timePart(s: string, def: string) { return s ? s.slice(11, 16) : def }
function fmtDisplay(s: string) { return s ? `${s.slice(5, 10)} ${s.slice(11, 16)}` : '…' }

export function DateTimeRange({ start, end, onChange, className }: {
  start: string
  end: string
  onChange: (start: string, end: string) => void
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const range: DateRange | undefined = start || end
    ? { from: toDate(start), to: toDate(end) }
    : undefined

  // 换日期保留已选时间；起点默认 00:00、终点默认 23:59
  function selectRange(r: DateRange | undefined) {
    if (!r?.from) { onChange('', ''); return }
    const s = `${dateStr(r.from)}T${timePart(start, '00:00')}:00`
    const e = r.to ? `${dateStr(r.to)}T${timePart(end, '23:59')}:59` : ''
    onChange(s, e)
  }
  function setStartTime(t: string) { if (start && t) onChange(start.slice(0, 11) + t + ':00', end) }
  function setEndTime(t: string) { if (end && t) onChange(start, end.slice(0, 11) + t + ':59') }

  const hasValue = !!(start || end)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={cn(
          'inline-flex cursor-pointer items-center gap-1.5 whitespace-nowrap rounded border border-[var(--border)] bg-[var(--card)] px-2 py-1 text-xs outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]',
          !hasValue && 'text-[var(--muted-foreground)]',
          className,
        )}
      >
        <CalendarDays className="h-3.5 w-3.5 shrink-0 opacity-60" />
        {hasValue ? <span className="tnum">{fmtDisplay(start)} ~ {fmtDisplay(end)}</span> : '起止时间'}
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align="end">
        <Calendar mode="range" numberOfMonths={2} selected={range} onSelect={selectRange} defaultMonth={range?.from} />
        <div className="flex items-center gap-2 border-t border-[var(--border)] px-3 py-2 text-xs">
          <input type="time" className="rounded border border-[var(--border)] bg-[var(--card)] px-1.5 py-0.5"
            value={timePart(start, '00:00')} disabled={!start} onChange={(e) => setStartTime(e.target.value)} />
          <span>~</span>
          <input type="time" className="rounded border border-[var(--border)] bg-[var(--card)] px-1.5 py-0.5"
            value={timePart(end, '23:59')} disabled={!end} onChange={(e) => setEndTime(e.target.value)} />
          {hasValue && (
            <button className="ml-auto inline-flex cursor-pointer items-center gap-0.5 text-[var(--destructive)] hover:underline"
              onClick={() => { onChange('', ''); setOpen(false) }}>
              <X className="h-3 w-3" />清除
            </button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
