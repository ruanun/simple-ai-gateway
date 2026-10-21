import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Field } from '../ui/field'
import { Dialog } from '../ui/dialog'
import { FormDialog } from '../ui/form-dialog'
import { Table, Th, Td } from '../ui/table'
import { confirmDelete } from '../ui/confirm'
import { Plus, Trash2 } from 'lucide-react'
import type { ModelPrice } from '../../lib/types'
import { useI18n } from '../../lib/i18n'

type Props = {
  // 目标渠道 id，0 表示关闭
  channelId: number
  onClose: () => void
}

// 渠道覆盖价管理弹窗：覆盖价列表 + 新增覆盖价，状态与数据拉取自持
export function ChannelPricesDialog({ channelId, onClose }: Props) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: chPrices = [] } = useQuery({
    queryKey: ['/admin/prices', { channel_id: channelId }],
    queryFn: () => api.get<ModelPrice[]>(`/admin/prices?channel_id=${channelId}`),
    enabled: channelId !== 0,
  })
  const reload = () => qc.invalidateQueries({ queryKey: ['/admin/prices', { channel_id: channelId }] })
  const [cpOpen, setCpOpen] = useState(false)
  const [cpForm, setCpForm] = useState({ model: '', input_per_1m: 0, output_per_1m: 0, cached_input_per_1m: 0, cache_write_per_1m: 0 })

  const addChPriceM = useMutation({
    mutationFn: (payload: typeof cpForm) => api.post('/admin/prices', {
      channel_id: channelId, model: payload.model,
      input_per_1m: Number(payload.input_per_1m), output_per_1m: Number(payload.output_per_1m), cached_input_per_1m: Number(payload.cached_input_per_1m), cache_write_per_1m: Number(payload.cache_write_per_1m),
      currency: 'USD', enabled: true, type: 'tokens',
    }),
    onSuccess: () => { setCpOpen(false); setCpForm({ model: '', input_per_1m: 0, output_per_1m: 0, cached_input_per_1m: 0, cache_write_per_1m: 0 }); reload() },
  })
  const delChPriceM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/prices/${id}`),
    onSuccess: () => reload(),
  })
  async function delChPrice(id: number) {
    if (await confirmDelete(t('删除该渠道覆盖价？'))) delChPriceM.mutate(id)
  }

  return <>
    {/* 渠道价格 Dialog */}
    <Dialog open={channelId !== 0} onClose={onClose} title={`渠道价格(覆盖全局官方价) · 渠道 #${channelId}`} width="max-w-2xl">
      <div className="space-y-3">
        <div className="flex justify-end">
          <Button onClick={() => setCpOpen(true)}><Plus size={15} />{t('加覆盖价')}</Button>
        </div>
        <div className="max-h-[60vh] overflow-y-auto">
          <Table head={<><Th>ID</Th><Th>{t('模型')}</Th><Th>{t('输入')}</Th><Th>{t('输出')}</Th><Th>{t('缓存输入')}</Th><Th>{t('缓存写')}</Th><Th>{t('操作')}</Th></>}>
            {chPrices.map(p => <tr key={p.id} className="hover:bg-[var(--muted)]">
              <Td className="tnum">{p.id}</Td><Td className="tnum">{p.model}</Td>
              <Td className="tnum">{p.input_per_1m}</Td><Td className="tnum">{p.output_per_1m}</Td><Td className="tnum">{p.cached_input_per_1m || 0}</Td>
              <Td className="tnum">{p.cache_write_per_1m || 0}</Td>
              <Td><Button variant="ghost" onClick={() => delChPrice(p.id)}><Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" /></Button></Td>
            </tr>)}
            {chPrices.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('未覆盖，按全局官方价计费')}</Td></tr>}
          </Table>
        </div>
      </div>
    </Dialog>

    {/* 新增覆盖价 Dialog */}
    <FormDialog open={cpOpen} onClose={() => setCpOpen(false)} title={t('新增渠道覆盖价')}
      onSubmit={() => addChPriceM.mutate(cpForm)}
      submitDisabled={!cpForm.model} submitting={addChPriceM.isPending}>
      <Field label={t('模型名')}><Input value={cpForm.model} onChange={e => setCpForm({ ...cpForm, model: e.target.value })} placeholder="gpt-4o" /></Field>
      <div className="grid grid-cols-3 gap-3">
        <Field label={t('输入价(USD/1M)')}><Input type="number" value={cpForm.input_per_1m} onChange={e => setCpForm({ ...cpForm, input_per_1m: Number(e.target.value) })} /></Field>
        <Field label={t('输出价(USD/1M)')}><Input type="number" value={cpForm.output_per_1m} onChange={e => setCpForm({ ...cpForm, output_per_1m: Number(e.target.value) })} /></Field>
        <Field label={t('缓存输入价(USD/1M)')}><Input type="number" value={cpForm.cached_input_per_1m} onChange={e => setCpForm({ ...cpForm, cached_input_per_1m: Number(e.target.value) })} /></Field>
        <Field label={t('缓存写价(USD/1M)')}><Input type="number" value={cpForm.cache_write_per_1m} onChange={e => setCpForm({ ...cpForm, cache_write_per_1m: Number(e.target.value) })} /></Field>
      </div>
    </FormDialog>
  </>
}
