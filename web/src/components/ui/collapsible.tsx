import * as CollapsiblePrimitive from '@radix-ui/react-collapsible'
import { ChevronRight } from 'lucide-react'
import type { ComponentProps } from 'react'
import { cn } from '../../lib/utils'

// shadcn/ui Collapsible（基于 Radix）：替代原生 <details>，触发器带旋转箭头指示

const Collapsible = CollapsiblePrimitive.Root
const CollapsibleContent = CollapsiblePrimitive.Content

function CollapsibleTrigger({ className, children, ...props }: ComponentProps<typeof CollapsiblePrimitive.Trigger>) {
  return (
    <CollapsiblePrimitive.Trigger
      className={cn('group flex w-full cursor-pointer items-center gap-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]', className)}
      {...props}
    >
      <ChevronRight className="h-3.5 w-3.5 shrink-0 transition-transform group-data-[state=open]:rotate-90" />
      {children}
    </CollapsiblePrimitive.Trigger>
  )
}

export { Collapsible, CollapsibleTrigger, CollapsibleContent }
