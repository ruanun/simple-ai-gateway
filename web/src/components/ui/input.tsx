import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

export function Input({ className, ...p }: ComponentProps<'input'>) {
  return <input className={cn('w-full rounded-md border border-[var(--border)] bg-[var(--card)] px-3 py-1.5 text-sm outline-none focus:ring-2 focus:ring-[var(--ring)]', className)} {...p} />
}
