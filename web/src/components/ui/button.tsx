import { cva, type VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

// shadcn/ui 风格 Button（cva 变体）；variant 命名沿用站内既有 primary/outline/ghost/destructive
// whitespace-nowrap + shrink-0：防止紧凑 flex 行里按钮被压缩导致中文逐字竖排
export const buttonVariants = cva(
  'inline-flex shrink-0 cursor-pointer items-center gap-1.5 whitespace-nowrap rounded-md text-sm font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        primary: 'bg-[var(--primary)] text-[var(--primary-foreground)] hover:opacity-90',
        outline: 'border border-[var(--border)] hover:bg-[var(--muted)]',
        ghost: 'hover:bg-[var(--muted)]',
        destructive: 'bg-[var(--destructive)] text-white hover:opacity-90',
      },
      size: {
        default: 'px-3 py-1.5',
        sm: 'px-2 py-1 text-xs',
        icon: 'p-1.5',
      },
    },
    defaultVariants: { variant: 'primary', size: 'default' },
  },
)

export function Button({ variant, size, className, ...p }: ComponentProps<'button'> & VariantProps<typeof buttonVariants>) {
  return <button className={cn(buttonVariants({ variant, size }), className)} {...p} />
}
