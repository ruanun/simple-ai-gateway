import { DayPicker } from 'react-day-picker'
import { zhCN } from 'react-day-picker/locale'
import 'react-day-picker/style.css'
import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

// shadcn/ui Calendar（基于 react-day-picker）：中文本地化、周一起始；
// 配色经 index.css 的 .rdp-root 变量覆盖接入站点主题
export function Calendar({ className, ...props }: ComponentProps<typeof DayPicker>) {
  return <DayPicker locale={zhCN} weekStartsOn={1} className={cn('p-2', className)} {...props} />
}
