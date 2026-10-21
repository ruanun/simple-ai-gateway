import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Field } from '../ui/field'
import { Dialog } from '../ui/dialog'
import { FormDialog } from '../ui/form-dialog'
import { Table, Th, Td } from '../ui/table'
import { StatusPill } from '../StatusPill'
import { toast } from '../ui/sonner'
import { confirmDelete } from '../ui/confirm'
import { Checkbox } from '../ui/checkbox'
import { CheckboxPickList } from '../CheckboxPickList'
import { toggleInSet } from '../../lib/utils'
import { Tip } from '../ui/tooltip'
import { Plus, Trash2, RefreshCw } from 'lucide-react'
import { AliasBatchDialog } from './AliasBatchDialog'
import type { ChannelModel, ModelPrice } from '../../lib/types'
import { useI18n } from '../../lib/i18n'

type Props = {
  // 目标渠道 id，0 表示关闭
  channelId: number
  onClose: () => void
}

// 渠道模型管理弹窗：模型列表（搜索/批删）+ 拉取上游导入 + 手动添加 + 批量建规范名别名入口，状态与数据拉取自持
export function ChannelModelsDialog({ channelId, onClose }: Props) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: chModels = [] } = useQuery({
    queryKey: ['/admin/channel-models', { channel_id: channelId }],
    queryFn: () => api.get<ChannelModel[]>(`/admin/channel-models?channel_id=${channelId}`),
    enabled: channelId !== 0,
  })
  const reload = () => qc.invalidateQueries({ queryKey: ['/admin/channel-models', { channel_id: channelId }] })
  const [cmOpen, setCmOpen] = useState(false)
  const [cmModel, setCmModel] = useState('')
  const [modelSearch, setModelSearch] = useState('')
  const [delSet, setDelSet] = useState<Set<number>>(new Set())
  // 拉取上游模型相关状态（导入清单的搜索词由 CheckboxPickList 自持，清空拉取结果即随卸载复位）
  const [fetchedModels, setFetchedModels] = useState<string[]>([])
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set())
  const [aliasBatchOpen, setAliasBatchOpen] = useState(false)

  // 全局已定价模型集合：用于「⚠未定价」标注（与主页面共享同一查询缓存）
  const { data: allPrices = [] } = useQuery({ queryKey: ['/admin/prices'], queryFn: () => api.get<Pick<ModelPrice, 'model' | 'channel_id'>[]>('/admin/prices') })
  const pricedModels = useMemo(() => new Set(allPrices.filter(p => p.channel_id === 0).map(p => p.model)), [allPrices])

  // 清空拉取结果/搜索/批删选中等瞬态视图态（渠道切换与关闭两处共用）
  function resetTransient() {
    setFetchedModels([]); setSelectedModels(new Set()); setModelSearch(''); setDelSet(new Set())
  }
  // 切换渠道时复位瞬态（对齐原打开时的清理行为；数据本身已由 useQuery 按 channelId 分 key）。
  // 用"渲染期间比较+同步 setState"而非 useEffect：避免多一次级联重渲（React 官方推荐的按 prop 变化调整状态的写法）。
  const [prevChannelId, setPrevChannelId] = useState(channelId)
  if (channelId !== prevChannelId) {
    setPrevChannelId(channelId)
    resetTransient()
  }
  // 关闭时复位瞬态（对齐原 onClose 行为）
  function handleClose() {
    resetTransient()
    onClose()
  }

  const addChModelM = useMutation({
    mutationFn: (model: string) => api.post('/admin/channel-models', { channel_id: channelId, model, enabled: true }),
    onSuccess: () => { setCmOpen(false); setCmModel(''); reload() },
  })
  const delChModelM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/channel-models/${id}`),
    onSuccess: () => reload(),
  })
  async function delChModel(id: number) {
    if (await confirmDelete(t('删除该模型？'))) delChModelM.mutate(id)
  }
  const batchDelModelsM = useMutation({
    mutationFn: async (ids: number[]) => { for (const id of ids) await api.del(`/admin/channel-models/${id}`) },
    onSuccess: () => { setDelSet(new Set()); reload() },
  })
  async function batchDelModels() {
    if (delSet.size === 0 || !(await confirmDelete(`确认删除选中的 ${delSet.size} 个模型？`))) return
    batchDelModelsM.mutate([...delSet])
  }
  const toggleDel = (id: number) => setDelSet(toggleInSet(delSet, id))
  const filteredModels = chModels.filter(m => !modelSearch || m.model.toLowerCase().includes(modelSearch.toLowerCase()))

  // 拉取上游模型：失败仅是 toast 通用错误 → 交给全局兜底（能展示后端具体原因）；成功后的过滤/默认全选/无新模型提示属正常业务逻辑，留在 onSuccess
  const fetchUpstreamM = useMutation({
    mutationFn: () => api.post<{ models: string[] }>(`/admin/channels/${channelId}/fetch-models`, {}),
    onSuccess: (r) => {
      const existSet = new Set(chModels.map(m => m.model))
      const newModels = r.models.filter(m => !existSet.has(m))
      setFetchedModels(newModels)
      setSelectedModels(new Set(newModels)) // 默认全选
      // 上游返回空列表是合法结果（账号可能一个模型都没开通），与「拉到了但都已导入」区分提示
      if (r.models.length === 0) toast('上游未返回任何模型')
      else if (newModels.length === 0) toast('无新模型可导入')
    },
  })
  const importSelectedM = useMutation({
    mutationFn: async (models: string[]) => { for (const m of models) await api.post('/admin/channel-models', { channel_id: channelId, model: m, enabled: true }) },
    onSuccess: () => { setFetchedModels([]); setSelectedModels(new Set()); reload() },
  })
  function importSelected() { importSelectedM.mutate([...selectedModels]) }

  // 模型启停：走独立 status 端点（写后服务端热刷路由表），成功即失效列表查询重拉
  const toggleM = useMutation({
    mutationFn: (m: ChannelModel) => api.post(`/admin/channel-models/${m.id}/status`, { status: m.enabled ? 0 : 1 }),
    onSuccess: () => reload(),
  })

  return <>
    {/* 渠道模型 Dialog */}
    <Dialog open={channelId !== 0} onClose={handleClose} title={`渠道模型 · 渠道 #${channelId}`} width="max-w-2xl">
      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <Input placeholder={t('搜索模型名...')} value={modelSearch} onChange={e => setModelSearch(e.target.value)} className="flex-1" />
          <Button variant="outline" onClick={() => fetchUpstreamM.mutate()} disabled={fetchUpstreamM.isPending}><RefreshCw size={15} />{fetchUpstreamM.isPending ? t('拉取中...') : t('拉取上游')}</Button>
          <Tip content="识别版本后缀，批量生成去版本号的规范名别名"><Button variant="outline" onClick={() => setAliasBatchOpen(true)} disabled={chModels.length === 0}>{t('批量建别名')}</Button></Tip>
          <Button onClick={() => setCmOpen(true)}><Plus size={15} />{t('加模型')}</Button>
          {delSet.size > 0 && <Button variant="outline" onClick={batchDelModels} disabled={batchDelModelsM.isPending} className="text-[var(--destructive)]"><Trash2 size={15} />删除({delSet.size})</Button>}
        </div>
        <div className="max-h-[40vh] overflow-y-auto">
          <Table head={<><Th><Checkbox checked={filteredModels.length > 0 && filteredModels.every(m => delSet.has(m.id))} onCheckedChange={() => { if (filteredModels.every(m => delSet.has(m.id))) setDelSet(new Set()); else setDelSet(new Set(filteredModels.map(m => m.id))) }} /></Th><Th>{t('模型')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
            {filteredModels.map(m => <tr key={m.id} className="hover:bg-[var(--muted)]">
              <Td><Checkbox checked={delSet.has(m.id)} onCheckedChange={() => toggleDel(m.id)} /></Td>
              <Td className="tnum">{m.model}{!pricedModels.has(m.model) && <Tip content={t('未配价格,请求会被拒绝(400)')}><span className="ml-1 text-xs text-[var(--destructive)]">⚠未定价</span></Tip>}</Td>
              <Td><StatusPill value={m.enabled} /></Td>
              <Td className="flex gap-1">
                <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(m)}>{m.enabled ? t('停用') : t('启用')}</Button>
                <Button variant="ghost" onClick={() => delChModel(m.id)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button>
              </Td>
            </tr>)}
            {filteredModels.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{chModels.length === 0 ? t('暂无模型，点「拉取上游」或手动添加') : t('无匹配模型')}</Td></tr>}
          </Table>
        </div>
        {fetchedModels.length > 0 && (
          <div className="space-y-2 border-t border-[var(--border)] pt-3">
            <CheckboxPickList items={fetchedModels} selected={selectedModels} onSelectedChange={setSelectedModels}
              getKey={m => m}
              countText={`${fetchedModels.length} 个新模型，已选 ${selectedModels.size}`}
              searchPlaceholder="搜索上游模型..."
              actionText="导入选中" actionDisabled={selectedModels.size === 0 || importSelectedM.isPending} onAction={importSelected} />
          </div>
        )}
      </div>
    </Dialog>

    {/* 手动添加模型 Dialog */}
    <FormDialog open={cmOpen} onClose={() => setCmOpen(false)} title={t('手动添加模型')}
      onSubmit={() => addChModelM.mutate(cmModel)}
      submitDisabled={!cmModel} submitting={addChModelM.isPending}>
      <Field label={t('模型名')}><Input value={cmModel} onChange={e => setCmModel(e.target.value)} placeholder="gpt-4o" /></Field>
    </FormDialog>

    {/* 批量建规范名别名 Dialog（模型列表经 props 显式传入） */}
    <AliasBatchDialog open={aliasBatchOpen} onClose={() => setAliasBatchOpen(false)} channelId={channelId} models={chModels.map(m => m.model)} />
  </>
}
