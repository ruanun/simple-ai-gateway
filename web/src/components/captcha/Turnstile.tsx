import { useEffect, useRef, useState } from 'react'

// Cloudflare Turnstile 组件（显式渲染模式）。脚本全局只加载一次；需要重置时由父组件换 key 重挂载。
type TurnstileApi = {
  render: (el: HTMLElement, opts: Record<string, unknown>) => string
  remove: (id: string) => void
}
declare global {
  interface Window { turnstile?: TurnstileApi }
}

const SCRIPT_SRC = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
let scriptPromise: Promise<void> | null = null

function loadScript(): Promise<void> {
  if (window.turnstile) return Promise.resolve()
  if (!scriptPromise) {
    scriptPromise = new Promise((resolve, reject) => {
      const s = document.createElement('script')
      s.src = SCRIPT_SRC
      s.async = true
      s.onload = () => resolve()
      s.onerror = () => { scriptPromise = null; reject(new Error('turnstile 脚本加载失败')) }
      document.head.appendChild(s)
    })
  }
  return scriptPromise
}

// onToken 在验证通过时回传 token；过期或出错时回传空串，父组件据此拦截提交。
export function Turnstile({ siteKey, onToken }: { siteKey: string; onToken: (token: string) => void }) {
  const boxRef = useRef<HTMLDivElement>(null)
  const onTokenRef = useRef(onToken)
  const [loadErr, setLoadErr] = useState(false)

  useEffect(() => { onTokenRef.current = onToken }, [onToken])

  useEffect(() => {
    let widgetId: string | undefined
    let cancelled = false
    loadScript().then(() => {
      if (cancelled || !boxRef.current || !window.turnstile) return
      widgetId = window.turnstile.render(boxRef.current, {
        sitekey: siteKey,
        theme: 'auto',
        callback: (t: string) => onTokenRef.current(t),
        'expired-callback': () => onTokenRef.current(''),
        'error-callback': () => onTokenRef.current(''),
      })
    }).catch(() => { if (!cancelled) setLoadErr(true) })
    return () => {
      cancelled = true
      if (widgetId && window.turnstile) window.turnstile.remove(widgetId)
    }
  }, [siteKey])

  if (loadErr) {
    return <div className="text-sm text-[var(--destructive)]">人机验证组件加载失败，请检查网络后刷新页面</div>
  }
  return <div ref={boxRef} className="flex justify-center min-h-[65px]" />
}
