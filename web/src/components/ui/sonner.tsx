import { Toaster as Sonner, toast } from 'sonner'

// 全局 toast 通知（sonner）：样式接站点 CSS 变量，自动跟随明暗主题；替代 window.alert
export function Toaster() {
  return (
    <Sonner
      position="top-right"
      duration={3500}
      closeButton
      toastOptions={{
        style: {
          background: 'var(--card)',
          color: 'var(--foreground)',
          border: '1px solid var(--border)',
        },
      }}
    />
  )
}

export { toast }
