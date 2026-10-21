import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

export function Card({ className, ...p }: ComponentProps<'div'>) {
  return <div className={cn('rounded-lg border border-[var(--border)] bg-[var(--card)]', className)} {...p} />
}
