import { Link } from 'react-router-dom'
import { ShieldCheck, KeyRound, ArrowRight } from 'lucide-react'
import { getToken, getPortalToken } from '../lib/api'
import { useI18n, LocaleToggle } from '../lib/i18n'

/**
 * 入口分流落地页（根路径 /）
 * 职责单一：把两类访客送到对的门 —— 管理员 → 管理后台，使用方 → 门户。
 * 始终展示本页（不因已登录而自动跳过）；已持有对应 token 时入口卡片直达
 * 控制台，未登录则指向登录页。配色全部走 CSS 变量，跟随站点亮/暗主题。
 */
export function Landing() {
  const { t } = useI18n()
  // 不再自动跳转：保留引导页作为固定入口，仅按登录态调整卡片目标
  const adminHref = getToken() ? '/dashboard' : '/login'
  const portalHref = getPortalToken() ? '/portal' : '/portal/login'

  return (
    <div className="relative flex min-h-screen flex-col overflow-hidden bg-[var(--background)] text-[var(--foreground)]">
      {/* 点阵网格 + 光晕：与登录页品牌面板同语言 */}
      <div
        className="absolute inset-0 opacity-[0.06] dark:opacity-[0.04]"
        style={{ backgroundImage: 'radial-gradient(circle, var(--primary) 1px, transparent 1px)', backgroundSize: '32px 32px' }}
      />
      <div className="absolute -top-32 -right-32 h-96 w-96 rounded-full bg-[var(--primary)] opacity-[0.08] dark:opacity-20 blur-[110px]" />
      <div className="absolute -bottom-40 -left-40 h-[28rem] w-[28rem] rounded-full bg-[var(--accent)] opacity-[0.06] dark:opacity-10 blur-[130px]" />

      {/* 顶栏 */}
      <header className="relative z-10 flex items-center gap-3 p-8">
        <img src="/logo-solid-light.svg" alt="logo" className="h-9 w-9 dark:hidden" />
        <img src="/logo-solid-dark.svg" alt="logo" className="hidden h-9 w-9 dark:block" />
        <span className="font-mono text-base font-semibold">Simple AI Gateway</span>
        <div className="ml-auto"><LocaleToggle /></div>
      </header>

      {/* 主体 */}
      <main className="relative z-10 flex flex-1 flex-col items-center justify-center px-6 pb-16">
        <div className="w-full max-w-2xl">
          <h1 className="text-center text-3xl font-bold leading-tight sm:text-4xl">
            自托管 <span className="text-[var(--primary)]">{t('AI 网关')}</span>
          </h1>
          <p className="mt-3 text-center text-sm text-[var(--muted-foreground)]">
            多协议透传 · 智能路由 · 密钥轮换 · 计费审计
          </p>

          {/* 签名元素：路由分流图 —— 一条入站请求经网关节点分叉到两个入口 */}
          <svg
            viewBox="0 0 640 120"
            aria-hidden="true"
            className="mt-2 hidden w-full sm:block"
          >
            {/* 入站主干 → 网关节点 */}
            <path d="M 40 24 H 320" className="landing-route landing-route-base" />
            <path d="M 40 24 H 320" className="landing-route landing-flow" stroke="var(--accent)" />
            {/* 分叉：节点 → 左右两卡片中心（25% / 75%） */}
            <path d="M 320 24 C 320 64, 160 56, 160 104" className="landing-route landing-route-base" />
            <path d="M 320 24 C 320 64, 480 56, 480 104" className="landing-route landing-route-base" />
            <path d="M 320 24 C 320 64, 160 56, 160 104" className="landing-route landing-flow" stroke="var(--primary)" />
            <path d="M 320 24 C 320 64, 480 56, 480 104" className="landing-route landing-flow landing-flow-late" stroke="var(--primary)" />
            {/* 网关节点（琥珀）与两个落点 */}
            <circle cx="320" cy="24" r="5" fill="var(--accent)" />
            <circle cx="320" cy="24" r="9" fill="none" stroke="var(--accent)" strokeOpacity="0.35" />
            <circle cx="160" cy="104" r="3.5" fill="var(--primary)" />
            <circle cx="480" cy="104" r="3.5" fill="var(--primary)" />
          </svg>

          {/* 两个入口 */}
          <div className="mt-6 grid gap-4 sm:mt-0 sm:grid-cols-2">
            <Link
              to={adminHref}
              className="group rounded-xl border border-[var(--border)] bg-[var(--card)] p-6 no-underline shadow-sm transition-colors hover:border-[var(--primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
            >
              <div className="flex items-center gap-2.5">
                <ShieldCheck className="h-5 w-5 text-[var(--primary)]" />
                <span className="text-base font-semibold">{t('管理后台')}</span>
              </div>
              <p className="mt-2 text-sm leading-relaxed text-[var(--muted-foreground)]">
                配置渠道、路由与价格，管理密钥，查看日志与审计。
              </p>
              <span className="mt-4 inline-flex items-center gap-1 text-sm text-[var(--primary)]">
                进入管理后台
                <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" />
              </span>
            </Link>
            <Link
              to={portalHref}
              className="group rounded-xl border border-[var(--border)] bg-[var(--card)] p-6 no-underline shadow-sm transition-colors hover:border-[var(--accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
            >
              <div className="flex items-center gap-2.5">
                <KeyRound className="h-5 w-5 text-[var(--accent)]" />
                <span className="text-base font-semibold">{t('使用方门户')}</span>
              </div>
              <p className="mt-2 text-sm leading-relaxed text-[var(--muted-foreground)]">
                查看自己的 API Key、配额用量与调用日志。
              </p>
              <span className="mt-4 inline-flex items-center gap-1 text-sm text-[var(--accent)]">
                进入门户
                <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" />
              </span>
            </Link>
          </div>
        </div>
      </main>

      {/* 底部：端点方言 */}
      <footer className="relative z-10 p-8 text-center font-mono text-xs text-[var(--muted-foreground)] opacity-70">
        POST /v1/chat/completions · /v1/messages · /v1/responses
      </footer>

      {/* 流光动画：仅装饰，reduced-motion 下静止 */}
      <style>{`
        .landing-route { fill: none; stroke-width: 1.5; }
        .landing-route-base { stroke: var(--border); }
        .landing-flow {
          stroke-dasharray: 10 130;
          stroke-dashoffset: 140;
          stroke-linecap: round;
          animation: landing-flow 3.2s linear infinite;
        }
        .landing-flow-late { animation-delay: 1.6s; }
        @keyframes landing-flow { to { stroke-dashoffset: 0; } }
        @media (prefers-reduced-motion: reduce) {
          .landing-flow { animation: none; stroke-dasharray: none; }
        }
      `}</style>
    </div>
  )
}
