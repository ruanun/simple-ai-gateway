import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Turnstile } from './Turnstile'

type TestResult = { ok: boolean; secret_only: boolean; message: string; hostname?: string; error_codes?: string[] }

// TurnstileTestDialog 用表单里（可未保存）的 Site Key 渲染组件，完成验证后连同 Secret 交后端真实调一次 siteverify，
// 一次覆盖 Site Key、域名白名单、Secret 及二者配对。组件加载不出来时（多为域名未加白）可退而只校验 Secret。
export function TurnstileTestDialog({ open, onClose, siteKey, secret }: {
  open: boolean
  onClose: () => void
  siteKey: string
  secret: string
}) {
  const [nonce, setNonce] = useState(0) // 自增即重挂载组件，重新测试
  const [result, setResult] = useState<TestResult | null>(null)

  const testM = useMutation({
    mutationFn: (token: string) => api.post<TestResult>('/admin/settings/turnstile-test', { secret, token }),
    meta: { silentError: true }, // 结果在弹窗内就地展示
    onSuccess: setResult,
    onError: (e) => setResult({ ok: false, secret_only: false, message: e instanceof Error ? e.message : '测试失败' }),
  })

  function retry() {
    setResult(null)
    setNonce(n => n + 1)
  }

  function close() {
    setResult(null)
    onClose()
  }

  return (
    <Dialog open={open} onClose={close} title="测试 Cloudflare Turnstile 配置">
      <div className="space-y-4">
        <p className="text-sm text-[var(--muted-foreground)]">
          完成下方验证后自动提交校验，使用的是当前表单里的 Site Key 与 Secret Key（无需先保存）。
          若组件报错或加载不出来，通常是当前访问域名 <code>{location.hostname}</code> 未加入该站点的域名白名单。
        </p>
        {open && (
          <Turnstile key={nonce} siteKey={siteKey} onToken={t => { if (t && !testM.isPending) testM.mutate(t) }} />
        )}
        {testM.isPending && <div className="text-sm text-[var(--muted-foreground)]">校验中…</div>}
        {result && (
          <div className={`rounded-md border px-3 py-2 text-sm ${result.ok
            ? 'border-green-200 bg-green-50 text-green-800 dark:border-green-900 dark:bg-green-950/30 dark:text-green-300'
            : 'border-red-200 bg-red-50 text-[var(--destructive)] dark:border-red-900 dark:bg-red-950/30'}`}>
            <div>{result.ok ? '✓ ' : '✗ '}{result.message}</div>
            {result.hostname && <div className="mt-1 text-xs opacity-80">验证所在域名：{result.hostname}</div>}
            {!result.ok && result.error_codes && result.error_codes.length > 0 && (
              <div className="mt-1 text-xs opacity-80">错误码：{result.error_codes.join(', ')}</div>
            )}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" disabled={testM.isPending} onClick={() => { setResult(null); testM.mutate('') }}>
            仅校验 Secret
          </Button>
          <Button variant="outline" disabled={testM.isPending} onClick={retry}>重新测试</Button>
          <Button onClick={close}>关闭</Button>
        </div>
      </div>
    </Dialog>
  )
}
