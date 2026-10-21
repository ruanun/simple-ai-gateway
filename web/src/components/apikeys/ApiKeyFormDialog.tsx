import type { Dispatch, SetStateAction } from 'react'
import { FormDialog } from '../ui/form-dialog'
import { Input } from '../ui/input'
import { Field } from '../ui/field'
import { Button } from '../ui/button'
import { SimpleSelect } from '../ui/select'
import { Checkbox } from '../ui/checkbox'
import { Plus, Trash2 } from 'lucide-react'
import type { ApiKeyForm, AppOption } from './types'
import { useI18n } from '../../lib/i18n'

type Props = {
  open: boolean
  onClose: () => void
  editId: number
  form: ApiKeyForm
  setForm: Dispatch<SetStateAction<ApiKeyForm>>
  onSubmit: () => void
  submitting: boolean
  apps: AppOption[]
  availableModels: string[]
  whitelistSearch: string
  setWhitelistSearch: Dispatch<SetStateAction<string>>
}

// 统一新增/编辑弹窗：仅新增时显示 name/app（创建后不可更改）；
// 白名单 checkbox+搜索区、模型映射动态键值行均纯渲染+回调，state 仍在主组件
export function ApiKeyFormDialog({
  open, onClose, editId, form, setForm, onSubmit, submitting,
  apps, availableModels, whitelistSearch, setWhitelistSearch,
}: Props) {
  const { t } = useI18n()
  // 白名单过滤后的模型列表
  const filteredWL = availableModels.filter(
    m => !whitelistSearch || m.toLowerCase().includes(whitelistSearch.toLowerCase())
  )

  return (
    <FormDialog open={open} onClose={onClose} title={editId ? t('编辑令牌') : t('新增令牌')} width="max-w-xl"
      onSubmit={onSubmit} submitting={submitting} submitText={editId ? t('保存') : t('创建')}
      submitDisabled={editId === 0 && (!form.name || !form.app_id)}>
      {/* 仅新增时显示 name 和 app（创建后不可更改） */}
      {editId === 0 && <>
        <Field label={t('名称')}>
          <Input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder={t('例如：客服机器人')} />
        </Field>
        <Field label={t('所属应用')}>
          <SimpleSelect className="w-full" value={String(form.app_id)}
            onChange={v => setForm({ ...form, app_id: Number(v) })}
            options={[
              { value: '0', label: t('请选择应用') },
              ...apps.map(a => ({ value: String(a.id), label: a.name })),
            ]} />
          {apps.length === 0 && <span className="mt-1 block text-xs text-[var(--warn)]">{t('请先到「应用管理」创建应用')}</span>}
        </Field>
      </>}

      <Field label={t('每分钟限流 RPM（0=按全局默认上限，-1=不限）')}>
        <Input type="number" min={-1} value={form.rpm} onChange={e => setForm({ ...form, rpm: Number(e.target.value) })} />
      </Field>

      <Field label={t('日成本上限（USD，0=不限）')}>
        <Input type="number" value={form.daily_cost_usd} onChange={e => setForm({ ...form, daily_cost_usd: Number(e.target.value) })} />
      </Field>

      {/* 模型白名单：checkbox 选择 */}
      <Field label={t('模型白名单（空=不限）')}>
        <Input
          placeholder="搜索模型..."
          value={whitelistSearch}
          onChange={e => setWhitelistSearch(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') e.preventDefault() }}
          className="mb-2"
        />
        <div className="max-h-40 overflow-y-auto space-y-1 border border-[var(--border)] rounded-md p-2">
          {filteredWL.map(m => (
            <label key={m} className="flex items-center gap-2 text-sm cursor-pointer hover:bg-[var(--muted)] px-1 rounded">
              <Checkbox
                checked={form.whitelist.includes(m)}
                onCheckedChange={() => {
                  const wl = form.whitelist.includes(m)
                    ? form.whitelist.filter(x => x !== m)
                    : [...form.whitelist, m]
                  setForm({ ...form, whitelist: wl })
                }}
              />
              {m}
            </label>
          ))}
          {filteredWL.length === 0 && (
            <span className="text-xs text-[var(--muted-foreground)]">
              {availableModels.length === 0 ? t('暂无可用模型（请先配置渠道模型或别名）') : t('无匹配')}
            </span>
          )}
        </div>
        {form.whitelist.length > 0 && (
          <div className="mt-1 text-xs text-[var(--muted-foreground)]">已选 {form.whitelist.length} 个</div>
        )}
      </Field>

      {/* 模型映射：键值对动态行 */}
      <Field label={t('模型映射（请求模型 → 实际模型）')}>
        {form.mappings.map((mp, i) => (
          <div key={i} className="flex items-center gap-2 mb-1">
            <Input
              placeholder={t('请求模型')}
              value={mp.from}
              onChange={e => {
                const ms = [...form.mappings]
                ms[i] = { ...ms[i], from: e.target.value }
                setForm({ ...form, mappings: ms })
              }}
              className="flex-1"
            />
            <span className="text-sm text-[var(--muted-foreground)]">→</span>
            <Input
              placeholder={t('实际模型')}
              value={mp.to}
              onChange={e => {
                const ms = [...form.mappings]
                ms[i] = { ...ms[i], to: e.target.value }
                setForm({ ...form, mappings: ms })
              }}
              className="flex-1"
            />
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                const ms = form.mappings.filter((_, j) => j !== i)
                setForm({ ...form, mappings: ms })
              }}
            >
              <Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" />
            </Button>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          onClick={() => setForm({ ...form, mappings: [...form.mappings, { from: '', to: '' }] })}
        >
          <Plus size={14} />添加映射
        </Button>
      </Field>
    </FormDialog>
  )
}
