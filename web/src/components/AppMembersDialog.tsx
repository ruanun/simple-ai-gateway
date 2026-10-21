import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Table, Th, Td } from './ui/table'
import { Badge } from './ui/badge'
import { Combobox } from './ui/combobox'
import { toast } from './ui/sonner'
import { confirmDialog } from './ui/confirm'
import { Trash2 } from 'lucide-react'
import type { App, PortalUser, AppMemberInfo } from '../lib/types'
import { useI18n } from '../lib/i18n'

type Props = {
  app: App | null // 非空即视为打开
  onClose: () => void
  portalUsers: PortalUser[]
}

// 应用成员管理弹窗：列出成员（owner 徽章 + 移除按钮禁用）、Combobox 选门户用户添加、
// 逐行 confirmDialog 确认后移除；操作后失效对应查询触发刷新，成败均 toast 提示。
export function AppMembersDialog({ app, onClose, portalUsers }: Props) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const open = !!app
  const queryKey = ['/admin/apps', app?.id, 'members']
  const { data: members = [] } = useQuery({
    queryKey,
    queryFn: () => api.get<AppMemberInfo[]>(`/admin/apps/${app!.id}/members`),
    enabled: open,
  })
  const [addId, setAddId] = useState('0')

  function reload() { qc.invalidateQueries({ queryKey }) }

  // 原 catch 只是 toast 错误 → 删除交全局兜底；成功 toast 保留在 onSuccess
  const addMemberM = useMutation({
    mutationFn: (userId: number) => api.post(`/admin/apps/${app!.id}/members`, { user_id: userId }),
    onSuccess: () => { setAddId('0'); reload(); toast.success(t('已添加成员')) },
  })
  function add() {
    if (addId === '0') return
    addMemberM.mutate(Number(addId))
  }

  const removeMemberM = useMutation({
    mutationFn: (userId: number) => api.del(`/admin/apps/${app!.id}/members/${userId}`),
    onSuccess: () => { reload(); toast.success(t('已移除成员')) },
  })
  async function remove(m: AppMemberInfo) {
    if (!(await confirmDialog({
      title: `移除成员「${m.username}」？`,
      description: t('移除后该用户将无法在使用方门户访问此应用。'),
      destructive: true, confirmText: t('移除'),
    }))) return
    removeMemberM.mutate(m.user_id)
  }

  // 已是成员的用户不再出现在待添加下拉里
  const memberIds = new Set(members.map(m => m.user_id))
  const assignable = portalUsers.filter(u => !memberIds.has(u.id))

  return <Dialog open={open} onClose={onClose} title={`${app?.name ?? ''} · ${t('成员')}`}>
    <div className="space-y-3">
      <Table head={<><Th>{t('用户名')}</Th><Th>{t('角色')}</Th><Th>{t('操作')}</Th></>}>
        {members.map(m => <tr key={m.user_id}>
          <Td>{m.username}</Td>
          <Td>{m.is_owner ? <Badge status="ok">{t('所有者')}</Badge> : <span className="text-xs text-[var(--muted-foreground)]">{t('成员')}</span>}</Td>
          <Td>
            <Button variant="ghost" disabled={m.is_owner} onClick={() => remove(m)} title={m.is_owner ? t('所有者不可移除，请先变更所有者') : t('移除成员')}>
              <Trash2 size={14} className="text-[var(--muted-foreground)] transition-colors" />
            </Button>
          </Td>
        </tr>)}
        {members.length === 0 && <tr><Td className="text-[var(--muted-foreground)]">{t('暂无成员')}</Td></tr>}
      </Table>
      <div className="flex gap-2">
        <Combobox className="flex-1" value={addId} onChange={setAddId}
          placeholder={t('选择用户…')} searchPlaceholder="搜索用户名…"
          options={assignable.map(u => ({ value: String(u.id), label: u.username }))} />
        <Button disabled={addId === '0' || addMemberM.isPending} onClick={add}>{t('添加')}</Button>
      </div>
    </div>
  </Dialog>
}
