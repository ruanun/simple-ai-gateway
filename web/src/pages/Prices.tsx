import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { usdToMicros, microsToUsd } from '../lib/format'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { useFormDialog, FormDialog } from '../components/ui/form-dialog'
import { Table, Th, Td } from '../components/ui/table'
import { StatusPill } from '../components/StatusPill'
import { RefreshButton } from '../components/ui/refresh'
import { SimpleSelect } from '../components/ui/select'
import { confirmDelete } from '../components/ui/confirm'
import { CheckboxPickList } from '../components/CheckboxPickList'
import { Plus, Trash2, RefreshCw, Pencil } from 'lucide-react'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '../components/ui/collapsible'
import { PaginationBar } from '../components/ui/pagination'
import type { ModelPrice, PriceSuggestion } from '../lib/types'
import { useI18n } from '../lib/i18n'

const emptyForm = { model: '', input_per_1m: 0, output_per_1m: 0, cached_input_per_1m: 0, cache_write_per_1m: 0, type: 'tokens', per_call_usd: 0, enabled: true }

export function Prices() {
  const { t } = useI18n()
  const qc = useQueryClient()
  // 价格列表走 TanStack Query（挂载即拉取；搜索/分页为纯前端过滤切片，不进 queryKey）
  const { data: rows = [] } = useQuery({ queryKey: ['/admin/prices'], queryFn: () => api.get<ModelPrice[]>('/admin/prices') })
  const fd = useFormDialog(emptyForm)
  const [lookupResults, setLookupResults] = useState<{model:string;provider:string;input:number;output:number;cache_read:number;cache_write:number}[]>([])
  const [suggestions, setSuggestions] = useState<PriceSuggestion[]>([])
  const [syncSelected, setSyncSelected] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const [pageSize, setPageSize] = useState(20)

  // 失效查询触发重拉（保留原函数名，mutation 后的 load() 与 RefreshButton 均不用改）
  const load = () => qc.invalidateQueries({ queryKey: ['/admin/prices'] })
  function openEdit(p: ModelPrice) {
    fd.openEdit(p.id, { model: p.model, input_per_1m: p.input_per_1m, output_per_1m: p.output_per_1m, cached_input_per_1m: p.cached_input_per_1m, cache_write_per_1m: p.cache_write_per_1m, type: p.type, per_call_usd: microsToUsd(p.per_call_micros), enabled: p.enabled })
  }
  const saveM = useMutation({
    mutationFn: () => api.post('/admin/prices', {
      model: fd.form.model,
      input_per_1m: Number(fd.form.input_per_1m),
      output_per_1m: Number(fd.form.output_per_1m),
      cached_input_per_1m: Number(fd.form.cached_input_per_1m),
      cache_write_per_1m: Number(fd.form.cache_write_per_1m),
      currency: 'USD',
      enabled: fd.form.enabled,
      type: fd.form.type,
      per_call_micros: usdToMicros(fd.form.per_call_usd),
    }),
    onSuccess: () => { fd.close(); setLookupResults([]); load() },
  })

  // 查询价格建议：原 catch 无 toast、只静默清空结果（特殊交互）→ meta.silentError + 局部 onError 保留该行为
  const lookupM = useMutation({
    mutationFn: (query: string) => api.post<{matches: typeof lookupResults}>('/admin/prices/lookup-model', { query }),
    onSuccess: (r) => setLookupResults(r.matches || []),
    onError: () => setLookupResults([]),
    meta: { silentError: true },
  })
  function lookupPrice() {
    if (!fd.form.model) return
    lookupM.mutate(fd.form.model)
  }

  // 从 models.dev 同步：原 catch 只是 toast 错误（'拉取失败'）→ 删除交全局兜底
  const fetchModelsDevM = useMutation({
    mutationFn: () => api.post<{ suggestions: PriceSuggestion[]; matched: number; total_upstream: number }>('/admin/prices/sync-models-dev', {}),
    onSuccess: (r) => {
      setSuggestions(r.suggestions || [])
      // 默认选中未定价的
      setSyncSelected(new Set((r.suggestions || []).filter(s => !s.has_price).map(s => s.model)))
    },
  })

  const importPricesM = useMutation({
    mutationFn: async (items: PriceSuggestion[]) => {
      for (const s of items) {
        await api.post('/admin/prices', {
          model: s.model, input_per_1m: s.input, output_per_1m: s.output,
          cached_input_per_1m: s.cache_read, cache_write_per_1m: s.cache_write, currency: 'USD', enabled: true, type: 'tokens',
        })
      }
    },
    onSuccess: () => { setSuggestions([]); setSyncSelected(new Set()); load() },
  })
  function importPrices() {
    importPricesM.mutate(suggestions.filter(s => syncSelected.has(s.model)))
  }

  const delM = useMutation({
    mutationFn: (id: number) => api.del(`/admin/prices/${id}`),
    onSuccess: () => load(),
  })
  async function del(id: number) { if (await confirmDelete(t('删除该价格？'))) delM.mutate(id) }

  // 启停切换：POST /admin/prices 是 Upsert（按 channel_id+model 冲突键），需整行提交、只翻转 enabled
  const toggleM = useMutation({
    mutationFn: (p: ModelPrice) => api.post('/admin/prices', { ...p, enabled: !p.enabled }),
    onSuccess: () => load(),
  })

  // 渠道页「N未定价」跳转带 ?sync=1：挂载后自动触发一次 models.dev 同步（ref 防 StrictMode 重复触发）
  const autoSynced = useRef(false)
  useEffect(() => {
    if (autoSynced.current || new URLSearchParams(window.location.search).get('sync') !== '1') return
    autoSynced.current = true
    fetchModelsDevM.mutate()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅挂载时按 URL 参数触发一次，mutate 引用不参与依赖
  }, [])

  return <div className="space-y-4">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">价格管理 <span className="text-xs font-normal text-[var(--muted-foreground)]">USD / 1M token</span></h2>
      <div className="flex gap-2">
        <RefreshButton onRefresh={load} />
        <Button variant="outline" onClick={() => fetchModelsDevM.mutate()} disabled={fetchModelsDevM.isPending}>
          <RefreshCw size={15} />{fetchModelsDevM.isPending ? t('拉取中...') : t('从 models.dev 同步')}
        </Button>
        <Button onClick={fd.openNew}><Plus size={16} />{t('配置价格')}</Button>
      </div>
    </div>
    <Collapsible className="text-xs text-[var(--muted-foreground)] border border-[var(--border)] rounded-md p-3">
      <CollapsibleTrigger className="font-semibold">计费完整链路（模型匹配 → 价格解析 → 成本计算）</CollapsibleTrigger>
      <CollapsibleContent className="mt-2 space-y-1.5 leading-relaxed">
        <p className="font-semibold">一、请求模型名解析</p>
        <p className="pl-3">1. 客户端请求携带 model 名（如 <code>gpt-4</code>、<code>smart</code>）</p>
        <p className="pl-3">2. <b>令牌模型映射</b>：若令牌配了映射（如 gpt-4→gpt-4o），先改写模型名</p>
        <p className="pl-3">3. <b>令牌模型白名单</b>：改写后的模型不在白名单内 → 403 拒绝</p>
        <p className="pl-3">4. <b>令牌日成本上限</b>：当日累计超限 → 402 拒绝</p>
        <p className="pl-3">5. <b>路由解析</b>：用改写后的模型名查路由表 → 得到候选渠道列表（别名自动解析为目标模型）</p>
        <p className="font-semibold mt-2">二、价格匹配（在路由解析后、派发前校验）</p>
        <p className="pl-3">· 用候选的<b>真实模型名</b>（路由目标,非请求名/别名名）查价格</p>
        <p className="pl-3">· 查价优先级：<b>渠道覆盖价</b>（渠道定价面板配的,按渠道×模型）→ 回退<b>全局价</b>（本页配的,channel_id=0）</p>
        <p className="pl-3">· <b>未配价格 → 400 拒绝请求</b>（避免上游已消耗但不计费）</p>
        <p className="font-semibold mt-2">三、成本计算（派发成功后）</p>
        <p className="pl-3">· 按 token 计费：<code>成本 = (input_tokens × 输入价 + output_tokens × 输出价) × 渠道倍率</code></p>
        <p className="pl-3">· 按次计费：<code>成本 = 每次额度 × 渠道倍率</code></p>
        <p className="pl-3">· 成本同时扣减：应用配额 + 令牌日成本 + 落入日/时用量聚合</p>
        <p className="font-semibold mt-2">四、价格数据来源</p>
        <p className="pl-3">· 手动配置（本页「配置价格」按钮）或从 <b>models.dev</b> 一键同步（开源社区维护的实时官方价）</p>
      </CollapsibleContent>
    </Collapsible>

    {suggestions.length > 0 && (
      <div className="space-y-2 border border-[var(--border)] rounded-md p-3">
        <CheckboxPickList items={suggestions} selected={syncSelected} onSelectedChange={setSyncSelected}
          getKey={s => s.model}
          countText={`匹配 ${suggestions.length} 个，已选 ${syncSelected.size}`}
          searchPlaceholder="搜索模型..."
          actionText="导入选中" actionDisabled={syncSelected.size === 0 || importPricesM.isPending} onAction={importPrices}
          extraActions={<Button variant="outline" onClick={() => { setSuggestions([]); setSyncSelected(new Set()) }}>{t('关闭')}</Button>}
          renderExtra={s => <>
            <span className="tnum text-xs text-[var(--muted-foreground)]">{s.input}/{s.output}</span>
            {s.has_price && <span className="text-xs text-[var(--warn)]">{t('已有价格')}</span>}
          </>} />
      </div>
    )}

    {rows.length > 0 && (
      <Input placeholder={t('搜索模型名...')} value={search} onChange={e => { setSearch(e.target.value); setPage(0) }} className="max-w-xs" />
    )}
    {(() => {
      const filtered = rows.filter(r => !search || r.model.toLowerCase().includes(search.toLowerCase()))
      const paged = filtered.slice(page * pageSize, (page + 1) * pageSize)
      return <>
        <Table head={<><Th>{t('模型')}</Th><Th>{t('类型')}</Th><Th>{t('输入')}</Th><Th>{t('输出')}</Th><Th>{t('缓存输入')}</Th><Th>{t('缓存写')}</Th><Th>{t('币种')}</Th><Th>{t('状态')}</Th><Th>{t('操作')}</Th></>}>
          {paged.map(r => <tr key={r.id} className="hover:bg-[var(--muted)]">
            <Td className="tnum">{r.model}</Td>
            <Td>{r.type === 'times' ? t('按次') : 'Token'}</Td>
            <Td className="tnum">{r.type === 'times' ? '—' : r.input_per_1m}</Td>
            <Td className="tnum">{r.type === 'times' ? '—' : r.output_per_1m}</Td>
            <Td className="tnum">{r.type === 'times' ? '—' : r.cached_input_per_1m}</Td>
            <Td className="tnum">{r.type === 'times' ? '—' : (r.cache_write_per_1m || 0)}</Td>
            <Td>{r.currency}</Td>
            <Td><StatusPill value={r.enabled} /></Td>
            <Td className="flex gap-1">
              <Button variant="ghost" disabled={toggleM.isPending} onClick={() => toggleM.mutate(r)}>{r.enabled ? t('停用') : t('启用')}</Button>
              <Button variant="ghost" onClick={() => openEdit(r)}><Pencil size={15} /></Button>
              <Button variant="ghost" onClick={() => del(r.id)}><Trash2 size={15} className="text-[var(--muted-foreground)] transition-colors" /></Button>
            </Td>
          </tr>)}
          {paged.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{rows.length === 0 ? t('暂无价格') : t('无匹配')}</Td></tr>}
        </Table>
        <PaginationBar total={filtered.length} page={page} pageSize={pageSize} onPageChange={setPage} hideOnSinglePage
          onPageSizeChange={n => { setPageSize(n); setPage(0) }} showJumper showEdges />
      </>
    })()}
    <FormDialog open={fd.open} onClose={() => { fd.close(); setLookupResults([]) }} title={fd.editId ? t('编辑模型价格') : t('配置模型价格')}
      onSubmit={() => saveM.mutate()}
      submitDisabled={!fd.form.model} submitting={saveM.isPending}>
      <Field label={t('模型名')}>
        <div className="flex gap-2">
          <Input value={fd.form.model} disabled={fd.editId !== 0} onChange={e => { fd.setForm({ ...fd.form, model: e.target.value }); setLookupResults([]) }}
            onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); lookupPrice() } }}
            placeholder="gpt-4o" className="flex-1" />
          {fd.editId === 0 && <Button type="button" variant="outline" onClick={lookupPrice} disabled={!fd.form.model || lookupM.isPending}>
            {lookupM.isPending ? t('查询中...') : t('查询价格')}
          </Button>}
        </div>
      </Field>
      {lookupResults.length > 0 && (
        <div className="max-h-40 overflow-y-auto border border-[var(--border)] rounded-md">
          {lookupResults.map((m, i) => (
            <button key={i} type="button" className="flex w-full items-center justify-between px-3 py-1.5 text-sm hover:bg-[var(--muted)] text-left"
              onClick={() => {
                fd.setForm({ ...fd.form, input_per_1m: m.input, output_per_1m: m.output, cached_input_per_1m: m.cache_read, cache_write_per_1m: m.cache_write })
                setLookupResults([])
              }}>
              <span>{m.model} <span className="text-xs text-[var(--muted-foreground)]">({m.provider})</span></span>
              <span className="tnum text-xs">{m.input}/{m.output}</span>
            </button>
          ))}
        </div>
      )}
      <Field label={t('计费类型')}>
        <SimpleSelect className="w-full" value={fd.form.type}
          onChange={v => fd.setForm({ ...fd.form, type: v })}
          options={[
            { value: 'tokens', label: t('按 Token') },
            { value: 'times', label: t('按次') },
          ]} />
      </Field>
      {fd.form.type === 'times' ? (
        <Field label={t('每次额度（USD）')}><Input type="number" value={fd.form.per_call_usd} onChange={e => fd.setForm({ ...fd.form, per_call_usd: Number(e.target.value) })} /></Field>
      ) : (
        <div className="grid grid-cols-3 gap-3">
          <Field label={t('输入价')}><Input type="number" value={fd.form.input_per_1m} onChange={e => fd.setForm({ ...fd.form, input_per_1m: Number(e.target.value) })} /></Field>
          <Field label={t('输出价')}><Input type="number" value={fd.form.output_per_1m} onChange={e => fd.setForm({ ...fd.form, output_per_1m: Number(e.target.value) })} /></Field>
          <Field label={t('缓存输入')}><Input type="number" value={fd.form.cached_input_per_1m} onChange={e => fd.setForm({ ...fd.form, cached_input_per_1m: Number(e.target.value) })} /></Field>
          <Field label={t('缓存写')}><Input type="number" value={fd.form.cache_write_per_1m} onChange={e => fd.setForm({ ...fd.form, cache_write_per_1m: Number(e.target.value) })} /></Field>
        </div>
      )}
    </FormDialog>
  </div>
}
