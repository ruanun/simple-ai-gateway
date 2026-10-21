import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { Dialog } from '../components/ui/dialog'
import { useFormDialog, FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { confirmDelete } from '../components/ui/confirm'
import { Checkbox } from '../components/ui/checkbox'
import { Combobox } from '../components/ui/combobox'
import { ChangePasswordDialog } from '../components/ChangePasswordDialog'
import { Plus, Trash2, KeyRound } from 'lucide-react'
import { usd, usdToMicros, fmtDateTime } from '../lib/format'
import type { App, PortalUser } from '../lib/types'
import { useI18n } from '../lib/i18n'

const emptyForm = { username: '', password: '', with_app: false, quotaUsd: 1 }

// PortalUsers 管理端的使用方用户管理（P9-B）：建号（可带默认 App）/重置密码/启停/删除。
// 「应用数」可下钻：查看名下应用并在此直接分配/收回（与应用管理页的归属下拉等价）。
export function PortalUsers() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: rows = [] } = useQuery({
    queryKey: ['/admin/portal-users'],
    queryFn: () => api.get<PortalUser[]>('/admin/portal-users'),
  })
  // 应用列表与应用管理页共享同一缓存 key，用于「名下应用」下钻
  const { data: apps = [] } = useQuery({ queryKey: ['/admin/apps'], queryFn: () => api.get<App[]>('/admin/apps') })
  // 变更后失效缓存触发重取，沿用原 load 名字
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/portal-users'] })
  const loadApps = () => Promise.all([load(), qc.invalidateQueries({ queryKey: ['/admin/apps'] })])
  const fd = useFormDialog(emptyForm)
  const [resetTarget, setResetTarget] = useState<PortalUser | null>(null)
  // 「名下应用」下钻目标与待分配应用选择
  const [appsTarget, setAppsTarget] = useState<PortalUser | null>(null)
  const [assignId, setAssignId] = useState('0')

  const createM = useMutation({
    mutationFn: (payload: typeof fd.form) => api.post('/admin/portal-users', {
      username: payload.username, password: payload.password,
      with_app: payload.with_app, quota: usdToMicros(payload.quotaUsd),
    }),
    onSuccess: () => { fd.close(); load() },
  })
  const toggleM = useMutation({
    mutationFn: (u: PortalUser) => api.post(`/admin/portal-users/${u.id}`, { status: u.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })
  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/portal-users/${id}`),
    onSuccess: () => load(),
  })
  // 分配/收回应用归属（user_id=0 即收回为无主）；仅分配成功后重置选择项，
  // 收回不重置——避免连带清空用户已在下拉选中但未提交的分配目标（用 variables 判别，防闭包漂移）
  const setOwnerM = useMutation({
    mutationFn: (p: { appId: number; userId: number }) => api.post(`/admin/apps/${p.appId}/owner`, { user_id: p.userId }),
    onSuccess: (_data, p) => { loadApps(); if (p.userId !== 0) setAssignId('0') },
  })
  // 密码类接口：ChangePasswordDialog 自行 toast 成功/失败，全局兜底会重复提示，故静默
  const resetPwdM = useMutation({
    mutationFn: (p: { id: number; password: string }) => api.post(`/admin/portal-users/${p.id}`, { password: p.password }),
    meta: { silentError: true },
  })
  async function del(u: PortalUser) {
    if (!(await confirmDelete(`删除用户「${u.username}」？`, '其名下应用将变为无主（应用/令牌/日志保留）。'))) return
    delM.mutate(u.id)
  }

  const ownedApps = appsTarget ? apps.filter(a => a.owner_user_id === appsTarget.id) : []
  // 可分配 = 不属于当前用户的应用；标注当前归属方便识别
  const assignable = appsTarget ? apps.filter(a => a.owner_user_id !== appsTarget.id) : []
  const ownerName = (id: number) => rows.find(u => u.id === id)?.username ?? `#${id}`

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('使用方用户')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={loadApps} />
        <Button onClick={fd.openNew}><Plus size={16} />{t('新增用户')}</Button>
      </div>
    </div>
    <p className="text-xs text-[var(--muted-foreground)]">
      使用方用户通过「使用方门户」（/portal/login）登录，自助查看名下应用的配额、用量与日志，并可自建令牌。点击「应用数」可查看并分配名下应用。
    </p>
    <Table head={<><Th>ID</Th><Th>{t('用户名')}</Th><Th>{t('来源')}</Th><Th>{t('应用数')}</Th><Th>{t('状态')}</Th><Th>{t('创建时间')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td>{r.username}</Td>
        <Td className="tnum">{r.source}</Td>
        <Td className="tnum">
          <button className="text-[var(--primary)] hover:underline cursor-pointer"
            onClick={() => { setAppsTarget(r); setAssignId('0') }}>{r.app_count}</button>
        </Td>
        <Td><StatusPill value={r.status} /></Td>
        <Td className="tnum">{fmtDateTime(r.created_at)}</Td>
        <Td className="flex gap-1">
          <Button variant="ghost" onClick={() => setResetTarget(r)}><KeyRound size={14} />{t('重置密码')}</Button>
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          <Button variant="ghost" onClick={() => del(r)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无使用方用户')}</Td></tr>}
    </Table>

    <FormDialog open={fd.open} onClose={fd.close} title={t('新增使用方用户')}
      onSubmit={() => createM.mutate(fd.form)}
      submitDisabled={!fd.form.username || fd.form.password.length < 8} submitting={createM.isPending}>
      <Field label={t('用户名')}><Input name="username" autoComplete="off" value={fd.form.username} onChange={e => fd.setForm({ ...fd.form, username: e.target.value })} /></Field>
      <Field label="密码(8~64 字符)"><Input type="password" name="new-password" autoComplete="new-password" value={fd.form.password} onChange={e => fd.setForm({ ...fd.form, password: e.target.value })} /></Field>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={fd.form.with_app} onCheckedChange={v => fd.setForm({ ...fd.form, with_app: v === true })} />
        同时创建同名默认应用
      </label>
      {fd.form.with_app && (
        <Field label={t('默认应用配额（USD，0 = 不限）')}>
          <Input type="number" value={fd.form.quotaUsd} onChange={e => fd.setForm({ ...fd.form, quotaUsd: Number(e.target.value) })} />
        </Field>
      )}
    </FormDialog>

    {/* 名下应用下钻：查看 + 分配/收回（与应用管理页归属下拉同一后端接口） */}
    <Dialog open={!!appsTarget} onClose={() => setAppsTarget(null)} title={`${appsTarget?.username ?? ''} · ${t('名下应用')}`}>
      <div className="space-y-3">
        {ownedApps.length === 0
          ? <p className="text-sm text-[var(--muted-foreground)]">{t('暂无应用，可在下方分配已有应用。')}</p>
          : <Table head={<><Th>名称</Th><Th>配额</Th><Th>已用</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
              {ownedApps.map(a => <tr key={a.id}>
                <Td>{a.name}</Td>
                <Td className="tnum">{a.quota === 0 ? '不限' : usd(a.quota)}</Td>
                <Td className="tnum">{usd(a.used_quota)}</Td>
                <Td><StatusPill value={a.status} /></Td>
                <Td><Button variant="ghost" disabled={setOwnerM.isPending} onClick={() => setOwnerM.mutate({ appId: a.id, userId: 0 })}>{t('收回')}</Button></Td>
              </tr>)}
            </Table>}
        <Field label={t('分配已有应用')}>
          <div className="flex gap-2">
            <Combobox className="flex-1" value={assignId} onChange={setAssignId}
              placeholder={t('选择应用…')} searchPlaceholder="搜索应用名…"
              options={assignable.map(a => ({
                value: String(a.id),
                label: a.owner_user_id ? `${a.name}（现属 ${ownerName(a.owner_user_id)}）` : `${a.name}（无主）`,
              }))} />
            <Button disabled={assignId === '0' || setOwnerM.isPending}
              onClick={() => setOwnerM.mutate({ appId: Number(assignId), userId: appsTarget!.id })}>{t('分配')}</Button>
          </div>
        </Field>
      </div>
    </Dialog>

    <ChangePasswordDialog open={!!resetTarget} onClose={() => setResetTarget(null)} mode="reset"
      username={resetTarget?.username ?? ''} title={`重置「${resetTarget?.username ?? ''}」的密码`}
      onSubmit={(_, newPwd) => resetPwdM.mutateAsync({ id: resetTarget!.id, password: newPwd })} />
  </div>
}
