import { useState } from 'react'
import type { ReactNode } from 'react'
import { Input } from './ui/input'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { toggleInSet } from '../lib/utils'

// 多选清单块：搜索框过滤 + 计数 + 对过滤集全选/反选 + 动作按钮 + Checkbox 滚动列表。
// 此前 Prices（models.dev 同步建议）与 ChannelModelsDialog（拉取上游导入）各写一份同段 UI。
// 外层容器（边框/分隔线等）由调用方自持；搜索词为组件内部瞬态，随卸载自然清空。
// getKey 同时用作行 key、显示文案与搜索匹配目标（两个调用点均为模型名）。
export function CheckboxPickList<T>({ items, selected, onSelectedChange, getKey, renderExtra, countText, searchPlaceholder, actionText, actionDisabled, onAction, extraActions }: {
  items: T[]
  selected: Set<string>
  onSelectedChange: (next: Set<string>) => void
  getKey: (item: T) => string
  // 行右侧附加渲染（如价格列/「已有价格」标注），可省略
  renderExtra?: (item: T) => ReactNode
  // 头部统计文案（如「匹配 N 个，已选 M」），由调用方拼好传入
  countText: string
  searchPlaceholder?: string
  actionText: string
  actionDisabled?: boolean
  onAction: () => void
  // 动作按钮之后的附加按钮（如「关闭」），可省略
  extraActions?: ReactNode
}) {
  const [search, setSearch] = useState('')
  const filtered = items.filter(it => !search || getKey(it).toLowerCase().includes(search.toLowerCase()))
  const allSelected = filtered.length > 0 && filtered.every(it => selected.has(getKey(it)))
  // 对当前过滤集全选/反选（不影响过滤集之外的已选项）
  function toggleAll() {
    const next = new Set(selected)
    filtered.forEach(it => { if (allSelected) next.delete(getKey(it)); else next.add(getKey(it)) })
    onSelectedChange(next)
  }
  return <>
    <div className="flex items-center gap-2">
      <Input placeholder={searchPlaceholder} value={search} onChange={e => setSearch(e.target.value)} className="flex-1" />
      <span className="text-xs text-[var(--muted-foreground)] whitespace-nowrap">{countText}</span>
      <Button variant="outline" onClick={toggleAll}>{allSelected ? '取消全选' : '全选'}</Button>
      <Button onClick={onAction} disabled={actionDisabled}>{actionText}</Button>
      {extraActions}
    </div>
    <div className="max-h-60 overflow-y-auto space-y-1">
      {filtered.map(it => {
        const key = getKey(it)
        return (
          <label key={key} className="flex items-center gap-2 px-2 py-1 text-sm hover:bg-[var(--muted)] rounded cursor-pointer">
            <Checkbox checked={selected.has(key)} onCheckedChange={() => onSelectedChange(toggleInSet(selected, key))} />
            <span className="flex-1">{key}</span>
            {renderExtra?.(it)}
          </label>
        )
      })}
      {filtered.length === 0 && <p className="text-sm text-[var(--muted-foreground)] px-2">无匹配</p>}
    </div>
  </>
}
