import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from '../../lib/utils'

// shadcn/ui Dialog（基于 Radix）：焦点陷阱、Esc 关闭、滚动锁定、aria 语义完整；样式接站点 CSS 变量

const DialogRoot = DialogPrimitive.Root
const DialogTrigger = DialogPrimitive.Trigger
const DialogPortal = DialogPrimitive.Portal
const DialogClose = DialogPrimitive.Close

function DialogOverlay({ className, ...props }: ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      className={cn(
        'fixed inset-0 z-50 bg-black/50 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0',
        className,
      )}
      {...props}
    />
  )
}

function DialogContent({ className, children, ...props }: ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        // 包装器用法无描述文本，显式置空避免 Radix 的 aria-describedby 告警
        aria-describedby={undefined}
        className={cn(
          'fixed left-1/2 top-1/2 z-50 max-h-[85vh] w-full max-w-md -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-lg border border-[var(--border)] bg-[var(--card)] p-5 text-[var(--foreground)] shadow-xl outline-none duration-200',
          'data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95',
          className,
        )}
        {...props}
      >
        {children}
      </DialogPrimitive.Content>
    </DialogPortal>
  )
}

function DialogTitle({ className, ...props }: ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title className={cn('text-lg font-semibold', className)} {...props} />
}

function DialogDescription({ className, ...props }: ComponentProps<typeof DialogPrimitive.Description>) {
  return <DialogPrimitive.Description className={cn('text-sm text-[var(--muted-foreground)]', className)} {...props} />
}

export { DialogRoot, DialogTrigger, DialogPortal, DialogClose, DialogOverlay, DialogContent, DialogTitle, DialogDescription }

// —— 兼容包装器：保持全站既有受控用法 <Dialog open onClose title width> 不变 ——
export function Dialog({ open, onClose, title, children, width = 'max-w-md' }: {
  open: boolean
  onClose: () => void
  title: ReactNode
  children: ReactNode
  width?: string
}) {
  return (
    <DialogRoot open={open} onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className={width}>
        <DialogTitle className="mb-4">{title}</DialogTitle>
        {children}
      </DialogContent>
    </DialogRoot>
  )
}

// 供 Drawer 复用的关闭按钮（Radix Close，自动带关闭语义）
export function DialogCloseX({ className }: { className?: string }) {
  return (
    <DialogPrimitive.Close
      className={cn('cursor-pointer rounded-sm text-[var(--muted-foreground)] transition hover:text-[var(--foreground)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]', className)}
    >
      <X className="h-4 w-4" />
      <span className="sr-only">关闭</span>
    </DialogPrimitive.Close>
  )
}
