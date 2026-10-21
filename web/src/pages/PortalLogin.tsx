import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { ApiError, portalApi, setPortalToken } from '../lib/api'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Field } from '../components/ui/field'
import { Turnstile } from '../components/captcha/Turnstile'
import { useI18n, LocaleToggle } from '../lib/i18n'

// PortalLogin 使用方门户登录/注册页。注册是否开放由后端设置决定（关闭时提交返回 403 提示）。
export function PortalLogin() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [u, setU] = useState('')
  const [p, setP] = useState('')
  const [p2, setP2] = useState('')
  const [err, setErr] = useState('')
  const [capAnswer, setCapAnswer] = useState('') // 图形验证码答案或 Turnstile token
  const [capNonce, setCapNonce] = useState(0)    // 自增即换新验证码（图形重新拉取 / Turnstile 重挂载）

  // OIDC 回调后跳回 /portal/login?oidc=1：门户 JWT 在服务端写下的短时 HttpOnly cookie 里，
  // 同源 POST /oidc/exchange 取走（URL 不带任何凭证，防登录 CSRF）。ref 防 StrictMode 双调用。
  const [searchParams, setSearchParams] = useSearchParams()
  const oidcHandled = useRef(false)
  useEffect(() => {
    if (searchParams.get('oidc') !== '1' || oidcHandled.current) return
    oidcHandled.current = true
    setSearchParams({}, { replace: true })
    portalApi.post<{ token: string }>('/portal/api/oidc/exchange')
      .then(r => { setPortalToken(r.token); navigate('/portal') })
      .catch(e => setErr(e instanceof ApiError && e.status === 401 ? t('第三方登录已失效，请重新登录') : (e instanceof Error ? e.message : t('第三方登录失败'))))
  }, [searchParams, setSearchParams, navigate, t])

  // 登录页配置（注册开关/OIDC 按钮/注册人机验证）：拉取失败静默，按默认样式展示
  const { data: conf } = useQuery({
    queryKey: ['/portal/api/config'],
    queryFn: () => portalApi.get<{
      register_enabled: boolean; oidc_enabled: boolean; oidc_button_text: string
      captcha_type: '' | 'image' | 'turnstile'; turnstile_site_key: string
    }>('/portal/api/config'),
    meta: { silentError: true },
  })
  const capType = mode === 'register' ? conf?.captcha_type ?? '' : ''

  // 图形验证码：capNonce 变化即重新拉取；不随窗口聚焦刷新，免得用户看图时图被换掉
  const { data: capImg } = useQuery({
    queryKey: ['/portal/api/captcha', capNonce],
    queryFn: () => portalApi.get<{ captcha_id: string; image: string }>('/portal/api/captcha'),
    enabled: capType === 'image',
    staleTime: Infinity, gcTime: 0, refetchOnWindowFocus: false,
    meta: { silentError: true },
  })

  function refreshCaptcha() {
    setCapAnswer('')
    setCapNonce(n => n + 1)
  }

  const submitM = useMutation({
    mutationFn: () => {
      const path = mode === 'login' ? '/portal/api/login' : '/portal/api/register'
      const body = mode === 'login'
        ? { username: u, password: p }
        : { username: u, password: p, captcha_id: capImg?.captcha_id ?? '', captcha: capAnswer }
      return portalApi.post<{ token: string }>(path, body)
    },
    meta: { silentError: true }, // 登录/注册失败在表单内就地提示，不走全局 toast
    onSuccess: (r) => { setPortalToken(r.token); navigate('/portal') },
    onError: (e) => {
      setErr(e instanceof Error ? e.message : t('操作失败'))
      if (capType) refreshCaptcha() // 验证码一次性，提交后后端已作废，失败须换新
    },
  })

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!u.trim() || !p.trim()) { setErr(t('请输入账号和密码')); return }
    if (mode === 'register' && p !== p2) { setErr(t('两次输入的密码不一致')); return }
    if (capType === 'image' && !capAnswer.trim()) { setErr(t('请输入验证码')); return }
    if (capType === 'turnstile' && !capAnswer) { setErr(t('请先完成人机验证')); return }
    setErr('')
    submitM.mutate()
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-[var(--background)] p-6">
      {/* 点阵网格 + 光晕：与落地页/登录页同一套品牌装饰语言 */}
      <div
        className="absolute inset-0 opacity-[0.06] dark:opacity-[0.04]"
        style={{ backgroundImage: 'radial-gradient(circle, var(--primary) 1px, transparent 1px)', backgroundSize: '32px 32px' }}
      />
      <div className="absolute -top-32 -right-32 h-96 w-96 rounded-full bg-[var(--primary)] opacity-[0.08] dark:opacity-20 blur-[110px]" />
      <div className="absolute -bottom-40 -left-40 h-[28rem] w-[28rem] rounded-full bg-[var(--accent)] opacity-[0.06] dark:opacity-10 blur-[130px]" />
      <div className="absolute top-4 right-4 z-20"><LocaleToggle /></div>
      <div className="relative z-10 w-full max-w-sm rounded-xl border border-[var(--border)] bg-[var(--card)] p-8 shadow-sm">
        <div className="flex items-center justify-center gap-3 mb-8">
          <img src="/logo-solid-light.svg" alt="logo" className="h-10 w-10 dark:hidden" />
          <img src="/logo-solid-dark.svg" alt="logo" className="h-10 w-10 hidden dark:block" />
          <span className="font-mono text-lg font-semibold">Simple AI Gateway</span>
        </div>
        <div className="mb-6 text-center">
          <h1 className="text-2xl font-bold">{mode === 'login' ? t('使用方门户') : t('注册账号')}</h1>
          <p className="mt-2 text-sm text-[var(--muted-foreground)]">{t('查看你的应用配额、令牌与用量')}</p>
        </div>
        <form className="space-y-4" onSubmit={submit}>
          <Field label={t('账号')}>
            <Input value={u} onChange={e => setU(e.target.value)} placeholder={t('请输入账号')} autoComplete="username" autoFocus />
          </Field>
          <Field label={t('密码')}>
            <Input type="password" value={p} onChange={e => setP(e.target.value)} placeholder={mode === 'register' ? t('8~64 字符') : t('请输入密码')}
              autoComplete={mode === 'login' ? 'current-password' : 'new-password'} />
          </Field>
          {mode === 'register' && (
            <Field label={t('确认密码')}>
              <Input type="password" value={p2} onChange={e => setP2(e.target.value)} placeholder={t('再次输入密码')} autoComplete="new-password" />
            </Field>
          )}
          {capType === 'image' && (
            <Field label={t('验证码')}>
              <div className="flex items-center gap-2">
                <Input value={capAnswer} onChange={e => setCapAnswer(e.target.value)} placeholder={t('请输入图中数字')}
                  inputMode="numeric" autoComplete="off" className="flex-1" />
                <button type="button" onClick={refreshCaptcha} title={t('看不清？换一张')}
                  className="flex h-10 w-[87px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-[var(--border)] bg-white cursor-pointer">
                  {capImg
                    ? <img src={capImg.image} alt="验证码" className="h-full w-full object-cover" />
                    : <span className="text-xs text-[var(--muted-foreground)]">{t('加载中…')}</span>}
                </button>
              </div>
            </Field>
          )}
          {capType === 'turnstile' && conf?.turnstile_site_key && (
            <Turnstile key={capNonce} siteKey={conf.turnstile_site_key} onToken={setCapAnswer} />
          )}
          {err && (
            <div className="rounded-md bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 px-3 py-2 text-sm text-[var(--destructive)]">
              {err}
            </div>
          )}
          <Button type="submit" className="w-full justify-center h-9 mt-2" disabled={submitM.isPending}>
            {submitM.isPending ? t('提交中…') : mode === 'login' ? t('登录') : t('注册')}
          </Button>
        </form>
        {conf?.oidc_enabled && (
          <Button variant="outline" className="w-full justify-center h-9 mt-3"
            onClick={() => { location.href = '/portal/api/oidc/login' }}>
            {conf.oidc_button_text || t('使用 OIDC 登录')}
          </Button>
        )}
        <div className="mt-4 flex items-center justify-between text-sm">
          {conf?.register_enabled !== false ? (
            <button className="text-[var(--primary)] hover:underline cursor-pointer"
              onClick={() => { setMode(mode === 'login' ? 'register' : 'login'); setErr(''); setCapAnswer('') }}>
              {mode === 'login' ? t('没有账号？注册') : t('已有账号？登录')}
            </button>
          ) : <span className="text-[var(--muted-foreground)]">{t('注册未开放')}</span>}
          <Link className="text-[var(--muted-foreground)] hover:underline" to="/login">{t('管理后台入口')}</Link>
        </div>
      </div>
    </div>
  )
}
