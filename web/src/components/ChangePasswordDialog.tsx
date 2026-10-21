import { useState } from 'react'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Field } from './ui/field'
import { toast } from './ui/sonner'
import { errMsg } from '../lib/format'
import { useI18n } from '../lib/i18n'

type Props = {
  open: boolean
  onClose: () => void
  // 关联账号的用户名（渲染为隐藏 username 字段，供浏览器密码管理器识别）
  username: string
  // 提交回调：管理端/门户端接口差异由调用方闭包封装；抛错由组件统一 toast
  onSubmit: (oldPwd: string, newPwd: string) => Promise<unknown>
  title?: string
  // self=自改密（当前密码 + 新密码 + 确认一致性校验）；reset=管理员重置他人密码（仅新密码）
  mode?: 'self' | 'reset'
}

const emptyForm = { old_password: '', new_password: '', confirm: '' }

// 改密/重置密码弹窗（PortalLayout/Users 自改密、Users/PortalUsers 重置密码四处共用）
export function ChangePasswordDialog({ open, onClose, username, onSubmit, title, mode = 'self' }: Props) {
  const { t } = useI18n()
  const [form, setForm] = useState(emptyForm)
  const [submitting, setSubmitting] = useState(false)
  const isSelf = mode === 'self'

  // 重置模式取消/关闭即清空输入；自改密仅成功后清空（与抽取前行为一致）
  function close() { if (!isSelf) setForm(emptyForm); onClose() }

  async function submit() {
    if (isSelf && form.new_password !== form.confirm) { toast.error(t('两次输入的新密码不一致')); return }
    setSubmitting(true)
    try {
      await onSubmit(form.old_password, form.new_password)
      onClose(); setForm(emptyForm); toast.success(isSelf ? t('密码已修改') : t('密码已重置'))
    } catch (e) { toast.error(errMsg(e)) } finally { setSubmitting(false) }
  }

  return <Dialog open={open} onClose={close} title={title ?? (isSelf ? t('修改密码') : t('重置密码'))}>
    <form className="space-y-3" onSubmit={e => { e.preventDefault(); submit() }}>
      <input type="text" name="username" autoComplete="username" value={username} readOnly hidden />
      {isSelf && <Field label={t('当前密码')}><Input type="password" name="current-password" autoComplete="current-password" value={form.old_password} onChange={e => setForm({ ...form, old_password: e.target.value })} /></Field>}
      <Field label={t('新密码(8~64 字符)')}><Input type="password" name={isSelf ? 'new-password' : 'reset-password'} autoComplete="new-password" value={form.new_password} onChange={e => setForm({ ...form, new_password: e.target.value })} /></Field>
      {isSelf && <Field label={t('确认新密码')}><Input type="password" name="confirm-password" autoComplete="new-password" value={form.confirm} onChange={e => setForm({ ...form, confirm: e.target.value })} /></Field>}
      <div className="flex justify-end gap-2 pt-1">
        <Button type="button" variant="outline" onClick={close}>{t('取消')}</Button>
        <Button type="submit" disabled={(isSelf ? (!form.old_password || form.new_password.length < 8 || !form.confirm) : form.new_password.length < 8) || submitting}>
          {isSelf ? t('修改') : t('重置')}
        </Button>
      </div>
    </form>
  </Dialog>
}
