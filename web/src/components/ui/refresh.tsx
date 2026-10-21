import { useState } from 'react'
import { RotateCw } from 'lucide-react'
import { Button } from './button'
import { Tip } from './tooltip'

// 页面级手动刷新按钮：点击调用页面的 load()，等待期间图标转圈并禁用防连点。
export function RefreshButton({ onRefresh }: { onRefresh: () => Promise<unknown> | void }) {
  const [busy, setBusy] = useState(false)
  return (
    <Tip content="刷新数据">
      <Button variant="outline" aria-label="刷新数据" disabled={busy}
        onClick={async () => { setBusy(true); try { await onRefresh() } finally { setBusy(false) } }}>
        <RotateCw size={14} className={busy ? 'animate-spin' : ''} />刷新
      </Button>
    </Tip>
  )
}
