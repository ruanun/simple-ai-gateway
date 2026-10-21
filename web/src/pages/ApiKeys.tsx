import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usd, usdToMicros, microsToUsd } from '../lib/format'
import { Button } from '../components/ui/button'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { confirmDelete, promptDialog } from '../components/ui/confirm'
import { Tip } from '../components/ui/tooltip'
import { useFormDialog } from '../components/ui/form-dialog'
import { Plus, Trash2, Copy } from 'lucide-react'
import type { AdminApiKey as Key } from '../lib/types'
import type { Tag, KeyItem, AppOption, ApiKeyForm } from '../components/apikeys/types'
import { ApiKeyFormDialog } from '../components/apikeys/ApiKeyFormDialog'
import { KeyTagsDialog } from '../components/apikeys/KeyTagsDialog'
import { PlainKeyDialog } from '../components/apikeys/PlainKeyDialog'
import { useI18n } from '../lib/i18n'

// 统一表单的空值
const emptyForm: ApiKeyForm = {
  name: '',
  app_id: 0,
  rpm: 0,
  daily_cost_usd: 0,
  whitelist: [],
  mappings: [],
}

export function ApiKeys() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [tagFilter, setTagFilter] = useState(0)

  // 列表数据统一走 TanStack Query（挂载即拉取，同 key 跨页面共享缓存）
  const { data: rows = [] } = useQuery({
    queryKey: ['/admin/api-keys', { tag_id: tagFilter }],
    queryFn: () => api.get<KeyItem[]>(`/admin/api-keys${tagFilter ? `?tag_id=${tagFilter}` : ''}`),
    placeholderData: keepPreviousData, // 切换标签筛选时保留上一份数据，避免闪空
  })
  const { data: apps = [] } = useQuery({ queryKey: ['/admin/apps'], queryFn: () => api.get<AppOption[]>('/admin/apps') })
  const { data: allTags = [] } = useQuery({ queryKey: ['/admin/key-tags'], queryFn: () => api.get<Tag[]>('/admin/key-tags') })

  // 密钥明文显示弹窗（创建后一次性）
  const [plain, setPlain] = useState<string | null>(null)

  // 统一新增/编辑弹窗（editId=0 表示新增，>0 表示编辑对应 id）
  const { open: formOpen, editId, form, setForm, openNew: openFormNew, openEdit: openFormEdit, close: closeFormBase } = useFormDialog<ApiKeyForm>(emptyForm)

  // 白名单搜索（随弹窗开合手动同步重置）
  const [whitelistSearch, setWhitelistSearch] = useState('')

  // 标签绑定弹窗（弹窗内已绑标签直接从 rows 派生，绑/解绑失效刷新后自动更新）
  const [tagKey, setTagKey] = useState<Key | null>(null)
  const keyTags = tagKey ? (rows.find(r => r.key.id === tagKey.id)?.tags ?? []) : []

  // 失效对应查询触发重拉（保留原函数名，mutation 后的调用与 RefreshButton 均不用改）
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/api-keys'] })
  const loadTags = () => qc.invalidateQueries({ queryKey: ['/admin/key-tags'] })
  const createTagM = useMutation({
    mutationFn: (name: string) => api.post('/admin/key-tags', { name }),
    onSuccess: () => loadTags(),
  })

  // 可用模型 = 渠道模型 + 别名（用于白名单 checkbox），由两个查询派生
  const { data: cms = [] } = useQuery({ queryKey: ['/admin/channel-models'], queryFn: () => api.get<{ id: number; model: string }[]>('/admin/channel-models') })
  const { data: als = [] } = useQuery({ queryKey: ['/admin/aliases'], queryFn: () => api.get<{ id: number; alias: string }[]>('/admin/aliases') })
  const availableModels = useMemo(() => {
    const set = new Set<string>()
    cms.forEach(m => set.add(m.model))
    als.forEach(a => set.add(a.alias))
    return [...set].sort()
  }, [cms, als])

  // 打开新增弹窗
  function openNew() {
    setWhitelistSearch('')
    openFormNew()
  }

  // 打开编辑弹窗，预填所有字段
  function openEdit(k: Key) {
    const mappings = (() => {
      try {
        return Object.entries(JSON.parse(k.model_mappings || '{}')).map(
          ([from, to]) => ({ from, to: to as string })
        )
      } catch {
        return []
      }
    })()
    setWhitelistSearch('')
    openFormEdit(k.id, {
      name: k.name,
      app_id: 0, // 编辑时 app 不可更改，占位
      rpm: k.rpm || 0,
      daily_cost_usd: microsToUsd(k.daily_cost_limit || 0),
      whitelist: (k.model_whitelist || '').split(',').filter(Boolean),
      mappings,
    })
  }

  // 关闭弹窗
  function closeForm() {
    setWhitelistSearch('')
    closeFormBase()
  }

  // 保存（新增 or 编辑）：分支逻辑读当前 form/editId 闭包态，成功后统一收尾
  const saveM = useMutation({
    mutationFn: async () => {
      const whitelistStr = form.whitelist.join(',')
      const mappingsStr = JSON.stringify(
        Object.fromEntries(
          form.mappings.filter(m => m.from && m.to).map(m => [m.from, m.to])
        )
      )

      if (editId) {
        // 编辑：直接更新全部治理字段
        await api.post(`/admin/api-keys/${editId}`, {
          daily_cost_limit: usdToMicros(form.daily_cost_usd),
          model_whitelist: whitelistStr,
          model_mappings: mappingsStr === '{}' ? '' : mappingsStr,
          rpm: Number(form.rpm),
        })
        return null
      }
      // 新增：先创建，再看是否需要更新治理字段
      const r = await api.post<{ key: string; record: { id: number } }>(
        '/admin/api-keys',
        { name: form.name, app_id: Number(form.app_id), rpm: Number(form.rpm) }
      )
      const hasGov =
        form.daily_cost_usd > 0 ||
        whitelistStr !== '' ||
        (mappingsStr !== '{}' && mappingsStr !== '')
      if (hasGov && r.record?.id) {
        await api.post(`/admin/api-keys/${r.record.id}`, {
          daily_cost_limit: usdToMicros(form.daily_cost_usd),
          model_whitelist: whitelistStr,
          model_mappings: mappingsStr === '{}' ? '' : mappingsStr,
        })
      }
      return r.key
    },
    onSuccess: (key) => {
      if (key) setPlain(key)
      closeForm()
      load()
    },
  })

  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/api-keys/${id}`),
    onSuccess: () => load(),
  })
  async function del(id: number) {
    if (await confirmDelete(t('确认删除该令牌？'))) delM.mutate(id)
  }

  function openTags(k: Key) { setTagKey(k) }
  // bind/unbind 后失效查询即可，弹窗内 keyTags 由最新 rows 派生自动更新
  const bindTagM = useMutation({
    mutationFn: (tagID: number) => api.post(`/admin/api-keys/${tagKey!.id}/tags`, { tag_id: tagID }),
    onSuccess: () => load(),
  })
  function bindTag(tagID: number) {
    if (!tagKey) return
    bindTagM.mutate(tagID)
  }
  const unbindTagM = useMutation({
    mutationFn: (tagID: number) => api.del(`/admin/api-keys/${tagKey!.id}/tags/${tagID}`),
    onSuccess: () => load(),
  })
  function unbindTag(tagID: number) {
    if (!tagKey) return
    unbindTagM.mutate(tagID)
  }

  // 启停切换：Update 端点 status 为指针字段，只发 { status } 即局部更新
  const toggleM = useMutation({
    mutationFn: (k: Key) => api.post(`/admin/api-keys/${k.id}`, { status: k.status === 1 ? 0 : 1 }),
    onSuccess: () => load(),
  })

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{t('令牌管理')}</h2>
      <div className="flex items-center gap-2">
        <RefreshButton onRefresh={() => Promise.all([load(), loadTags()])} />
        <SimpleSelect value={String(tagFilter)} onChange={v => setTagFilter(Number(v))}
          options={[
            { value: '0', label: t('全部标签') },
            ...allTags.map(t => ({ value: String(t.id), label: t.name })),
          ]} />
        <Button variant="outline" disabled={createTagM.isPending} onClick={async () => {
          const n = await promptDialog({ title: t('新标签名') })
          if (n) createTagM.mutate(n)
        }}>{t('新建标签')}</Button>
        <Button onClick={openNew}><Plus size={16} />{t('新增令牌')}</Button>
      </div>
    </div>

    <Table head={<><Th>ID</Th><Th>{t('名称')}</Th><Th>{t('应用')}</Th><Th>{t('前缀')}</Th><Th>RPM</Th><Th>{t('日成本')}</Th><Th>{t('白名单')}</Th><Th>{t('标签')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
      {rows.map(({ key: k, tags }) => <tr key={k.id} className="hover:bg-[var(--muted)]">
        <Td className="tnum">{k.id}</Td><Td>{k.name}</Td>
        <Td className="text-[var(--muted-foreground)]">{apps.find(a => a.id === k.app_id)?.name || (k.app_id ? `#${k.app_id}` : '—')}</Td>
        <Td className="tnum">{k.prefix}…{k.key
          ? <Tip content={t('复制令牌')}><button className="ml-1 cursor-pointer align-middle text-[var(--muted-foreground)] hover:text-[var(--foreground)]"
              onClick={() => navigator.clipboard.writeText(k.key!)}><Copy size={13} /></button></Tip>
          : <Tip content={t('旧令牌未存明文，删除重建后可回查')}><span className="ml-1 text-xs text-[var(--muted-foreground)]">{t('不可回查')}</span></Tip>}</Td>
        <Td className="tnum">{k.rpm === -1 ? t('不限') : k.rpm || t('默认')}</Td>
        <Td className="tnum">{k.daily_cost_limit ? `${usd(k.daily_cost_used || 0)}/${usd(k.daily_cost_limit)}` : t('不限')}</Td>
        <Td className="max-w-[140px] truncate">{k.model_whitelist || t('不限')}</Td>
        <Td>
          {tags.map(t => <span key={t.id} className="mr-1 rounded bg-[var(--muted)] px-1.5 py-0.5 text-xs">{t.name}</span>)}
          {tags.length === 0 && <span className="text-xs text-[var(--muted-foreground)]">—</span>}
        </Td>
        <Td>
          <StatusPill value={k.status} />
          {k.status !== 1 && k.disabled_by_admin && (
            <Tip content={t('管理员停用：使用方无法在门户重新启用')}><span className="ml-1 text-xs text-[var(--muted-foreground)]">{t('已锁定')}</span></Tip>
          )}
        </Td>
        <Td className="flex gap-1">
          <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(k)}>{k.status === 1 ? t('停用') : t('启用')}</Button>
          <Button variant="ghost" onClick={() => openEdit(k)}>{t('编辑')}</Button>
          <Button variant="ghost" onClick={() => openTags(k)}>{t('标签')}</Button>
          <Button variant="ghost" onClick={() => navigate(`/logs?api_key_id=${k.id}`)}>{t('日志')}</Button>
          <Button variant="ghost" onClick={() => navigate(`/api-keys/${k.id}/usage`)}>{t('分析')}</Button>
          <Button variant="ghost" onClick={() => del(k.id)}><Trash2 size={15} className="text-[var(--muted-foreground)] transition-colors" /></Button>
        </Td>
      </tr>)}
      {rows.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无令牌')}</Td></tr>}
    </Table>

    {/* 统一新增/编辑弹窗 */}
    <ApiKeyFormDialog
      open={formOpen} onClose={closeForm} editId={editId}
      form={form} setForm={setForm} onSubmit={() => saveM.mutate()} submitting={saveM.isPending}
      apps={apps} availableModels={availableModels}
      whitelistSearch={whitelistSearch} setWhitelistSearch={setWhitelistSearch}
    />

    {/* 标签绑定弹窗 */}
    <KeyTagsDialog
      keyItem={tagKey} onClose={() => setTagKey(null)}
      keyTags={keyTags} allTags={allTags}
      onBind={bindTag} onUnbind={unbindTag}
      bindPending={bindTagM.isPending} unbindPending={unbindTagM.isPending}
    />

    {/* 密钥明文显示弹窗（创建成功后一次性显示） */}
    <PlainKeyDialog plainKey={plain} onClose={() => setPlain(null)} />
  </div>
}
