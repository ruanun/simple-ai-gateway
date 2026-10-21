import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { LayoutDashboard, Server, Network, GitBranch, AppWindow, KeyRound, DollarSign, ScrollText, BookOpen, Settings2, LogOut, FlaskConical, UsersRound, ClipboardList, Contact, HeartPulse } from 'lucide-react'
import { useAuth, type Role } from '../lib/auth'
import { useI18n, LocaleToggle } from '../lib/i18n'
import { ThemeToggle } from './ThemeToggle'

// 导航按职能分区,带静态分区标题。roles 缺省=全角色可见;后端 RBAC 才是强制,此处仅体验层。
const navGroups: { title: string; items: { to: string; label: string; icon: typeof LayoutDashboard; roles?: Role[] }[] }[] = [
  { title: '总览', items: [
    { to: '/dashboard', label: '仪表盘', icon: LayoutDashboard },
    { to: '/docs', label: '接入文档', icon: BookOpen },
    { to: '/playground', label: '调试台', icon: FlaskConical },
  ] },
  { title: '模型接入', items: [
    { to: '/channels', label: '渠道管理', icon: Server },
    { to: '/proxies', label: 'IP代理池', icon: Network },
    { to: '/aliases', label: '别名路由', icon: GitBranch },
  ] },
  { title: '计费治理', items: [
    { to: '/apps', label: '应用管理', icon: AppWindow },
    { to: '/api-keys', label: '令牌管理', icon: KeyRound },
    { to: '/prices', label: '价格管理', icon: DollarSign },
    { to: '/portal-users', label: '使用方用户', icon: Contact },
  ] },
  { title: '审计观测', items: [
    { to: '/logs', label: '请求日志', icon: ScrollText },
    { to: '/channels/health', label: '渠道健康', icon: HeartPulse },
    { to: '/op-logs', label: '操作日志', icon: ClipboardList, roles: ['super', 'ops'] },
  ] },
  { title: '系统设置', items: [
    { to: '/users', label: '用户管理', icon: UsersRound, roles: ['super'] },
    { to: '/settings', label: '系统设置', icon: Settings2, roles: ['super'] },
  ] },
]

export function AdminLayout() {
  const { logout, role } = useAuth()
  const { t } = useI18n()
  const navigate = useNavigate()
  const groups = navGroups
    .map(g => ({ ...g, items: g.items.filter(it => !it.roles || it.roles.includes(role)) }))
    .filter(g => g.items.length > 0)
  return <div className="flex h-screen">
    <aside className="flex w-60 flex-col border-r border-[var(--border)] bg-[var(--card)]">
      <div className="flex h-14 items-center gap-2 border-b border-[var(--border)] px-4">
        <img src="/logo.svg" alt="logo" className="h-7 w-7" />
        <span className="font-mono text-sm font-bold text-[var(--primary)]">Simple AI Gateway</span>
      </div>
      <nav className="flex-1 space-y-4 overflow-y-auto p-2">
        {groups.map(group => (
          <div key={group.title} className="space-y-1">
            <div className="px-3 pb-0.5 text-xs font-semibold text-[var(--muted-foreground)]">{t(group.title)}</div>
            {group.items.map(({ to, label, icon: Icon }) => (
              <NavLink key={to} to={to} className={({ isActive }) =>
                `flex items-center gap-2 rounded-md px-3 py-2 text-sm transition ${isActive ? 'bg-[var(--primary)] text-[var(--primary-foreground)]' : 'hover:bg-[var(--muted)]'}`}>
                <Icon size={16} />{t(label)}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
    </aside>
    <div className="flex flex-1 flex-col overflow-hidden">
      <header className="flex h-14 items-center justify-end gap-2 border-b border-[var(--border)] px-4">
        <LocaleToggle />
        <ThemeToggle />
        <button className="flex items-center gap-1 rounded-md px-3 py-1.5 text-sm text-[var(--destructive)] hover:bg-[var(--muted)] cursor-pointer"
          onClick={() => { logout(); navigate('/login') }}><LogOut size={16} />{t('登出')}</button>
      </header>
      <main className="flex-1 overflow-auto p-6"><Outlet /></main>
    </div>
  </div>
}
