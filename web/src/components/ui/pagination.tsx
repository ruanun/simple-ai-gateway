import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, MoreHorizontal } from 'lucide-react'
import { type ComponentProps, useState } from 'react'
import { cn } from '../../lib/utils'
import { buttonVariants } from './button'
import { Input } from './input'
import { SimpleSelect } from './select'
import { useI18n } from '../../lib/i18n'

// —— shadcn/ui 官方 pagination 原语（对接站点 CSS 变量与本项目 buttonVariants）——
// 适配点：① 复用 button.tsx 的 buttonVariants（已是 CSS 变量主题）；② 当前页用 primary 主蓝高亮；
// ③ 本项目分页是 state 驱动无路由，PaginationLink 渲染 <button> 而非 <a>（键盘可达、无 # 锚点跳转）；
// ④ 文案中文化。这套原语可供后续任意列表页直接复用。

function Pagination({ className, ...props }: ComponentProps<'nav'>) {
  return <nav role="navigation" aria-label="分页" className={cn('mx-auto flex w-full justify-center', className)} {...props} />
}

function PaginationContent({ className, ...props }: ComponentProps<'ul'>) {
  return <ul className={cn('flex flex-row items-center gap-1', className)} {...props} />
}

function PaginationItem({ ...props }: ComponentProps<'li'>) {
  return <li {...props} />
}

type PaginationLinkProps = { isActive?: boolean; size?: 'default' | 'sm' | 'icon' } & ComponentProps<'button'>

// PaginationLink：可点页码。isActive 用 primary（主蓝）高亮，否则 ghost；均以 <button> 承载点击。
function PaginationLink({ className, isActive, size = 'icon', type, ...props }: PaginationLinkProps) {
  return (
    <button
      type={type ?? 'button'}
      aria-current={isActive ? 'page' : undefined}
      data-active={isActive}
      className={cn(buttonVariants({ variant: isActive ? 'primary' : 'ghost', size }), 'tnum min-w-9 justify-center', className)}
      {...props}
    />
  )
}

// 翻页方向按钮（上一页/下一页/首页/末页）：统一 outline 风格，到边由 disabled 置灰。
function PaginationPrevious({ className, ...props }: ComponentProps<'button'>) {
  return (
    <button type="button" aria-label="上一页" className={cn(buttonVariants({ variant: 'outline', size: 'sm' }), className)} {...props}>
      <ChevronLeft className="h-4 w-4" />
      <span>上一页</span>
    </button>
  )
}

function PaginationNext({ className, ...props }: ComponentProps<'button'>) {
  return (
    <button type="button" aria-label="下一页" className={cn(buttonVariants({ variant: 'outline', size: 'sm' }), className)} {...props}>
      <span>下一页</span>
      <ChevronRight className="h-4 w-4" />
    </button>
  )
}

function PaginationFirst({ className, ...props }: ComponentProps<'button'>) {
  return (
    <button type="button" aria-label="首页" className={cn(buttonVariants({ variant: 'outline', size: 'icon' }), className)} {...props}>
      <ChevronsLeft className="h-4 w-4" />
    </button>
  )
}

function PaginationLast({ className, ...props }: ComponentProps<'button'>) {
  return (
    <button type="button" aria-label="末页" className={cn(buttonVariants({ variant: 'outline', size: 'icon' }), className)} {...props}>
      <ChevronsRight className="h-4 w-4" />
    </button>
  )
}

function PaginationEllipsis({ className, ...props }: ComponentProps<'span'>) {
  return (
    <span aria-hidden className={cn('flex h-8 w-8 items-center justify-center text-[var(--muted-foreground)]', className)} {...props}>
      <MoreHorizontal className="h-4 w-4" />
    </span>
  )
}

// —— 省略号页码算法（MUI usePagination 同款）——
// 输入 1 起始当前页与总页数，输出页码序列；'ellipsis' 为省略号占位。
// 页数少（<= 首尾 2 + 当前两侧 2*sibling + 3 个省略号槽）时全列不加省略号；
// 间隙恰好差 1 页时补出该真实页码，避免出现「1 … 3」这种假省略号。
export type PageItem = number | 'ellipsis'

export function buildPageItems(current: number, totalPages: number, sibling = 1): PageItem[] {
  const range = (s: number, e: number) => Array.from({ length: Math.max(0, e - s + 1) }, (_, i) => s + i)
  const boundary = 1 // 首尾各固定显示 1 页
  const totalSlots = boundary * 2 + sibling * 2 + 3
  if (totalPages <= totalSlots) return range(1, totalPages)

  const left = Math.max(Math.min(current - sibling, totalPages - boundary - sibling * 2 - 1), boundary + 2)
  const right = Math.min(Math.max(current + sibling, boundary + sibling * 2 + 2), totalPages - boundary - 1)

  return [
    ...range(1, boundary),
    left > boundary + 2 ? 'ellipsis' : boundary + 1, // 左间隙 >1 页才给省略号，否则补真实页码
    ...range(left, right),
    right < totalPages - boundary - 1 ? 'ellipsis' : totalPages - boundary,
    ...range(totalPages - boundary + 1, totalPages),
  ]
}

