import { StrictMode, Suspense, lazy } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import './index.css'
import { AuthProvider } from './lib/auth'
import { I18nProvider } from './lib/i18n'
import { PortalLayout } from './components/PortalLayout'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from './lib/query'
import { Toaster } from './components/ui/sonner'
import { ConfirmHost } from './components/ui/confirm'
import { TooltipProvider } from './components/ui/tooltip'
import { ProtectedRoute } from './components/ProtectedRoute'
import { AdminLayout } from './components/AdminLayout'

// 路由级懒加载：各页面拆分为独立 chunk，按需加载，避免主包过大
const Landing = lazy(() => import('./pages/Landing').then((m) => ({ default: m.Landing })))
const Login = lazy(() => import('./pages/Login').then((m) => ({ default: m.Login })))
const Dashboard = lazy(() => import('./pages/Dashboard').then((m) => ({ default: m.Dashboard })))
const Channels = lazy(() => import('./pages/Channels').then((m) => ({ default: m.Channels })))
const ChannelHealth = lazy(() => import('./pages/ChannelHealth').then((m) => ({ default: m.ChannelHealth })))
const Proxies = lazy(() => import('./pages/Proxies').then((m) => ({ default: m.Proxies })))
const Aliases = lazy(() => import('./pages/Aliases').then((m) => ({ default: m.Aliases })))
const Apps = lazy(() => import('./pages/Apps').then((m) => ({ default: m.Apps })))
const ApiKeys = lazy(() => import('./pages/ApiKeys').then((m) => ({ default: m.ApiKeys })))
const Prices = lazy(() => import('./pages/Prices').then((m) => ({ default: m.Prices })))
const Logs = lazy(() => import('./pages/Logs').then((m) => ({ default: m.Logs })))
const Docs = lazy(() => import('./pages/Docs').then((m) => ({ default: m.Docs })))
const Settings = lazy(() => import('./pages/Settings').then((m) => ({ default: m.Settings })))
const Playground = lazy(() => import('./pages/Playground').then((m) => ({ default: m.Playground })))
const Users = lazy(() => import('./pages/Users').then((m) => ({ default: m.Users })))
const OpLogs = lazy(() => import('./pages/OpLogs').then((m) => ({ default: m.OpLogs })))
const PortalUsers = lazy(() => import('./pages/PortalUsers').then((m) => ({ default: m.PortalUsers })))
const PortalLogin = lazy(() => import('./pages/PortalLogin').then((m) => ({ default: m.PortalLogin })))
const PortalHome = lazy(() => import('./pages/PortalHome').then((m) => ({ default: m.PortalHome })))
const PortalKeys = lazy(() => import('./pages/PortalKeys').then((m) => ({ default: m.PortalKeys })))
const PortalLogs = lazy(() => import('./pages/PortalLogs').then((m) => ({ default: m.PortalLogs })))
const UsageAnalysisApp = lazy(() => import('./pages/UsageAnalysis').then((m) => ({ default: m.AppUsageAnalysisPage })))
const UsageAnalysisKey = lazy(() => import('./pages/UsageAnalysis').then((m) => ({ default: m.KeyUsageAnalysisPage })))
const UsageAnalysisPortal = lazy(() => import('./pages/UsageAnalysis').then((m) => ({ default: m.PortalAppUsageAnalysisPage })))
const UsageAnalysisChannel = lazy(() => import('./pages/UsageAnalysis').then((m) => ({ default: m.ChannelUsageAnalysisPage })))

// 懒加载页面切换时的过渡占位
function PageFallback() {
  return <div className="flex h-full min-h-[60vh] items-center justify-center text-slate-400">加载中…</div>
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
    <QueryClientProvider client={queryClient}>
    <TooltipProvider delayDuration={300}>
    <Toaster />
    <ConfirmHost />
    <AuthProvider>
      <BrowserRouter>
        <Suspense fallback={<PageFallback />}>
          <Routes>
            {/* 入口分流落地页：管理后台 / 使用方门户，两枚 token 已登录时直跳 */}
            <Route path="/" element={<Landing />} />
            <Route path="/login" element={<Login />} />
            {/* 使用方门户（P9-B）：独立 token 与布局，与管理端隔离 */}
            <Route path="/portal/login" element={<PortalLogin />} />
            <Route path="/portal" element={<PortalLayout />}>
              <Route index element={<PortalHome />} />
              <Route path="keys" element={<PortalKeys />} />
              <Route path="docs" element={<Docs portal />} />
              <Route path="logs" element={<PortalLogs />} />
              <Route path="apps/:id/usage" element={<UsageAnalysisPortal />} />
            </Route>
            <Route element={<ProtectedRoute />}>
              <Route element={<AdminLayout />}>
                <Route path="/dashboard" element={<Dashboard />} />
                <Route path="/channels" element={<Channels />} />
                <Route path="/channels/health" element={<ChannelHealth />} />
                <Route path="/proxies" element={<Proxies />} />
                <Route path="/aliases" element={<Aliases />} />
                <Route path="/apps" element={<Apps />} />
                <Route path="/api-keys" element={<ApiKeys />} />
                <Route path="/apps/:id/usage" element={<UsageAnalysisApp />} />
                <Route path="/api-keys/:id/usage" element={<UsageAnalysisKey />} />
                <Route path="/channels/:id/usage" element={<UsageAnalysisChannel />} />
                <Route path="/prices" element={<Prices />} />
                <Route path="/logs" element={<Logs />} />
                <Route path="/docs" element={<Docs />} />
                <Route path="/users" element={<Users />} />
                <Route path="/op-logs" element={<OpLogs />} />
                <Route path="/portal-users" element={<PortalUsers />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="/playground" element={<Playground />} />
              </Route>
            </Route>
            {/* 未匹配路径回入口，不替访客猜身份 */}
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </BrowserRouter>
    </AuthProvider>
    </TooltipProvider>
    </QueryClientProvider>
    </I18nProvider>
  </StrictMode>,
)
