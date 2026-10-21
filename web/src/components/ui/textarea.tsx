import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

// shadcn 风格多行文本框：与 Input 同一套边框/焦点环样式（底色透明，随所在容器）
export function Textarea({ className, ...p }: ComponentProps<'textarea'>) {
  return <textarea className={cn('w-full rounded-md border border-[var(--border)] bg-transparent px-2 py-1.5 text-sm outline-none focus:ring-2 focus:ring-[var(--ring)]', className)} {...p} />
}