// —— 便捷封装：列表分页脚 ——
// 组合上述原语 + 每页条数选择器 + 跳页输入 + 首末页，保住各列表页的简洁调用（Prices/Logs/OpLogs/PortalLogs 共用）。
type BarProps = {
  total: number
  page: number // 0 起始页码
  pageSize: number
  onPageChange: (page: number) => void
  // 左侧追加「· 第 x/y 页」页码信息
  showPageInfo?: boolean
  // 单页（含空列表）时隐藏翻页导航（价格页形态）；仍保留「共 N 条 + 每页条数选择器」
  hideOnSinglePage?: boolean
  // 传入则渲染每页条数选择器
  onPageSizeChange?: (size: number) => void
  pageSizeOptions?: number[]
  // 跳页输入框 / 首末页按钮
  showJumper?: boolean
  showEdges?: boolean
  // 当前页两侧邻页数
  siblingCount?: number
}

export function PaginationBar({
  total,
  page,
  pageSize,
  onPageChange,
  showPageInfo = false,
  hideOnSinglePage = false,
  onPageSizeChange,
  pageSizeOptions,
  showJumper = false,
  showEdges = false,
  siblingCount = 1,
}: BarProps) {
  const { t } = useI18n()
  const [jump, setJump] = useState('')
  if (total === 0) return null

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const onSize = onPageSizeChange
  const showNav = !(hideOnSinglePage && totalPages <= 1) // 单页时按需隐藏翻页导航
  const atFirst = page === 0
  const atLast = page + 1 >= totalPages

  function doJump() {
    if (jump === '') return
    const n = Number(jump)
    if (!Number.isNaN(n)) onPageChange(Math.min(Math.max(1, Math.trunc(n)), totalPages) - 1)
    setJump('')
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
      <span className={cn('text-[var(--muted-foreground)]', !showPageInfo && 'tnum')}>
        {showPageInfo ? t('共 {total} 条 · 第 {page}/{pages} 页', { total, page: page + 1, pages: totalPages }) : t('共 {total} 条', { total })}
      </span>
      <div className="flex flex-wrap items-center gap-2">
        {onSize && (
          <div className="flex items-center gap-1 text-[var(--muted-foreground)]">
            <span>{t('每页')}</span>
            <SimpleSelect
              className="w-[4.5rem]"
              value={String(pageSize)}
              onChange={(v) => onSize(Number(v))}
              options={(pageSizeOptions ?? [10, 20, 50, 100]).map((n) => ({ value: String(n), label: String(n) }))}
            />
            <span>{t('条')}</span>
          </div>
        )}
        {showNav && (
          <Pagination className="mx-0 w-auto">
            <PaginationContent>
              {showEdges && (
                <PaginationItem>
                  <PaginationFirst disabled={atFirst} onClick={() => onPageChange(0)} />
                </PaginationItem>
              )}
              <PaginationItem>
                <PaginationPrevious disabled={atFirst} onClick={() => onPageChange(page - 1)} />
              </PaginationItem>
              {buildPageItems(page + 1, totalPages, siblingCount).map((it, idx) =>
                it === 'ellipsis' ? (
                  <PaginationItem key={`e-${idx}`}>
                    <PaginationEllipsis />
                  </PaginationItem>
                ) : (
                  <PaginationItem key={it}>
                    <PaginationLink isActive={it === page + 1} onClick={() => onPageChange(it - 1)}>
                      {it}
                    </PaginationLink>
                  </PaginationItem>
                ),
              )}
              <PaginationItem>
                <PaginationNext disabled={atLast} onClick={() => onPageChange(page + 1)} />
              </PaginationItem>
              {showEdges && (
                <PaginationItem>
                  <PaginationLast disabled={atLast} onClick={() => onPageChange(totalPages - 1)} />
                </PaginationItem>
              )}
            </PaginationContent>
          </Pagination>
        )}
        {showNav && showJumper && (
          <div className="flex items-center gap-1 text-[var(--muted-foreground)]">
            <span>{t('跳至')}</span>
            <Input
              className="w-14 text-center tnum"
              inputMode="numeric"
              value={jump}
              onChange={(e) => setJump(e.target.value.replace(/[^\d]/g, ''))}
              onKeyDown={(e) => {
                if (e.key === 'Enter') doJump()
              }}
              onBlur={doJump}
            />
            <span>{t('页')}</span>
          </div>
        )}
      </div>
    </div>
  )
}

export {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationEllipsis,
  PaginationPrevious,
  PaginationNext,
  PaginationFirst,
  PaginationLast,
}
