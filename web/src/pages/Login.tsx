import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { OpenAI, Anthropic } from '@lobehub/icons'
import { useAuth } from '../lib/auth'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { useI18n, LocaleToggle } from '../lib/i18n'

export function Login() {
  const { login } = useAuth()
  const { t } = useI18n()
  const navigate = useNavigate()
  const [u, setU] = useState('')
  const [p, setP] = useState('')
  const [err, setErr] = useState('')
  const [touched, setTouched] = useState({ u: false, p: false })
  const uErr = touched.u && !u.trim() ? t('请输入账号') : ''
  const pErr = touched.p && !p.trim() ? t('请输入密码') : ''

  const loginM = useMutation({
    mutationFn: () => login(u, p),
    meta: { silentError: true }, // 登录失败在表单内就地提示，不走全局 toast
    onSuccess: () => navigate('/dashboard'),
    onError: (e) => setErr(e instanceof Error ? e.message : t('登录失败')),
  })

  function submit(e: React.FormEvent) {
    e.preventDefault()
    setTouched({ u: true, p: true })
    if (!u.trim() || !p.trim()) return
    setErr('')
    loginM.mutate()
  }
  return (
    <div className="flex min-h-screen">
      {/* 左侧品牌区 */}
      <div className="hidden lg:flex lg:w-[480px] xl:w-[560px] flex-col justify-between bg-[#0B1220] p-10 relative overflow-hidden">
        {/* 装饰性网格 */}
        <div className="absolute inset-0 opacity-[0.04]"
          style={{
            backgroundImage: 'radial-gradient(circle, var(--primary) 1px, transparent 1px)',
            backgroundSize: '32px 32px',
          }}
        />
        {/* 装饰性光晕 */}
        <div className="absolute -top-24 -right-24 w-80 h-80 rounded-full bg-[#1E40AF] opacity-20 blur-[100px]" />
        <div className="absolute -bottom-32 -left-32 w-96 h-96 rounded-full bg-[#D97706] opacity-10 blur-[120px]" />

        <div className="relative z-10">
          <div className="flex items-center gap-3">
            <img src="/logo-solid-dark.svg" alt="logo" className="h-10 w-10" />
            <span className="text-lg font-semibold text-white font-mono">Simple AI Gateway</span>
          </div>
        </div>

        <div className="relative z-10 space-y-6">
          <h2 className="text-3xl font-bold text-white leading-tight">
            统一管理你的<br />
            <span className="text-[#60A5FA]">AI 模型</span>调用
          </h2>
          <p className="text-[#94A3B8] text-sm leading-relaxed max-w-sm">
            多协议透传 · 智能路由 · 密钥轮换 · 计费审计
          </p>
          <div className="flex gap-5 text-xs text-[#94A3B8]">
            <div className="flex items-center gap-1.5">
              <OpenAI.Avatar size={20} />
              OpenAI 兼容
            </div>
            <div className="flex items-center gap-1.5">
              <Anthropic.Avatar size={20} />
              Anthropic 原生
            </div>
          </div>
        </div>

        <div className="relative z-10 text-xs text-[#475569]">
          © {new Date().getFullYear()} Simple AI Gateway
        </div>
      </div>

      {/* 右侧表单区 */}
      <div className="flex-1 flex items-center justify-center p-6 bg-[var(--background)]">
        <div className="w-full max-w-sm">
          <div className="flex justify-end mb-2"><LocaleToggle /></div>
          {/* 移动端 logo */}
          <div className="lg:hidden flex items-center justify-center gap-3 mb-8">
            <img src="/logo-solid-light.svg" alt="logo" className="h-10 w-10" />
            <span className="text-lg font-semibold font-mono">Simple AI Gateway</span>
          </div>

          <div className="mb-8">
            <h1 className="text-2xl font-bold">{t('登录')}</h1>
            <p className="mt-2 text-sm text-[var(--muted-foreground)]">{t('登录管理后台以继续')}</p>
          </div>

          <form className="space-y-4" onSubmit={submit}>
            <Field label={t('账号')} error={uErr}>
              <Input
                value={u}
                onChange={e => setU(e.target.value)}
                onBlur={() => setTouched(t => ({ ...t, u: true }))}
                placeholder={t('请输入账号')}
                autoComplete="username"
                autoFocus
                className={uErr ? 'border-[var(--destructive)]' : ''}
              />
            </Field>
            <Field label={t('密码')} error={pErr}>
              <Input
                type="password"
                value={p}
                onChange={e => setP(e.target.value)}
                onBlur={() => setTouched(t => ({ ...t, p: true }))}
                placeholder={t('请输入密码')}
                autoComplete="current-password"
                className={pErr ? 'border-[var(--destructive)]' : ''}
              />
            </Field>
            {err && (
              <div className="rounded-md bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 px-3 py-2 text-sm text-[var(--destructive)]">
                {err}
              </div>
            )}
            <Button type="submit" className="w-full justify-center h-9 mt-2" disabled={loginM.isPending}>
              {loginM.isPending ? t('登录中…') : t('登录')}
            </Button>
          </form>
          <p className="mt-4 text-center text-sm">
            <a className="text-[var(--muted-foreground)] hover:underline" href="/portal/login">{t('使用方门户入口')}</a>
          </p>
        </div>
      </div>
    </div>
  )
}
