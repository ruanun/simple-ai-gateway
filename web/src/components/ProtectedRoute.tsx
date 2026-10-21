import { Navigate, Outlet } from 'react-router-dom'
import { useAuth } from '../lib/auth'

export function ProtectedRoute() {
  const { authed } = useAuth()
  return authed ? <Outlet /> : <Navigate to="/login" replace />
}
