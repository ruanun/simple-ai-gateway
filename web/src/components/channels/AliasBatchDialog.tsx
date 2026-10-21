import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Dialog } from '../ui/dialog'
import { Checkbox } from '../ui/checkbox'
import { Tip } from '../ui/tooltip'
import { toast } from '../ui/sonner'
import type { ModelPrice } from '../../lib/types'
import { useI18n } from '../../lib/i18n'

type Props = {
  open: boolean
  onClose: () => void
  // 目标渠道 id（别名路由边指向本渠道）
  channelId: number
  // 渠道模型名列表（由渠道模型弹窗显式传入，打开时快照）
  models: string[]
}

// 识别常见版本后缀（保守规则）：横杠日期(-2026-05-05) / 8位日期(-20260505) / 4位MMDD(-0125)
function stripVersion(m: string): string {
  return m.replace(/-\d{4}-\d{2}-\d{2}$/, '').replace(/-\d{8}$/, '').replace(/-\d{4}$/, '')
}

// 批量建规范名别名弹窗：Radix Dialog 关闭即卸载内容，每次打开时内容组件重新挂载并按模型列表构建建议行
export function AliasBatchDialog({ open, onClose, channelId, models }: Props) {
  const { t } = useI18n()
  return <Dialog open={open} onClose={onClose} title={t('批量建规范名别名')} width="max-w-2xl">
    <AliasBatchContent channelId={channelId} models={models} onClose={onClose} />
  </Dialog>
}

function AliasBatchContent({ channelId, models, onClose }: { channelId: number; models: string[]; onClose: () => void }) {
  const { t } = useI18n()
  // 挂载（弹窗打开）时构建：识别到版本后缀的默认勾选；其余行建议名=原名、默认不勾（可手改后再勾）
  const [aliasRows, setAliasRows] = useState(() => models.map(m => {
    const s = stripVersion(m)
    return { model: m, alias: s, checked: s !== m }
  }))
  // 全局已定价模型集合（与主页面/渠道模型弹窗共享同一查询缓存）
  const { data: allPrices = [] } = useQuery({ queryKey: ['/admin/prices'], queryFn: () => api.get<Pick<ModelPrice, 'model' | 'channel_id'>[]>('/admin/prices') })
  const pricedModels = useMemo(() => new Set(allPrices.filter(p => p.channel_id === 0).map(p => p.model)), [allPrices])

  const submitM = useMutation({
    mutationFn: (items: { alias: string; channel_id: number; model: string }[]) =>
      api.post<{ created: number; reused_alias: number; skipped: number }>('/admin/alias-routes/batch', { items }),
    onSuccess: (res) => {
      toast.success(`路由边新建 ${res.created} 条，复用已有别名 ${res.reused_alias} 个，跳过 ${res.skipped} 条（重复边）`)
      onClose()
    },
  })
  function submitAliasBatch() {
    const items = aliasRows.filter(r => r.checked && r.alias.trim() && r.alias.trim() !== r.model)
      .map(r => ({ alias: r.alias.trim(), channel_id: channelId, model: r.model }))
    if (items.length === 0) return
    submitM.mutate(items)
  }

  return <div className="space-y-3">
    <p className="text-xs text-[var(--muted-foreground)]">
      为带版本号的模型建「规范名 → (本渠道, 真实模型)」别名路由边；别名已存在时自动复用（追加为该别名的一条边）。
      计费按真实模型名，⚠未定价的目标模型请先配价。
    </p>
    <div className="max-h-[50vh] overflow-y-auto space-y-1">
      {aliasRows.map((r, i) => (
        <div key={r.model} className="flex items-center gap-2 text-sm px-1">
          <Checkbox checked={r.checked} onCheckedChange={() => {
            const rows = [...aliasRows]; rows[i] = { ...r, checked: !r.checked }; setAliasRows(rows)
          }} />
          <Tip content={r.model}><span className="tnum flex-1 truncate">{r.model}
            {!pricedModels.has(r.model) && <Tip content={t('目标真实模型未配全局价，请求会被拒绝(400)')}><span className="ml-1 text-xs text-[var(--destructive)]">⚠未定价</span></Tip>}
          </span></Tip>
          <span className="text-[var(--muted-foreground)]">→</span>
          <Input className="w-56" value={r.alias} onChange={e => {
            const rows = [...aliasRows]; rows[i] = { ...r, alias: e.target.value, checked: true }; setAliasRows(rows)
          }} />
        </div>
      ))}
    </div>
    <div className="flex items-center justify-between pt-1">
      <span className="text-xs text-[var(--muted-foreground)]">已勾选 {aliasRows.filter(r => r.checked && r.alias.trim() && r.alias.trim() !== r.model).length} 条（别名=原名的行不会提交）</span>
      <div className="flex gap-2">
        <Button variant="outline" onClick={onClose}>{t('取消')}</Button>
        <Button onClick={submitAliasBatch} disabled={aliasRows.every(r => !r.checked || !r.alias.trim() || r.alias.trim() === r.model) || submitM.isPending}>{t('创建')}</Button>
      </div>
    </div>
  </div>
}
