import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usd, usdToMicros, microsToUsd } from '../lib/format'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { Badge } from '../components/ui/badge'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { Combobox } from '../components/ui/combobox'
import { confirmDelete } from '../components/ui/confirm'
import { Checkbox } from '../components/ui/checkbox'
import { toggleInSet } from '../lib/utils'
import { Plus, Trash2, Pencil, Users } from 'lucide-react'
import { AppMembersDialog } from '../components/AppMembersDialog'
import type { App, Channel, PortalUser } from '../lib/types'
import { useI18n } from '../lib/i18n'

export function Apps() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const navigate = useNavigate()
  // 列表数据统一走 TanStack Query（挂载即拉取，同 key 跨页面共享缓存）
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/apps'], queryFn: () => api.get<App[]>('/admin/apps') })
  const { data: channels = [] } = useQuery({ queryKey: ['/admin/channels'], queryFn: () => api.get<Channel[]>('/admin/channels') })
  // 该接口不可用时（如未配置 OIDC/权限不足）静默降级为空列表，不弹全局错误 toast（既有行为，原用 .catch 吞错）
  const { data: portalUsers = [] } = useQuery({
    queryKey: ['/admin/portal-users'],
    queryFn: () => api.get<PortalUser[]>('/admin/portal-users'),
    meta: { silentError: true },
  })
  // 表单 state 历史原因散开为多个 useState（而非合成单个 form 对象），故本页不接 useFormDialog，
  // 仅用 FormDialog 统一弹窗壳与 footer；open/editId 仍手动维护
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [quotaUsd, setQuotaUsd] = useState(0) // 以 USD 输入，提交转 micros
  const [modelWhitelist, setModelWhitelist] = useState('')
  const [maxConcurrency, setMaxConcurrency] = useState(0)
  const [editId, setEditId] = useState(0)
  // 选中的渠道 ID 集合（用于 allowed_channels 编辑）
  const [selectedChannels, setSelectedChannels] = useState<Set<number>>(new Set())
  // 弹窗内的归属用户（'0' = 无主）；编辑时记录原值，保存只在变化时调用 owner 接口
  const [ownerId, setOwnerId] = useState('0')
  const [origOwnerId, setOrigOwnerId] = useState('0')

  // 成员管理弹窗：记录目标应用（非空即打开），成员数据由 AppMembersDialog 内部查询
  const [membersApp, setMembersApp] = useState<App | null>(null)

  // 失效对应查询触发重拉（保留原函数名，mutation 后的调用与 RefreshButton 均不用改）
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/apps'] })
  const loadChannels = () => qc.invalidateQueries({ queryKey: ['/admin/channels'] })
  const loadPortalUsers = () => qc.invalidateQueries({ queryKey: ['/admin/portal-users'] })

  // 归属变更：原 catch 只是 toast 错误（且成功/失败都 load() 刷新，无特殊状态保留）→ 删除交全局兜底
  const setOwnerM = useMutation({
    mutationFn: (v: { app: App; userID: number }) => api.post(`/admin/apps/${v.app.id}/owner`, { user_id: v.userID }),
    onSuccess: () => load(),
  })
  function setOwner(app: App, userID: number) {
    setOwnerM.mutate({ app, userID })
  }

  // 将 Set<number> 转为逗号分隔字符串
  function selectedToStr(s: Set<number>) { return [...s].sort((a, b) => a - b).join(',') }
  // 将逗号分隔字符串解析为 Set<number>
  function strToSet(s: string): Set<number> {
    const set = new Set<number>()
    if (!s) return set
    s.split(',').forEach(v => { const n = parseInt(v.trim(), 10); if (!isNaN(n) && n > 0) set.add(n) })
    return set
  }

  function openNew() {
    setName(''); setQuotaUsd(0); setEditId(0)
    setModelWhitelist(''); setMaxConcurrency(0)
    setSelectedChannels(new Set())
    setOwnerId('0'); setOrigOwnerId('0')
    setOpen(true)
  }
  function openEdit(a: App) {
    setName(a.name); setQuotaUsd(microsToUsd(a.quota)); setEditId(a.id)
    setModelWhitelist(a.model_whitelist ?? ''); setMaxConcurrency(a.max_concurrency ?? 0)
    setSelectedChannels(strToSet(a.allowed_channels))
    const o = String(a.owner_user_id ?? 0)
    setOwnerId(o); setOrigOwnerId(o)
    setOpen(true)
  }

  const saveM = useMutation({
    mutationFn: async () => {
      const quota = usdToMicros(quotaUsd)
      const allowed_channels = selectedToStr(selectedChannels)
      let appId = editId
      const body = { name, quota, allowed_channels, model_whitelist: modelWhitelist, max_concurrency: maxConcurrency }
      if (editId) await api.post(`/admin/apps/${editId}`, body)
      else appId = (await api.post<App>('/admin/apps', body)).id
      // 归属变化才调 owner 接口（创建时默认 '0'，选了人才分配）
      if (ownerId !== origOwnerId) await api.post(`/admin/apps/${appId}/owner`, { user_id: Number(ownerId) })
    },
    onSuccess: () => { setOpen(false); setEditId(0); setName(''); setQuotaUsd(0); setSelectedChannels(new Set()); load() },
  })

  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/apps/${id}`),
    onSuccess: () => load(),
  })
  async function del(id: number) {
    if (await confirmDelete('删除该应用？', '其下令牌将失去归属。')) delM.mutate(id)
  }

  const toggleAuditM = useMutation({
    mutationFn: (a: App) => api.post(`/admin/apps/${a.id}/audit`, { audit_enabled: !a.audit_enabled }),
    onSuccess: () => load(),
  })

  // 启停切换：Update 端点 status 为指针字段，只发 { status } 即局部更新
  const toggleM = useMutation({
    mutationFn: (a: App) => api.post(`/admin/apps/${a.id}`, { status: a.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })

  const toggleChannel = (id: number) => setSelectedChannels(prev => toggleInSet(prev, id))

  // 将 allowed_channels 字符串渲染为渠道名标签
  function renderAllowedChannels(allowed: string) {
    if (!allowed) return <span className="text-[var(--muted-foreground)] text-xs">{t('不限')}</span>
    const ids = strToSet(allowed)
    const names = [...ids].map(id => {
      const ch = channels.find(c => c.id === id)
      return ch ? ch.name : `#${id}`
    })
    return <span className="text-xs">{names.join('、')}</span>
  }

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('应用管理')}</h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={() => Promise.all([load(), loadChannels(), loadPortalUsers()])} />
        <Button onClick={openNew}><Plus size={16} />{t('新增应用')}</Button>
      </div>
    </div>
    <Table head={<><Th>ID</Th><Th>{t('名称')}</Th><Th>{t('配额')}</Th><Th>{t('已用')}</Th><Th>{t('允许渠道')}</Th><Th>{t('归属用户')}</Th><Th>{t('状态')}</Th><Th>{t('审计')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{r.id}</Td><Td>{r.name}</Td>
        <Td className="tnum">{r.quota === 0 ? t('不限') : usd(r.quota)}</Td>
        <Td className="tnum">{usd(r.used_quota)}</Td>
        <Td>{renderAllowedChannels(r.allowed_channels)}</Td>
        <Td>
          <Combobox value={String(r.owner_user_id ?? 0)} onChange={v => setOwner(r, Number(v))}
            searchPlaceholder="搜索用户名…"
            options={[
              { value: '0', label: t('无主') },
              ...portalUsers.map(u => ({ value: String(u.id), label: u.username })),
            ]} />
        </Td>
        <Td><StatusPill value={r.status} /></Td>
        <Td>
          <Button variant="ghost" disabled={toggleAuditM.isPending} onClick={() => toggleAuditM.mutate(r)}>
            {r.audit_enabled ? <Badge status="ok">{t('开')}</Badge> : <Badge status="muted">{t('关')}</Badge>}
          </Button>
        </Td>
        <Td className="flex gap-1">
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.status === 1 ? t('停用') : t('启用')}</Button>
          <Button variant="ghost" onClick={() => openEdit(r)}><Pencil size={15} /></Button>
          <Button variant="ghost" onClick={() => setMembersApp(r)}><Users size={15} />{t('成员')}</Button>
          <Button variant="ghost" onClick={() => navigate(`/apps/${r.id}/usage`)}>{t('分析')}</Button>
          <Button variant="ghost" onClick={() => del(r.id)}><Trash2 size={15} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无应用，新增一个以归属令牌与配额')}</Td></tr>}
    </Table>
    <FormDialog open={open} onClose={() => { setOpen(false); setEditId(0) }} title={editId ? t('编辑应用') : t('新增应用')}
      onSubmit={() => saveM.mutate()}
      submitDisabled={!name} submitting={saveM.isPending}>
      <Field label={t('名称')}><Input value={name} onChange={e => setName(e.target.value)} placeholder={t('例如：team-a')} /></Field>
      <Field label={t('配额（USD，0 = 不限）')}><Input type="number" value={quotaUsd} onChange={e => setQuotaUsd(Number(e.target.value))} /></Field>
      <Field label={t('最大并发（同时在途请求数；0 = 按全局默认，-1 = 不限）')}>
        <Input type="number" min={-1} value={maxConcurrency} onChange={e => setMaxConcurrency(Number(e.target.value))} />
      </Field>
      <Field label={t('模型白名单（逗号分隔，空 = 不限；使用方无法修改，对该应用所有令牌生效）')}>
        <Input value={modelWhitelist} onChange={e => setModelWhitelist(e.target.value)} placeholder={t('例如：gpt-4o-mini,claude-haiku')} />
      </Field>
      <Field label={t('归属用户（使用方门户可见）')}>
        <Combobox className="w-full" value={ownerId} onChange={setOwnerId}
          searchPlaceholder="搜索用户名…"
          options={[
            { value: '0', label: t('无主') },
            ...portalUsers.map(u => ({ value: String(u.id), label: u.username })),
          ]} />
      </Field>
      <Field label={t('允许渠道（空 = 不限）')}>
        {channels.length === 0
          ? <span className="text-xs text-[var(--muted-foreground)]">{t('暂无渠道')}</span>
          : <>
              <div className="mb-1 flex items-center gap-3 text-xs">
                <button type="button" className="text-[var(--primary)] hover:underline" onClick={() => setSelectedChannels(new Set(channels.map(c => c.id)))}>{t('全选')}</button>
                <button type="button" className="text-[var(--primary)] hover:underline" onClick={() => setSelectedChannels(new Set())}>{t('清空')}</button>
                <button type="button" className="text-[var(--primary)] hover:underline" onClick={() => setSelectedChannels(prev => new Set(channels.filter(c => !prev.has(c.id)).map(c => c.id)))}>{t('反选')}</button>
                <span className="ml-auto text-[var(--muted-foreground)]">已选 {selectedChannels.size}/{channels.length}</span>
              </div>
              <div className="max-h-40 overflow-y-auto border border-[var(--border)] rounded p-2 space-y-1">
                {channels.map(ch => (
                  <label key={ch.id} className="flex items-center gap-2 cursor-pointer text-sm hover:bg-[var(--muted)] rounded px-1 py-0.5">
                    <Checkbox
                      checked={selectedChannels.has(ch.id)}
                      onCheckedChange={() => toggleChannel(ch.id)}
                    />
                    <span>{ch.name}</span>
                    <span className="text-[var(--muted-foreground)] text-xs">({ch.type})</span>
                  </label>
                ))}
              </div>
            </>
        }
      </Field>
    </FormDialog>

    {/* 应用成员管理：列出/添加/移除成员，owner 不可移除 */}
    <AppMembersDialog app={membersApp} onClose={() => setMembersApp(null)} portalUsers={portalUsers} />
  </div>
}
