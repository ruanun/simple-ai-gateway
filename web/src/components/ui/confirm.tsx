import { useEffect, useRef, useState } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from './alert-dialog'
import { Input } from './input'
import { useI18n } from '../../lib/i18n'

// Promise 化的全局确认/输入对话框，替代 window.confirm / window.prompt：
//   if (!(await confirmDialog('删除渠道「x」？'))) return
//   const v = await promptDialog({ title: '设置倍率', defaultValue: '1' }); if (v === null) return
// 需在应用根部挂载一次 <ConfirmHost />。

export type ConfirmOptions = {
  title: string
  description?: string
  confirmText?: string
  cancelText?: string
  /** 危险操作：确认按钮红色 */
  destructive?: boolean
}
export type PromptOptions = ConfirmOptions & { defaultValue?: string; placeholder?: string }

type Pending =
  | { id: number; kind: 'confirm'; opts: ConfirmOptions; resolve: (ok: boolean) => void }
  | { id: number; kind: 'prompt'; opts: PromptOptions; resolve: (v: string | null) => void }

let enqueue: ((p: Omit<Pending, 'id'>) => void) | null = null

export function confirmDialog(opts: string | ConfirmOptions): Promise<boolean> {
  const o = typeof opts === 'string' ? { title: opts } : opts
  return new Promise((resolve) => {
    // Host 未挂载时兜底原生 confirm，保证功能不丢
    if (!enqueue) { resolve(window.confirm(o.title)); return }
    enqueue({ kind: 'confirm', opts: o, resolve })
  })
}

// confirmDelete 删除确认的统一入口（destructive+「删除」按钮文案固定），7 个管理页此前逐字复制。
export function confirmDelete(title: string, description?: string) {
  return confirmDialog({ title, description, destructive: true, confirmText: '删除' })
}

export function promptDialog(opts: string | PromptOptions): Promise<string | null> {
  const o = typeof opts === 'string' ? { title: opts } : opts
  return new Promise((resolve) => {
    if (!enqueue) { resolve(window.prompt(o.title, o.defaultValue ?? '')); return }
    enqueue({ kind: 'prompt', opts: o, resolve })
  })
}

let nextID = 1

export function ConfirmHost() {
  const { t } = useI18n()
  const [queue, setQueue] = useState<Pending[]>([])
  // prompt 输入框用非受控 + 按请求 id 换 key，避免 effect 里 setState
  const inputRef = useRef<HTMLInputElement>(null)
  const cur = queue[0]

  useEffect(() => {
    enqueue = (p) => setQueue((q) => [...q, { ...p, id: nextID++ } as Pending])
    return () => { enqueue = null }
  }, [])

  function settle(ok: boolean) {
    if (!cur) return
    if (cur.kind === 'confirm') cur.resolve(ok)
    else cur.resolve(ok ? (inputRef.current?.value ?? '') : null)
    setQueue((q) => q.slice(1))
  }

  return (
    <AlertDialog open={!!cur} onOpenChange={(open) => { if (!open) settle(false) }}>
      {cur && (
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{cur.opts.title}</AlertDialogTitle>
            {cur.opts.description && <AlertDialogDescription>{cur.opts.description}</AlertDialogDescription>}
          </AlertDialogHeader>
          {cur.kind === 'prompt' && (
            <Input
              key={cur.id}
              ref={inputRef}
              autoFocus
              defaultValue={cur.opts.defaultValue ?? ''}
              placeholder={cur.opts.placeholder}
              onKeyDown={(e) => { if (e.key === 'Enter') settle(true) }}
            />
          )}
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => settle(false)}>{cur.opts.cancelText ?? t('取消')}</AlertDialogCancel>
            <AlertDialogAction
              className={cur.opts.destructive ? 'bg-[var(--destructive)] text-white' : undefined}
              onClick={() => settle(true)}
            >
              {cur.opts.confirmText ?? t('确定')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      )}
    </AlertDialog>
  )
}
