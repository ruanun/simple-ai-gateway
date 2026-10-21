import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { portalApi } from '../lib/api'
import { usd } from '../lib/format'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { toast } from '../components/ui/sonner'
import { confirmDelete } from '../components/ui/confirm'
import { Plus, Trash2, Copy } from 'lucide-react'
import type { App, PortalApiKey as Key } from '../lib/types'
import { useI18n } from '../lib/i18n'

// PortalKeys 门户令牌自助：在自己 App 配额内创建/改名/启停/删除令牌（每 App 上限 20）。
export function PortalKeys() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: apps = [] } = useQuery({ queryKey: ['/portal/api/apps'], queryFn: () => portalApi.get<App[]>('/portal/api/apps') })
  const { data: rows = [] } = useQuery({ queryKey: ['/portal/api/api-keys'], queryFn: () => portalApi.get<Key[]>('/portal/api/api-keys') })
  const [createOpen, setCreateOpen] = useState(false)
  const [form, setForm] = useState({ name: '', app_id: 0 })

  // 变更后失效两份缓存触发重取，沿用原 load 名字
  const load = () => {
    qc.invalidateQueries({ queryKey: ['/portal/api/apps'] })
    qc.invalidateQueries({ queryKey: ['/portal/api/api-keys'] })
  }
  // 所属应用默认值渲染期派生：未手选（app_id 为 0）时落到列表第一项
  const formAppId = form.app_id || apps[0]?.id || 0

  function appName(id: number) { return apps.find(a => a.id === id)?.name ?? `#${id}` }

  const createM = useMutation({
    mutationFn: (payload: typeof form & { app_id: number }) => portalApi.post<{ key: string }>('/portal/api/api-keys', payload),
    onSuccess: async r => {
      setCreateOpen(false); setForm(f => ({ ...f, name: '' })); load()
      await navigator.clipboard.writeText(r.key).catch(() => {})
      toast.success(`令牌已创建并复制到剪贴板：${r.key}`)
    },
  })
  async function copyKey(k: Key) {
    await navigator.clipboard.writeText(k.key)
    toast.success(t('已复制令牌'))
  }
  // 管理员停用的令牌门户无法启用（后端 403），只能删除
  const lockedByAdmin = (k: Key) => k.status !== 1 && k.disabled_by_admin
  const toggleM = useMutation({
    mutationFn: (k: Key) => portalApi.post(`/portal/api/api-keys/${k.id}`, { status: k.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })
  const delM = useMutation({
    mutationFn: (id: number) => portalApi.del(`/portal/api/api-keys/${id}`),
    onSuccess: () => load(),
  })
  async function del(k: Key) {
    if (!(await confirmDelete(`删除令牌「${k.name}」？使用该令牌的调用将立即失效。`))) return
    delM.mutate(k.id)
  }

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('我的令牌')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={load} />
        <Button onClick={() => setCreateOpen(true)} disabled={apps.length === 0}><Plus size={16} />{t('新建令牌')}</Button>
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('名称')}</Th><Th>{t('所属应用')}</Th><Th>{t('令牌')}</Th><Th>{t('今日已用')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td>
        <Td>{r.name}</Td>
        <Td>{appName(r.app_id)}</Td>
        <Td className="tnum">{r.prefix}…</Td>
        <Td className="tnum">{usd(r.daily_cost_used)}{r.daily_cost_limit > 0 ? ` / ${usd(r.daily_cost_limit)}` : ''}</Td>
        <Td>
          <StatusPill value={r.status} />
          {lockedByAdmin(r) && <span className="ml-1 text-xs text-[var(--muted-foreground)]">{t('管理员已停用')}</span>}
        </Td>
        <Td className="flex gap-1">
          {r.key && <Button variant="ghost" onClick={() => copyKey(r)}><Copy size={14} />{t('复制')}</Button>}
          {!lockedByAdmin(r) && (
            <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          )}
          <Button variant="ghost" onClick={() => del(r)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">暂无令牌{apps.length === 0 ? t('（请先联系管理员分配应用）') : t('，点右上角新建')}</Td></tr>}
    </Table>

    <FormDialog open={createOpen} onClose={() => setCreateOpen(false)} title={t('新建令牌')}
      onSubmit={() => createM.mutate({ ...form, app_id: formAppId })}
      submitDisabled={!form.name.trim() || !formAppId} submitting={createM.isPending} submitText={t('创建')}>
      <Field label={t('名称')}><Input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder={t('例如：my-app-dev')} /></Field>
      <Field label={t('所属应用')}>
        <SimpleSelect value={String(formAppId)} onChange={v => setForm({ ...form, app_id: Number(v) })} className="w-full"
          placeholder={t('选择应用')} options={apps.map(a => ({ value: String(a.id), label: a.name }))} />
      </Field>
    </FormDialog>
  </div>
}
