import type { ReactNode } from 'react'
import { Input } from '../ui/input'
import { SimpleSelect } from '../ui/select'
import { DateTimeRange } from '../ui/date-range-picker'
import { todayStart, daysAgo } from '../../lib/format'
import { useI18n } from '../../lib/i18n'

type Props = {
  channels: { id: number; name: string }[]
  apps: { id: number; name: string }[]
  channel: string
  onChannelChange: (v: string) => void
  app: string
  onAppChange: (v: string) => void
  keyId: string
  onKeyIdChange: (v: string) => void
  start: string
  end: string
  // 时间变更（快捷钮「今天/近7天/近30天」把 end 置空）
  onTimeChange: (start: string, end: string) => void
  // 会话筛选高亮条：非空时显示，点「清除」触发回调
  session?: string
  onSessionClear?: () => void
  // 差异插槽：令牌ID 之后（日志页的模型名 + 接口类型）
  after?: ReactNode
}

// 日志类页面过滤栏：渠道/应用下拉 + 令牌ID + 时间快捷钮 + 日期范围 + 会话筛选高亮条
export function LogFilterBar({
  channels, apps, channel, onChannelChange, app, onAppChange,
  keyId, onKeyIdChange, start, end, onTimeChange, session, onSessionClear,
  after,
}: Props) {
  const { t } = useI18n()
  const quickBtn = 'rounded border border-[var(--border)] px-2 py-1 text-xs hover:bg-[var(--muted)]'
  return <>
    <div className="flex gap-2 flex-wrap">
      <SimpleSelect value={channel} onChange={onChannelChange}
        options={[
          { value: '', label: t('全部渠道') },
          ...channels.map(c => ({ value: String(c.id), label: c.name })),
        ]} />
      <SimpleSelect value={app} onChange={onAppChange}
        options={[
          { value: '', label: t('全部应用') },
          ...apps.map(a => ({ value: String(a.id), label: a.name })),
        ]} />
      <Input className="w-32" placeholder={t('令牌ID')} value={keyId}
        onChange={e => onKeyIdChange(e.target.value)} />
      {after}
      <div className="flex gap-1 items-center">
        <button className={quickBtn} onClick={() => onTimeChange(todayStart(), '')}>{t('今天')}</button>
        <button className={quickBtn} onClick={() => onTimeChange(daysAgo(7), '')}>{t('近7天')}</button>
        <button className={quickBtn} onClick={() => onTimeChange(daysAgo(30), '')}>{t('近30天')}</button>
        <DateTimeRange start={start} end={end} onChange={onTimeChange} />
      </div>
    </div>
    {session && (
      <div className="flex items-center gap-2 text-sm bg-[var(--muted)] rounded-md px-3 py-1.5">
        <span className="text-[var(--muted-foreground)]">{t('会话筛选')}:</span>
        <code className="text-xs font-mono">{session.slice(0, 16)}…</code>
        <button className="text-xs text-[var(--destructive)] hover:underline" onClick={onSessionClear}>{t('清除')}</button>
      </div>
    )}
  </>
}
