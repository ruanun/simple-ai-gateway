import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Field } from '../ui/field'
import { Dialog } from '../ui/dialog'
import { FormDialog } from '../ui/form-dialog'
import { Table, Th, Td } from '../ui/table'
import { Pill } from '../ui/badge'
import { StatusPill } from '../StatusPill'
import { confirmDelete } from '../ui/confirm'
import { SimpleSelect } from '../ui/select'
import { Plus, Trash2 } from 'lucide-react'
import { useBreakerStatus, useBreakerReset } from '../../lib/breaker'
import { BreakerBadge } from '../breaker/BreakerBadge'
import { useAuth } from '../../lib/auth'
import type { Account } from '../../lib/types'
import { useI18n } from '../../lib/i18n'

type Props = {
  // 目标渠道 id，0 表示关闭
  channelId: number
  onClose: () => void
}

type AccountForm = {
  name: string
  api_key: string
  base_url: string
  weight: number
  priority: number
  proxy_group: string
  max_concurrency: number
  rpm_limit: number
  expires_at: string
  temp_unschedulable_until: string
  temp_unschedulable_reason: string
  rate_limit_reset_at: string
  overload_until: string
}

function emptyAccountForm(): AccountForm {
  return {
    name: '', api_key: '', base_url: '', weight: 1, priority: 0, proxy_group: '',
    max_concurrency: 0, rpm_limit: 0,
    expires_at: '', temp_unschedulable_until: '', temp_unschedulable_reason: '',
    rate_limit_reset_at: '', overload_until: '',
  }
}

// ISO → datetime-local 输入值（按浏览器本地时区显示）
function toLocalInput(iso: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// datetime-local → RFC3339（UTC）；空串表示清除
function toISO(local: string): string {
  if (!local) return ''
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

function schedulePayload(f: AccountForm) {
  return {
    max_concurrency: Number(f.max_concurrency) || 0,
    rpm_limit: Number(f.rpm_limit) || 0,
    expires_at: toISO(f.expires_at),
    temp_unschedulable_until: toISO(f.temp_unschedulable_until),
    temp_unschedulable_reason: f.temp_unschedulable_reason,
    rate_limit_reset_at: toISO(f.rate_limit_reset_at),
    overload_until: toISO(f.overload_until),
  }
}

function activeCooldown(a: Account): string | null {
  const now = Date.now()
  if (a.expires_at && new Date(a.expires_at).getTime() <= now) return '已过期'
  if (a.temp_unschedulable_until && new Date(a.temp_unschedulable_until).getTime() > now) return '临时停用'
  if (a.rate_limit_reset_at && new Date(a.rate_limit_reset_at).getTime() > now) return '限流冷却'
  if (a.overload_until && new Date(a.overload_until).getTime() > now) return '过载冷却'
  return null
}

function AccountFields({ form, patch, withKey }: {
  form: AccountForm
  patch: (p: Partial<AccountForm>) => void
  withKey: boolean
}) {
  const { t } = useI18n()
  return <>
    <Field label={t('名称')}><Input value={form.name} onChange={e => patch({ name: e.target.value })} placeholder="如 账号2" /></Field>
    {withKey && <Field label={t('上游 API Key')}><Input type="password" value={form.api_key} onChange={e => patch({ api_key: e.target.value })} /></Field>}
    <Field label={t('Base URL（可空，默认用渠道）')}><Input value={form.base_url} onChange={e => patch({ base_url: e.target.value })} /></Field>
    <Field label={t('代理组（可空，直连留空）')}><Input value={form.proxy_group} onChange={e => patch({ proxy_group: e.target.value })} placeholder="pool-a" /></Field>
    <div className="grid grid-cols-2 gap-3">
      <Field label={t('权重')}><Input type="number" value={form.weight} onChange={e => patch({ weight: Number(e.target.value) })} /></Field>
      <Field label={t('优先级')}><Input type="number" value={form.priority} onChange={e => patch({ priority: Number(e.target.value) })} /></Field>
      <Field label={t('最大并发（0=不限）')}><Input type="number" min={0} value={form.max_concurrency} onChange={e => patch({ max_concurrency: Number(e.target.value) })} /></Field>
      <Field label={t('RPM 上限（0=不限）')}><Input type="number" min={0} value={form.rpm_limit} onChange={e => patch({ rpm_limit: Number(e.target.value) })} /></Field>
    </div>
    <div className="rounded-md border border-[var(--border)] p-3 space-y-3">
      <div className="text-xs font-medium text-[var(--muted-foreground)]">{t('调度窗口（留空=不限/不冷却）')}</div>
      <Field label={t('凭证过期时间')}><Input type="datetime-local" value={form.expires_at} onChange={e => patch({ expires_at: e.target.value })} /></Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('临时停用至')}><Input type="datetime-local" value={form.temp_unschedulable_until} onChange={e => patch({ temp_unschedulable_until: e.target.value })} /></Field>
        <Field label={t('停用原因')}><Input value={form.temp_unschedulable_reason} onChange={e => patch({ temp_unschedulable_reason: e.target.value })} placeholder={t('维护/额度耗尽')} /></Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('限流冷却至')}><Input type="datetime-local" value={form.rate_limit_reset_at} onChange={e => patch({ rate_limit_reset_at: e.target.value })} /></Field>
        <Field label={t('过载冷却至')}><Input type="datetime-local" value={form.overload_until} onChange={e => patch({ overload_until: e.target.value })} /></Field>
      </div>
    </div>
  </>
}

