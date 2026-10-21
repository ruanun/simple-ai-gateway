import { useState, useEffect } from 'react'
import { Badge } from '../ui/badge'
import type { BreakerState } from '../../lib/breaker'

// 熔断状态徽标 + 一键重置。state 缺失(正常)时不渲染任何内容。
// canReset=false（如 viewer 无写权限）时只展示徽标、不给重置按钮，避免死按钮。
export function BreakerBadge({ state, openUntil, canReset = true, onReset }: { state?: BreakerState; openUntil?: number; canReset?: boolean; onReset: () => void }) {
  const [remain, setRemain] = useState(0)

  useEffect(() => {
    if (state === 'open' && openUntil) {
      const updateRemain = () => {
        setRemain(Math.max(0, openUntil - Math.floor(Date.now() / 1000)))
      }
      updateRemain()
      const interval = setInterval(updateRemain, 1000)
      return () => clearInterval(interval)
    }
  }, [state, openUntil])

  if (!state) return null
  return (
    <span className="inline-flex items-center gap-1">
      {state === 'open'
        ? <Badge status="down">熔断{remain > 0 ? ` ${remain}s` : ''}</Badge>
        : <Badge status="warn">半开</Badge>}
      {canReset && <button className="text-xs text-[var(--primary)] hover:underline" onClick={onReset}>重置</button>}
    </span>
  )
}
