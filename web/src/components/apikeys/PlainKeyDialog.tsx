import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Copy } from 'lucide-react'
import { useI18n } from '../../lib/i18n'

type Props = {
  plainKey: string | null // 非空即视为打开（创建成功后一次性展示）
  onClose: () => void
}

// 密钥明文显示弹窗：创建令牌成功后一次性展示，关闭后无法再次查看
export function PlainKeyDialog({ plainKey, onClose }: Props) {
  const { t } = useI18n()
  return <Dialog open={!!plainKey} onClose={onClose} title={t('令牌创建成功')}>
    <p className="mb-2 text-sm text-[var(--warn)]">{t('⚠ 密钥仅显示一次，关闭后无法再次查看')}</p>
    <div className="flex items-center gap-2 rounded-md border border-[var(--border)] bg-[var(--muted)] p-2">
      <code className="flex-1 break-all font-mono text-sm">{plainKey}</code>
      <Button variant="outline" onClick={() => plainKey && navigator.clipboard.writeText(plainKey)}><Copy size={15} /></Button>
    </div>
    <div className="mt-4 flex justify-end"><Button onClick={onClose}>{t('我已保存，关闭')}</Button></div>
  </Dialog>
}
