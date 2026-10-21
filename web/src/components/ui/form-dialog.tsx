import { useState } from 'react'
import type { Dispatch, ReactNode, SetStateAction } from 'react'
import { Dialog } from './dialog'
import { Button } from './button'
import { useI18n } from '../../lib/i18n'

// useFormDialog 新增/编辑弹窗三件套 state（open/editId/form）+ 开关函数。
// editId=0 表示新增（沿用全站哨兵约定）。此前 7 个管理页各写一份同构脚手架。
export function useFormDialog<T>(emptyForm: T) {
  const [open, setOpen] = useState(false)
  const [editId, setEditId] = useState(0)
  const [form, setForm] = useState<T>(emptyForm)
  const openNew = () => { setEditId(0); setForm(emptyForm); setOpen(true) }
  const openEdit = (id: number, f: T) => { setEditId(id); setForm(f); setOpen(true) }
  const close = () => { setOpen(false); setEditId(0); setForm(emptyForm) }
  return { open, editId, form, setForm: setForm as Dispatch<SetStateAction<T>>, openNew, openEdit, close }
}

// FormDialog 表单弹窗壳：Dialog + form 提交语义 + 统一「取消/保存」footer。
// 字段体由各页自渲染（Switch/Checkbox 列表/动态行等差异保留在页内）。
export function FormDialog({ open, onClose, title, onSubmit, submitDisabled, submitting, submitText, width, children }: {
  open: boolean; onClose: () => void; title: string
  onSubmit: () => void; submitDisabled?: boolean; submitting?: boolean
  submitText?: string; width?: string; children: ReactNode
}) {
  const { t } = useI18n()
  return (
    <Dialog open={open} onClose={onClose} title={title} width={width}>
      <form className="space-y-3" onSubmit={e => { e.preventDefault(); onSubmit() }}>
        {children}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t('取消')}</Button>
          <Button type="submit" disabled={submitDisabled || submitting}>{submitText ?? t('保存')}</Button>
        </div>
      </form>
    </Dialog>
  )
}