// 密钥池（账号）管理弹窗：账号列表 + 新增/编辑账号 + 代理组设置，状态与数据拉取自持
export function AccountsDialog({ channelId, onClose }: Props) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: accounts = [] } = useQuery({
    queryKey: ['/admin/accounts', { channel_id: channelId }],
    queryFn: () => api.get<Account[]>(`/admin/accounts?channel_id=${channelId}`),
    enabled: channelId !== 0,
  })
  const reload = () => qc.invalidateQueries({ queryKey: ['/admin/accounts', { channel_id: channelId }] })
  const { role } = useAuth()
  const resetBreaker = useBreakerReset()
  // 熔断状态：与渠道页共用同 key，弹窗打开期间同样 10s 轮询
  const { data: brk } = useBreakerStatus()
  const accBrkMap = useMemo(() => new Map((brk?.accounts ?? []).map(x => [x.id, x])), [brk])
  // 账号×模型配额冷却（429/403）：按账号聚合，状态列叠加提示（此前状态页漏报该维度）
  const quotaMap = useMemo(() => {
    const m = new Map<number, { model: string; open_until: number }[]>()
    for (const q of brk?.account_quotas ?? []) {
      const arr = m.get(q.account_id) ?? []
      arr.push(q)
      m.set(q.account_id, arr)
    }
    return m
  }, [brk])
  const [accOpen, setAccOpen] = useState(false)
  const [accForm, setAccForm] = useState<AccountForm>(emptyAccountForm)
  const patchAcc = (p: Partial<AccountForm>) => setAccForm(prev => ({ ...prev, ...p }))

  const addAccountM = useMutation({
    mutationFn: (payload: AccountForm) => api.post('/admin/accounts', {
      channel_id: channelId,
      name: payload.name,
      api_key: payload.api_key,
      base_url: payload.base_url,
      proxy_group: payload.proxy_group,
      weight: Number(payload.weight),
      priority: Number(payload.priority),
      ...schedulePayload(payload),
    }),
    onSuccess: () => { setAccOpen(false); setAccForm(emptyAccountForm()); reload() },
  })

  const [editAcc, setEditAcc] = useState<Account | null>(null)
  const [editForm, setEditForm] = useState<AccountForm>(emptyAccountForm)
  const patchEdit = (p: Partial<AccountForm>) => setEditForm(prev => ({ ...prev, ...p }))
  function openEdit(a: Account) {
    setEditForm({
      ...emptyAccountForm(),
      name: a.name,
      base_url: a.base_url,
      weight: a.weight,
      priority: a.priority,
      proxy_group: a.proxy_group,
      max_concurrency: a.max_concurrency,
      rpm_limit: a.rpm_limit,
      expires_at: toLocalInput(a.expires_at),
      temp_unschedulable_until: toLocalInput(a.temp_unschedulable_until),
      temp_unschedulable_reason: a.temp_unschedulable_reason,
      rate_limit_reset_at: toLocalInput(a.rate_limit_reset_at),
      overload_until: toLocalInput(a.overload_until),
    })
    setEditAcc(a)
  }
  const updateAccountM = useMutation({
    mutationFn: (payload: { id: number; form: AccountForm }) => api.post(`/admin/accounts/${payload.id}`, {
      name: payload.form.name,
      base_url: payload.form.base_url,
      weight: Number(payload.form.weight),
      priority: Number(payload.form.priority),
      ...schedulePayload(payload.form),
    }),
    onSuccess: () => { setEditAcc(null); reload() },
  })

  const delAccountM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/accounts/${id}`),
    onSuccess: () => reload(),
  })
  async function delAccount(id: number) {
    if (await confirmDelete(t('删除该账号？'))) delAccountM.mutate(id)
  }
  const toggleAccountStatusM = useMutation({
    mutationFn: (a: Account) => api.post(`/admin/accounts/${a.id}/status`, { status: a.status === 1 ? 0 : 1 }),
    onSuccess: () => reload(),
  })

  // 代理组弹窗：代理分组列表按需查询（弹窗打开时才拉），去重逻辑派生自查询结果
  const [proxyDialogAcc, setProxyDialogAcc] = useState<Account | null>(null)
  const { data: proxies = [] } = useQuery({
    queryKey: ['/admin/proxies'],
    queryFn: () => api.get<{ group: string }[]>('/admin/proxies'),
    enabled: proxyDialogAcc !== null,
  })
  const proxyGroups = useMemo(() => [...new Set(proxies.map(p => p.group).filter(Boolean))].sort(), [proxies])
  const [proxyGroupVal, setProxyGroupVal] = useState('')
  function openProxyDialog(a: Account) {
    setProxyGroupVal(a.proxy_group || '')
    setProxyDialogAcc(a)
  }
  const saveProxyGroupM = useMutation({
    mutationFn: (payload: { id: number; proxy_group: string }) => api.post(`/admin/accounts/${payload.id}/proxy`, { proxy_group: payload.proxy_group }),
    onSuccess: () => { setProxyDialogAcc(null); reload() },
  })
  function saveProxyGroup() {
    if (!proxyDialogAcc) return
    saveProxyGroupM.mutate({ id: proxyDialogAcc.id, proxy_group: proxyGroupVal })
  }

  return <>
    {/* 密钥池 Dialog */}
    <Dialog open={channelId !== 0} onClose={onClose} title={`${t('密钥池')} · #${channelId}`} width="max-w-4xl">
      <div className="space-y-3">
        <div className="flex justify-end">
          <Button onClick={() => setAccOpen(true)}><Plus size={15} />{t('加账号')}</Button>
        </div>
        <div className="max-h-[60vh] overflow-y-auto">
          <Table head={<><Th>ID</Th><Th>{t('名称')}</Th><Th>{t('代理组')}</Th><Th>{t('权重/优先级')}</Th><Th>{t('并发/RPM')}</Th><Th>{t('失败数')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
            {accounts.map(a => {
              const cooldown = activeCooldown(a)
              return <tr key={a.id} className="hover:bg-[var(--muted)]">
                <Td className="tnum">{a.id}</Td><Td>{a.name || t('默认账号')}</Td>
                <Td className="tnum">{a.proxy_group || t('直连')}</Td>
                <Td className="tnum">{a.weight}/{a.priority}</Td>
                <Td className="tnum">{a.max_concurrency > 0 ? a.max_concurrency : '∞'}/{a.rpm_limit > 0 ? a.rpm_limit : '∞'}</Td>
                <Td className="tnum">{a.fail_count}</Td>
                <Td><div className="flex items-center gap-2">
                  <StatusPill value={a.status} />
                  <BreakerBadge state={accBrkMap.get(a.id)?.state} openUntil={accBrkMap.get(a.id)?.open_until} canReset={role !== 'viewer'} onReset={() => resetBreaker('account', a.id)} />
                  {cooldown && <Pill status="warn">{t(cooldown)}</Pill>}
                  {(quotaMap.get(a.id)?.length ?? 0) > 0 && (
                    <span title={quotaMap.get(a.id)!.map(q => `${q.model} 冷却至 ${new Date(q.open_until * 1000).toLocaleTimeString()}`).join('\n')}>
                      <Pill status="warn">{t('配额冷却')}×{quotaMap.get(a.id)!.length}</Pill>
                    </span>
                  )}
                </div></Td>
                <Td className="flex gap-1">
                  <Button variant="ghost" onClick={() => openEdit(a)}>{t('编辑')}</Button>
                  <Button variant="ghost" onClick={() => openProxyDialog(a)}>{t('代理组')}</Button>
                  <Button variant="ghost" disabled={toggleAccountStatusM.isPending} onClick={() => toggleAccountStatusM.mutate(a)}>{a.status === 1 ? t('停用') : t('启用')}</Button>
                  <Button variant="ghost" onClick={() => delAccount(a.id)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
                </Td>
              </tr>
            })}
            {accounts.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无账号')}</Td></tr>}
          </Table>
        </div>
      </div>
    </Dialog>

    {/* 新增账号 Dialog */}
    <FormDialog open={accOpen} onClose={() => setAccOpen(false)} title={t('新增账号')}
      onSubmit={() => addAccountM.mutate(accForm)}
      submitDisabled={!accForm.api_key} submitting={addAccountM.isPending}>
      <AccountFields form={accForm} patch={patchAcc} withKey />
    </FormDialog>

    {/* 编辑账号 Dialog */}
    <FormDialog open={editAcc !== null} onClose={() => setEditAcc(null)} title={t('编辑账号')}
      onSubmit={() => editAcc && updateAccountM.mutate({ id: editAcc.id, form: editForm })}
      submitting={updateAccountM.isPending}>
      <AccountFields form={editForm} patch={patchEdit} withKey={false} />
    </FormDialog>

    {/* 代理组设置 Dialog */}
    <Dialog open={!!proxyDialogAcc} onClose={() => setProxyDialogAcc(null)} title={t('设置代理组')}>
      <div className="space-y-3">
        <Field label={t('代理组')}>
          <SimpleSelect className="w-full" value={proxyGroupVal} onChange={v => setProxyGroupVal(v)}
            options={[
              { value: '', label: t('直连（不使用代理）') },
              ...proxyGroups.map(g => ({ value: g, label: g })),
            ]} />
        </Field>
        <p className="text-xs text-[var(--muted-foreground)]">选择代理组后，该账号的出站请求将通过代理池中对应分组的代理发出。如需新建代理组，请先在「IP 代理池」页面添加代理。</p>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="outline" onClick={() => setProxyDialogAcc(null)}>{t('取消')}</Button>
          <Button onClick={saveProxyGroup} disabled={saveProxyGroupM.isPending}>{t('保存')}</Button>
        </div>
      </div>
    </Dialog>
  </>
}
