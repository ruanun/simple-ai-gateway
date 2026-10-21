import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { NavLink, Navigate, Outlet, useNavigate } from 'react-router-dom'
import { LayoutDashboard, KeyRound, ScrollText, BookOpen, Lock, LogOut } from 'lucide-react'
import { portalApi, getPortalToken, clearPortalToken } from '../lib/api'
import { ThemeToggle } from './ThemeToggle'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { useI18n, LocaleToggle } from '../lib/i18n'

const navItems = [
  { to: '/portal', label: '概览', icon: LayoutDashboard, end: true },
  { to: '/portal/keys', label: '我的令牌', icon: KeyRound },
  { to: '/portal/docs', label: '接入文档', icon: BookOpen },
  { to: '/portal/logs', label: '请求日志', icon: ScrollText },
]

// PortalLayout 使用方门户布局：顶栏 + 三项导航，与管理端完全隔离（独立 token）。
export function PortalLayout() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [pwdOpen, setPwdOpen] = useState(false)

  // 顶栏用户名仅作展示：失败静默（401 由 portalApi 统一清 token 跳登录，无需 toast）
  const { data: me } = useQuery({
    queryKey: ['/portal/api/me'],
    queryFn: () => portalApi.get<{ username: string }>('/portal/api/me'),
    enabled: !!getPortalToken(),
    meta: { silentError: true },
  })
  const username = me?.username ?? ''

  // 门户自改密：ChangePasswordDialog 自行 toast 成功/失败，全局兜底会重复提示，故静默；
  // mutateAsync 保持组件的 Promise 契约（组件内部 onClose/清表单/toast 收尾不变）。
  const pwdM = useMutation({
    mutationFn: (p: { old: string; next: string }) => portalApi.post('/portal/api/password', { old_password: p.old, new_password: p.next }),
    meta: { silentError: true },
  })

  if (!getPortalToken()) return <Navigate to="/portal/login" replace />

  function logout() { clearPortalToken(); navigate('/portal/login') }

  return <div className="flex h-screen flex-col">
    <header className="flex h-14 items-center gap-6 border-b border-[var(--border)] bg-[var(--card)] px-6">
      <div className="flex items-center gap-2">
        <img src="/logo.svg" alt="logo" className="h-7 w-7" />
        <span className="font-mono text-sm font-bold text-[var(--primary)]">Simple AI Gateway</span>
        <span className="rounded bg-[var(--muted)] px-1.5 py-0.5 text-xs text-[var(--muted-foreground)]">{t('使用方门户')}</span>
      </div>
      <nav className="flex flex-1 items-center gap-1">
        {navItems.map(({ to, label, icon: Icon, end }) => (
          <NavLink key={to} to={to} end={end} className={({ isActive }) =>
            `flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition ${isActive ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'hover:bg-[var(--muted)]'}`}>
            <Icon size={15} />{t(label)}
          </NavLink>
        ))}
      </nav>
      <div className="flex items-center gap-2">
        <span className="text-sm text-[var(--muted-foreground)]">{username}</span>
        <LocaleToggle />
        <ThemeToggle />
        <button className="flex items-center gap-1 rounded-md px-2 py-1.5 text-sm hover:bg-[var(--muted)] cursor-pointer"
          onClick={() => setPwdOpen(true)}><Lock size={15} />{t('改密')}</button>
        <button className="flex items-center gap-1 rounded-md px-2 py-1.5 text-sm text-[var(--destructive)] hover:bg-[var(--muted)] cursor-pointer"
          onClick={logout}><LogOut size={15} />{t('登出')}</button>
      </div>
    </header>
    <main className="flex-1 overflow-auto p-6"><Outlet /></main>

    <ChangePasswordDialog open={pwdOpen} onClose={() => setPwdOpen(false)} username={username}
      onSubmit={(oldPwd, newPwd) => pwdM.mutateAsync({ old: oldPwd, next: newPwd })} />
  </div>
}
