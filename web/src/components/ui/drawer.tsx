import * as DialogPrimitive from '@radix-ui/react-dialog'
import type { ReactNode } from 'react'
import { DialogCloseX, DialogOverlay, DialogPortal, DialogTitle } from './dialog'
import { cn } from '../../lib/utils'

// Drawer：右侧滑入抽屉（shadcn Sheet 交互），基于 Radix Dialog——焦点陷阱、Esc、滚动锁定齐全。
// 保持全站既有受控用法 <Drawer open onClose title width> 不变。
export function Drawer({
  open, onClose, title, children, width = 'w-[min(92vw,640px)]',
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  children: ReactNode
  width?: string
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogPortal>
        <DialogOverlay />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className={cn(
            'fixed right-0 top-0 z-50 flex h-full flex-col border-l border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] shadow-xl outline-none',
            'transition ease-in-out data-[state=open]:animate-in data-[state=open]:slide-in-from-right data-[state=open]:duration-300 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-right data-[state=closed]:duration-200',
            width,
          )}
        >
          <div className="flex items-center justify-between border-b border-[var(--border)] px-5 py-3">
            <DialogTitle className="text-base">{title}</DialogTitle>
            <DialogCloseX />
          </div>
          <div className="flex-1 overflow-auto p-5">{children}</div>
        </DialogPrimitive.Content>
      </DialogPortal>
    </DialogPrimitive.Root>
  )
}
