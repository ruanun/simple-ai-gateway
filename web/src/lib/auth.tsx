import { createContext, useContext, useState, type ReactNode } from 'react'
import { api, setToken, clearToken, getToken } from './api'

const ROLE_KEY = 'saig_role'
export type Role = 'super' | 'ops' | 'viewer'

export function getRole(): Role {
  const r = localStorage.getItem(ROLE_KEY)
  // 旧会话无角色记录时按 super 展示——真正的强制在后端 RBAC，前端只是体验层
  return r === 'ops' || r === 'viewer' ? r : 'super'
}

type AuthCtx = { authed: boolean; role: Role; login: (u: string, p: string) => Promise<void>; logout: () => void }
const Ctx = createContext<AuthCtx>(null as unknown as AuthCtx)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [authed, setAuthed] = useState(!!getToken())
  const [role, setRole] = useState<Role>(getRole())
  async function login(username: string, password: string) {
    const r = await api.post<{ token: string; role?: Role }>('/admin/login', { username, password })
    setToken(r.token)
    localStorage.setItem(ROLE_KEY, r.role ?? 'super')
    setRole(r.role ?? 'super')
    setAuthed(true)
  }
  function logout() { clearToken(); localStorage.removeItem(ROLE_KEY); setAuthed(false) }
  return <Ctx.Provider value={{ authed, role, login, logout }}>{children}</Ctx.Provider>
}

export const useAuth = () => useContext(Ctx)
