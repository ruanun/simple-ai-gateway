import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { portalApi } from '../lib/api'
import { usd } from '../lib/format'
import { Card } from '../components/ui/card'
import { StatusPill } from '../components/StatusPill'
import { Button } from '../components/ui/button'
import { RefreshButton } from '../components/ui/refresh'
import type { App } from '../lib/types'
import { useI18n } from '../lib/i18n'

// PortalHome 门户概览：名下 App 卡片（配额进度）+ 用量分析入口。
export function PortalHome() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { data: apps = [] } = useQuery({
    queryKey: ['/portal/api/apps'],
    queryFn: () => portalApi.get<App[]>('/portal/api/apps'),
    meta: { silentError: true },
  })
  // 刷新按钮沿用原 loadApps 名字，改为失效缓存触发重取
  const loadApps = () => qc.invalidateQueries({ queryKey: ['/portal/api/apps'] })

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('我的应用')}</h2>
      <RefreshButton onRefresh={loadApps} />
    </div>
    {apps.length === 0 && <p className="text-sm text-[var(--muted-foreground)]">{t('暂无应用，请联系管理员为你分配。')}</p>}
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {apps.map(a => {
        const pct = a.quota > 0 ? Math.min(100, Math.round(a.used_quota / a.quota * 100)) : 0
        return <Card key={a.id} className="p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="font-medium">{a.name}</span>
            <StatusPill value={a.status} />
          </div>
          <div className="space-y-1">
            <div className="flex justify-between text-sm">
              <span className="text-[var(--muted-foreground)]">已用 {usd(a.used_quota)}</span>
              <span className="text-[var(--muted-foreground)]">{a.quota === 0 ? t('配额不限') : `配额 ${usd(a.quota)}`}</span>
            </div>
            {a.quota > 0 && (
              <div className="h-2 rounded-full bg-[var(--muted)]">
                <div className={`h-2 rounded-full ${pct >= 90 ? 'bg-[var(--destructive)]' : 'bg-[var(--primary)]'}`} style={{ width: `${pct}%` }} />
              </div>
            )}
          </div>
          <Button variant="outline" onClick={() => navigate(`/portal/apps/${a.id}/usage`)}>{t('用量分析')}</Button>
        </Card>
      })}
    </div>
  </div>
}
