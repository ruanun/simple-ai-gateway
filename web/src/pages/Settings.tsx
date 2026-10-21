import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usdToMicros, microsToUsd } from '../lib/format'
import { Button } from '../components/ui/button'
import { Card } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { toast } from '../components/ui/sonner'
import { confirmDialog } from '../components/ui/confirm'
import { Switch } from '../components/ui/switch'
import { SimpleSelect } from '../components/ui/select'
import { TurnstileTestDialog } from '../components/captcha/TurnstileTestDialog'
import { Tip } from '../components/ui/tooltip'
import type { Setting } from '../lib/types'
import { useI18n } from '../lib/i18n'

// OIDC 回调路由（与后端 /portal/api/oidc/callback 一致）
const OIDC_CALLBACK_PATH = '/portal/api/oidc/callback'

export function Settings() {
  const { t } = useI18n()
  const { data } = useQuery({ queryKey: ['/admin/settings'], queryFn: () => api.get<Setting>('/admin/settings') })
  // 编辑态本地拷贝：仅首次加载时从查询结果同步一次，此后只受本地 set()/保存回填驱动，
  // 渲染期间条件赋值等价于原 useEffect(...,[]) 只拉一次的语义，避免额外一次渲染。
  const [s, setS] = useState<Setting | null>(null)
  if (data && s === null) setS(data)
  const [tsTestOpen, setTsTestOpen] = useState(false)

  function set<K extends keyof Setting>(k: K, v: Setting[K]) {
    setS(prev => (prev ? { ...prev, [k]: v } : prev))
  }

  const saveM = useMutation({
    mutationFn: () => api.post<Setting>('/admin/settings', s!),
    onSuccess: saved => { setS(saved); toast.success(t('已保存')) },
  })
  const cleanupM = useMutation({
    mutationFn: () => api.post<{ audit_deleted: number; request_log_deleted: number }>('/admin/settings/cleanup', {}),
    onSuccess: r => toast.success(`已清理:审计 ${r.audit_deleted} 条、请求日志 ${r.request_log_deleted} 条`),
  })

  async function cleanupNow() {
    if (!s || (!s.audit_cleanup_enabled && !s.request_log_cleanup_enabled)) return
    if (!(await confirmDialog({ title: t('按当前保留天数立即清理一次？此操作不可撤销。'), destructive: true, confirmText: t('立即清理') }))) return
    cleanupM.mutate()
  }

  if (!s) return <div className="text-sm text-[var(--muted-foreground)]">加载中…</div>

  const bothOff = !s.audit_cleanup_enabled && !s.request_log_cleanup_enabled

  return <div className="max-w-2xl space-y-6">
    <h2 className="text-lg font-semibold">{t('系统设置')}</h2>

    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-[var(--muted-foreground)]">{t('数据清理')}</h3>
        <Tip content={bothOff ? t('请先启用至少一项清理') : ''}>
          <Button size="sm" variant="outline" onClick={cleanupNow} disabled={bothOff || cleanupM.isPending}>{t('立即清理')}</Button>
        </Tip>
      </div>

      <Card className="p-4 space-y-3">
        <div className="font-medium">{t('审计清理')}</div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={s.audit_cleanup_enabled}
            onCheckedChange={v => set('audit_cleanup_enabled', v)} />
          启用定时清理过期审计记录
        </label>
        <label className="flex items-center gap-2 text-sm">
          保留天数
          <Input type="number" min={1} className="w-24"
            value={s.audit_retention_days}
            onChange={e => set('audit_retention_days', Number(e.target.value))} />
        </label>
      </Card>

      <Card className="p-4 space-y-3">
        <div className="font-medium">{t('请求日志清理')}</div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={s.request_log_cleanup_enabled}
            onCheckedChange={v => set('request_log_cleanup_enabled', v)} />
          启用定时清理过期请求日志
        </label>
        <label className="flex items-center gap-2 text-sm">
          保留天数
          <Input type="number" min={1} className="w-24"
            value={s.request_log_retention_days}
            onChange={e => set('request_log_retention_days', Number(e.target.value))} />
        </label>
      </Card>

      <p className="text-xs text-[var(--muted-foreground)]">
        定时任务每 6 小时自动扫描一次;启用开关并保存后,过期记录会被自动删除。「立即清理」按当前保留天数立刻执行一次。
      </p>
    </section>

    <section className="space-y-3">
      <h3 className="text-sm font-semibold text-[var(--muted-foreground)]">{t('门户与登录')}</h3>

      <Card className="p-4 space-y-3">
        <div className="font-medium">{t('使用方门户')}</div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={s.portal_register_enabled}
            onCheckedChange={v => set('portal_register_enabled', v)} />
          开放使用方注册（/portal/login 页可自行注册账号）
        </label>
        <label className="flex items-center gap-2 text-sm">
          注册默认配额（USD）
          <Input type="number" min={0} step={0.5} className="w-28"
            value={microsToUsd(s.portal_register_quota)}
            onChange={e => set('portal_register_quota', usdToMicros(e.target.value))} />
          <span className="text-xs text-[var(--muted-foreground)]">注册自动创建同名默认应用，0 = 不限</span>
        </label>
        <label className="flex items-center gap-2 text-sm">
          注册人机验证
          <SimpleSelect className="w-52" value={s.captcha_type}
            onChange={v => set('captcha_type', v as Setting['captcha_type'])}
            options={[
              { value: '', label: t('不启用') },
              { value: 'image', label: t('图形验证码') },
              { value: 'turnstile', label: 'Cloudflare Turnstile' },
            ]} />
        </label>
        {s.captcha_type === 'turnstile' && (
          <div className="grid grid-cols-2 gap-3">
            <label className="block space-y-1 text-sm">
              <span className="text-xs font-medium text-[var(--muted-foreground)]">Site Key</span>
              <Input value={s.turnstile_site_key} onChange={e => set('turnstile_site_key', e.target.value)} />
            </label>
            <label className="block space-y-1 text-sm">
              <span className="text-xs font-medium text-[var(--muted-foreground)]">Secret Key</span>
              <Input value={s.turnstile_secret_key} onChange={e => set('turnstile_secret_key', e.target.value)} />
            </label>
            <div className="col-span-2">
              <Button variant="outline" disabled={!s.turnstile_site_key.trim() || !s.turnstile_secret_key.trim()}
                onClick={() => setTsTestOpen(true)}>
                测试配置
              </Button>
            </div>
          </div>
        )}
        <TurnstileTestDialog open={tsTestOpen} onClose={() => setTsTestOpen(false)}
          siteKey={s.turnstile_site_key.trim()} secret={s.turnstile_secret_key.trim()} />
        <p className="text-xs text-[var(--muted-foreground)]">
          防脚本批量注册刷默认配额。图形验证码无外部依赖；Turnstile 需在 Cloudflare 控制台创建站点并把门户域名加入允许列表，
          其校验服务不可达时注册会暂时失败（不放行）。
        </p>
      </Card>

      <Card className="p-4 space-y-3">
        <div className="font-medium">{t('OIDC 第三方登录')}</div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={s.oidc_enabled}
            onCheckedChange={v => set('oidc_enabled', v)} />
          启用通用 OIDC 登录（门户登录页显示第三方登录按钮）
        </label>
        <div className="grid grid-cols-2 gap-3">
          <label className="block space-y-1 text-sm">
            <span className="text-xs font-medium text-[var(--muted-foreground)]">Issuer（发现地址前缀）</span>
            <Input placeholder="https://auth.example.com" value={s.oidc_issuer}
              onChange={e => set('oidc_issuer', e.target.value)} />
          </label>
          <label className="block space-y-1 text-sm">
            <span className="text-xs font-medium text-[var(--muted-foreground)]">{t('按钮文案')}</span>
            <Input value={s.oidc_button_text} onChange={e => set('oidc_button_text', e.target.value)} />
          </label>
          <label className="block space-y-1 text-sm">
            <span className="text-xs font-medium text-[var(--muted-foreground)]">Client ID</span>
            <Input value={s.oidc_client_id} onChange={e => set('oidc_client_id', e.target.value)} />
          </label>
          <label className="block space-y-1 text-sm">
            <span className="text-xs font-medium text-[var(--muted-foreground)]">Client Secret</span>
            <Input value={s.oidc_client_secret} onChange={e => set('oidc_client_secret', e.target.value)} />
          </label>
          <label className="col-span-2 block space-y-1 text-sm">
            <span className="text-xs font-medium text-[var(--muted-foreground)]">{t('回调地址（启用时必填，须与提供方登记的一致）')}</span>
            <div className="flex gap-2">
              <Input className="flex-1" placeholder={`https://你的域名${OIDC_CALLBACK_PATH}`} value={s.oidc_redirect_url}
                onChange={e => set('oidc_redirect_url', e.target.value)} />
              <Button variant="outline" onClick={() => set('oidc_redirect_url', location.origin + OIDC_CALLBACK_PATH)}>{t('填入当前地址')}</Button>
            </div>
          </label>
        </div>
        <p className="text-xs text-[var(--muted-foreground)]">
          回调地址由此处显式配置（不再按请求的 Host 推导），在提供方登记同一地址。首次经第三方登录且未绑定的账号，仅在「开放使用方注册」开启时自动创建。
        </p>
      </Card>
    </section>

    <div>
      <Button onClick={() => saveM.mutate()} disabled={saveM.isPending}>{t('保存')}</Button>
    </div>
  </div>
}
