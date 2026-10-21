import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, getToken } from '../lib/api'
import { fmtDateTime } from '../lib/format'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { useFormDialog, FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { confirmDelete } from '../components/ui/confirm'
import { ChangePasswordDialog } from '../components/ChangePasswordDialog'
import { Plus, Trash2, KeyRound, Lock } from 'lucide-react'
import type { AdminUser } from '../lib/types'
import { useI18n } from '../lib/i18n'

const ROLE_LABELS: Record<string, string> = { super: '超管', ops: '运营', viewer: '只读' }

// 从 JWT payload 解出当前登录用户名（sub），供改密表单的隐藏 username 字段使用，
// 让浏览器密码管理器能把新密码关联到正确账号。
function currentUsername(): string {
  try {
    const payload = getToken().split('.')[1].replace(/-/g, '+').replace(/_/g, '/')
    return JSON.parse(atob(payload)).sub ?? ''
  } catch { return '' }
}

export function Users() {
  const { t } = useI18n()
  const qc = useQueryClient()
  // 当前登录用户名解析一次复用（此前渲染循环内每行都重新 split/atob/JSON.parse 一遍）
  const me = currentUsername()
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/users'], queryFn: () => api.get<AdminUser[]>('/admin/users') })
  // 变更后失效缓存触发重取，沿用原 load 名字
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/users'] })
  const fd = useFormDialog({ username: '', password: '', role: 'viewer' })
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null)
  const [pwdOpen, setPwdOpen] = useState(false)

  const createM = useMutation({
    mutationFn: (payload: typeof fd.form) => api.post('/admin/users', payload),
    onSuccess: () => { fd.close(); load() },
  })
  const changeRoleM = useMutation({
    mutationFn: (p: { id: number; role: string }) => api.post(`/admin/users/${p.id}`, { role: p.role }),
    onSuccess: () => load(),
  })
  const toggleM = useMutation({
    mutationFn: (u: AdminUser) => api.post(`/admin/users/${u.id}`, { status: u.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })
  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/users/${id}`),
    onSuccess: () => load(),
  })
  // 密码类接口：ChangePasswordDialog 自行 toast 成功/失败，全局兜底会重复提示，故静默
  const resetPwdM = useMutation({
    mutationFn: (p: { id: number; password: string }) => api.post(`/admin/users/${p.id}`, { password: p.password }),
    meta: { silentError: true },
  })
  const myPwdM = useMutation({
    mutationFn: (p: { old: string; next: string }) => api.post('/admin/profile/password', { old_password: p.old, new_password: p.next }),
    meta: { silentError: true },
  })
  async function del(u: AdminUser) {
    if (!(await confirmDelete(`删除管理员「${u.username}」？`))) return
    delM.mutate(u.id)
  }
  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('用户管理')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={load} />
        <Button variant="outline" onClick={() => setPwdOpen(true)}><Lock size={16} />{t('修改我的密码')}</Button>
        <Button onClick={fd.openNew}><Plus size={16} />{t('新增管理员')}</Button>
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('用户名')}</Th><Th>{t('角色')}</Th><Th>状态</Th><Th>{t('创建时间')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td>{r.username}</Td>
        <Td>
          {r.username === me
            ? <span className="text-sm">{t(ROLE_LABELS[r.role] ?? r.role)}</span>
            : <SimpleSelect value={r.role} onChange={role => changeRoleM.mutate({ id: r.id, role })}
                options={Object.entries(ROLE_LABELS).map(([v, label]) => ({ value: v, label: t(label) }))} />}
        </Td>
        <Td><StatusPill value={r.status} /></Td>
        <Td className="tnum">{fmtDateTime(r.created_at)}</Td>
        <Td className="flex gap-1">
          <Button variant="ghost" onClick={() => setResetTarget(r)}><KeyRound size={14} />{t('重置密码')}</Button>
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          <Button variant="ghost" onClick={() => del(r)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无管理员')}</Td></tr>}
    </Table>

    <FormDialog open={fd.open} onClose={fd.close} title={t('新增管理员')}
      onSubmit={() => createM.mutate(fd.form)}
      submitDisabled={!fd.form.username || fd.form.password.length < 8} submitting={createM.isPending}>
      <Field label={t('用户名')}><Input name="username" autoComplete="off" value={fd.form.username} onChange={e => fd.setForm({ ...fd.form, username: e.target.value })} /></Field>
      <Field label={t('密码(8~64 字符)')}><Input type="password" name="new-password" autoComplete="new-password" value={fd.form.password} onChange={e => fd.setForm({ ...fd.form, password: e.target.value })} /></Field>
      <Field label={t('角色')}>
        <SimpleSelect className="w-full" value={fd.form.role} onChange={role => fd.setForm({ ...fd.form, role })}
          options={[
            { value: 'viewer', label: t('只读（仅查看）') },
            { value: 'ops', label: t('运营（除用户管理/系统设置外全部）') },
            { value: 'super', label: t('超管（全部权限）') },
          ]} />
      </Field>
    </FormDialog>

    <ChangePasswordDialog open={!!resetTarget} onClose={() => setResetTarget(null)} mode="reset"
      username={resetTarget?.username ?? ''} title={`重置「${resetTarget?.username ?? ''}」的密码`}
      onSubmit={(_, newPwd) => resetPwdM.mutateAsync({ id: resetTarget!.id, password: newPwd })} />

    <ChangePasswordDialog open={pwdOpen} onClose={() => setPwdOpen(false)} title={t('修改我的密码')}
      username={me}
      onSubmit={(oldPwd, newPwd) => myPwdM.mutateAsync({ old: oldPwd, next: newPwd })} />
  </div>
}
