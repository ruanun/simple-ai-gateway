import { useMemo } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { useFormDialog, FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { confirmDelete } from '../components/ui/confirm'
import { useBreakerStatus, useBreakerReset } from '../lib/breaker'
import { BreakerBadge } from '../components/breaker/BreakerBadge'
import { useAuth } from '../lib/auth'
import { Plus, Trash2 } from 'lucide-react'
import type { ProxyItem } from '../lib/types'
import { useI18n } from '../lib/i18n'

const emptyForm = { group: '', name: '', protocol: 'http', host: '', port: 1080, username: '', password: '', weight: 1, priority: 0 }

export function Proxies() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/proxies'], queryFn: () => api.get<ProxyItem[]>('/admin/proxies') })
  // 变更后失效缓存触发重取，沿用原 load 名字
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/proxies'] })
  const { data: brk } = useBreakerStatus()
  const proxyBrkMap = useMemo(() => new Map((brk?.proxies ?? []).map(p => [p.id, p])), [brk])
  const { role } = useAuth()
  const resetBreaker = useBreakerReset()
  const fd = useFormDialog(emptyForm)
  const createM = useMutation({
    mutationFn: (payload: typeof emptyForm) =>
      api.post('/admin/proxies', { ...payload, port: Number(payload.port), weight: Number(payload.weight), priority: Number(payload.priority) }),
    onSuccess: () => { fd.close(); load() },
  })
  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/proxies/${id}`),
    onSuccess: () => load(),
  })
  const toggleM = useMutation({
    mutationFn: (p: ProxyItem) => api.post(`/admin/proxies/${p.id}/status`, { status: p.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })
  async function del(id: number) {
    if (await confirmDelete(t('删除该代理？'))) delM.mutate(id)
  }
  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('IP 代理池')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={load} />
        <Button onClick={fd.openNew}><Plus size={16} />{t('新增代理')}</Button>
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('分组')}</Th><Th>{t('名称')}</Th><Th>{t('协议')}</Th><Th>{t('地址')}</Th><Th>{t('权重/优先级')}</Th><Th>{t('失败数')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td><span className="rounded bg-[var(--muted)] px-1.5 py-0.5 text-xs">{r.group}</span></Td>
        <Td>{r.name}</Td><Td className="tnum">{r.protocol}</Td>
        <Td className="tnum">{r.host}:{r.port}</Td>
        <Td className="tnum">{r.weight}/{r.priority}</Td>
        <Td className="tnum">{r.fail_count}</Td>
        <Td><span className="flex items-center gap-2">
          <StatusPill value={r.status} />
          <BreakerBadge state={proxyBrkMap.get(r.id)?.state} openUntil={proxyBrkMap.get(r.id)?.open_until} canReset={role !== 'viewer'} onReset={() => resetBreaker('proxy', r.id)} />
        </span></Td>
        <Td className="flex gap-1">
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          <Button variant="ghost" onClick={() => del(r.id)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无代理，新增并分组后可在账号上绑定')}</Td></tr>}
    </Table>
    <FormDialog open={fd.open} onClose={fd.close} title={t('新增代理')}
      onSubmit={() => createM.mutate(fd.form)}
      submitDisabled={!fd.form.group || !fd.form.host} submitting={createM.isPending}>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('分组')}><Input value={fd.form.group} onChange={e => fd.setForm({ ...fd.form, group: e.target.value })} placeholder="pool-a" /></Field>
        <Field label={t('名称')}><Input value={fd.form.name} onChange={e => fd.setForm({ ...fd.form, name: e.target.value })} /></Field>
      </div>
      <Field label={t('协议')}>
        <SimpleSelect className="w-full" value={fd.form.protocol}
          onChange={v => fd.setForm({ ...fd.form, protocol: v })}
          options={[
            { value: 'http', label: 'http' },
            { value: 'https', label: 'https' },
            { value: 'socks5', label: 'socks5' },
          ]} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('主机')}><Input value={fd.form.host} onChange={e => fd.setForm({ ...fd.form, host: e.target.value })} placeholder="127.0.0.1" /></Field>
        <Field label={t('端口')}><Input type="number" value={fd.form.port} onChange={e => fd.setForm({ ...fd.form, port: Number(e.target.value) })} /></Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('用户名(可空)')}><Input value={fd.form.username} onChange={e => fd.setForm({ ...fd.form, username: e.target.value })} /></Field>
        <Field label={t('密码(可空)')}><Input type="password" value={fd.form.password} onChange={e => fd.setForm({ ...fd.form, password: e.target.value })} /></Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('权重')}><Input type="number" value={fd.form.weight} onChange={e => fd.setForm({ ...fd.form, weight: Number(e.target.value) })} /></Field>
        <Field label={t('优先级')}><Input type="number" value={fd.form.priority} onChange={e => fd.setForm({ ...fd.form, priority: Number(e.target.value) })} /></Field>
      </div>
    </FormDialog>
  </div>
}
