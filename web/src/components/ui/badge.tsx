import type { ReactNode } from 'react'

type Status = 'ok' | 'warn' | 'down' | 'info' | 'muted'
const color: Record<Status, string> = {
  ok: 'var(--ok)', warn: 'var(--warn)', down: 'var(--down)', info: 'var(--info)', muted: 'var(--muted-foreground)',
}
export function Badge({ status, children }: { status: Status; children: ReactNode }) {
  return <span className="inline-flex items-center gap-1 text-xs">
    <span className="h-2 w-2 rounded-full" style={{ background: color[status] }} />{children}
  </span>
}

// Pill 色底徽章：用于开关型状态（启用/停用等），与 Badge 圆点（分类标识）形态区分，避免同形撞色
export function Pill({ status, children }: { status: Status; children: ReactNode }) {
  return <span
    className="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium"
    style={{ color: color[status], background: `color-mix(in srgb, ${color[status]} 12%, transparent)` }}
  >{children}</span>
}
