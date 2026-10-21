const TOKEN_KEY = 'saig_token'
const PORTAL_TOKEN_KEY = 'saig_portal_token'

export function getToken() { return localStorage.getItem(TOKEN_KEY) || '' }
export function setToken(t: string) { localStorage.setItem(TOKEN_KEY, t) }
export function clearToken() { localStorage.removeItem(TOKEN_KEY) }

export function getPortalToken() { return localStorage.getItem(PORTAL_TOKEN_KEY) || '' }
export function setPortalToken(t: string) { localStorage.setItem(PORTAL_TOKEN_KEY, t) }
export function clearPortalToken() { localStorage.removeItem(PORTAL_TOKEN_KEY) }

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

// 管理端与门户两套 token/登录页相互独立，401 各自跳转各自的登录页。
type AuthConf = { getTok: () => string; clearTok: () => void; loginPath: string }
const adminConf: AuthConf = { getTok: getToken, clearTok: clearToken, loginPath: '/login' }
const portalConf: AuthConf = { getTok: getPortalToken, clearTok: clearPortalToken, loginPath: '/portal/login' }

async function request<T>(conf: AuthConf, method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const tok = conf.getTok()
  if (tok) headers['Authorization'] = `Bearer ${tok}`
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined })
  } catch {
    // fetch 仅在网络层失败时抛（断网/后端未启动/DNS），统一包装成中文可展示错误
    throw new ApiError(0, 'network_error', '网络错误，请检查连接')
  }
  if (res.status === 401 && !path.endsWith('/login')) {
    conf.clearTok()
    if (location.pathname !== conf.loginPath) location.href = conf.loginPath
    throw new ApiError(401, 'unauthorized', 'unauthorized')
  }
  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      // 非 JSON 响应体（如反代 502 HTML 页）：不让 SyntaxError 裸奔丢状态码
      throw new ApiError(res.status, 'http_error', `HTTP ${res.status}`)
    }
  }
  if (!res.ok) {
    // 后端统一形状 {"error":{"code","message"}}；宽容处理 error 为字符串的异常形态
    const e = (data as { error?: { code?: string; message?: string } | string } | null)?.error
    if (e && typeof e === 'object') throw new ApiError(res.status, e.code || 'unknown', e.message || `HTTP ${res.status}`)
    if (typeof e === 'string') throw new ApiError(res.status, 'unknown', e)
    throw new ApiError(res.status, 'unknown', `HTTP ${res.status}`)
  }
  return data as T
}

export const api = {
  get: <T>(p: string) => request<T>(adminConf, 'GET', p),
  post: <T>(p: string, b?: unknown) => request<T>(adminConf, 'POST', p, b),
  del: <T>(p: string) => request<T>(adminConf, 'DELETE', p),
}

export const portalApi = {
  get: <T>(p: string) => request<T>(portalConf, 'GET', p),
  post: <T>(p: string, b?: unknown) => request<T>(portalConf, 'POST', p, b),
  del: <T>(p: string) => request<T>(portalConf, 'DELETE', p),
}
