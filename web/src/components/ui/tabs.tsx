import * as TabsPrimitive from '@radix-ui/react-tabs'
import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

// shadcn/ui Tabs（基于 Radix）：方向键导航、role=tablist/tab/tabpanel 语义完整；
// 视觉保留原有下划线风格（激活项主色下边框）而非 shadcn 默认药丸底

const Tabs = TabsPrimitive.Root

function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return <TabsPrimitive.List className={cn('flex gap-1 border-b border-[var(--border)]', className)} {...props} />
}

function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        '-mb-px cursor-pointer whitespace-nowrap border-b-2 border-transparent px-3 py-1.5 text-sm text-[var(--muted-foreground)] transition-colors hover:text-[var(--foreground)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]',
        'data-[state=active]:border-[var(--primary)] data-[state=active]:font-medium data-[state=active]:text-[var(--primary)]',
        className,
      )}
      {...props}
    />
  )
}

function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn('pt-3 outline-none', className)} {...props} />
}

export { Tabs, TabsList, TabsTrigger, TabsContent }
