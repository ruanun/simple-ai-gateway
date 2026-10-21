import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import type { AdminApiKey as Key } from '../../lib/types'
import type { Tag } from './types'
import { useI18n } from '../../lib/i18n'

type Props = {
  keyItem: Key | null // 目标令牌，非空即视为打开
  onClose: () => void
  keyTags: Tag[] // 已绑标签（父组件从列表数据派生）
  allTags: Tag[] // 全部标签
  onBind: (tagId: number) => void
  onUnbind: (tagId: number) => void
  bindPending: boolean
  unbindPending: boolean
}

// 标签绑定弹窗：已绑标签点击解绑、可绑标签点击绑定，数据均由父组件派生传入
export function KeyTagsDialog({ keyItem, onClose, keyTags, allTags, onBind, onUnbind, bindPending, unbindPending }: Props) {
  const { t } = useI18n()
  const pending = bindPending || unbindPending
  return <Dialog open={!!keyItem} onClose={onClose} title={`${t('令牌标签')} ${keyItem?.name || ''}`}>
    {keyItem && <div className="space-y-3">
      <div>
        <div className="mb-1 text-xs text-[var(--muted-foreground)]">{t('已绑标签（点击解绑）')}</div>
        <div className="flex flex-wrap gap-1">
          {keyTags.map(t => <button key={t.id} disabled={pending} className="rounded bg-[var(--primary)] px-2 py-0.5 text-xs text-[var(--primary-foreground)]" onClick={() => onUnbind(t.id)}>{t.name} ✕</button>)}
          {keyTags.length === 0 && <span className="text-xs text-[var(--muted-foreground)]">{t('无')}</span>}
        </div>
      </div>
      <div>
        <div className="mb-1 text-xs text-[var(--muted-foreground)]">{t('可绑标签（点击绑定）')}</div>
        <div className="flex flex-wrap gap-1">
          {allTags.filter(t => !keyTags.some(kt => kt.id === t.id)).map(t => <button key={t.id} disabled={pending} className="rounded border border-[var(--border)] px-2 py-0.5 text-xs hover:bg-[var(--muted)]" onClick={() => onBind(t.id)}>{t.name}</button>)}
        </div>
      </div>
      <div className="flex justify-end"><Button onClick={onClose}>{t('关闭')}</Button></div>
    </div>}
  </Dialog>
}
