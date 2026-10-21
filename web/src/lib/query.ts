import { QueryClient, QueryCache, MutationCache } from '@tanstack/react-query'
import { ApiError } from './api'
import { toast } from '../components/ui/sonner'

// meta 类型注册：silentError=true 表示该 query/mutation 自行处理错误，全局兜底跳过
declare module '@tanstack/react-query' {
  interface Register {
    queryMeta: { silentError?: boolean }
    mutationMeta: { silentError?: boolean }
  }
}

// 全局错误提示唯一出口：
// - 401 不提示（api.ts 已清 token 跳登录）
// - meta.silentError 的调用方自行 onError，防双重提示（v5 里全局与局部 onError 会同时触发）
// 未走 Query 的裸 Promise 已清零，无需 unhandledrejection 二道防线（曾有 main.tsx 兜底，已删；
// 唯一的裸 fetch 是 Playground SSE 流，其调用方 run() 自带 try/catch，不会泄漏未捕获拒绝）
function reportError(e: unknown, meta?: { silentError?: boolean }) {
  if (meta?.silentError) return
  if (e instanceof ApiError && e.status === 401) return
  toast.error(e instanceof Error && e.message ? e.message : '请求失败')
}

// 全站 TanStack Query 客户端。约定：
// - queryKey = [接口路径, 参数对象?]，如 ['/admin/channels']、['/admin/logs', { page, channel }]
// - 页面级列表/统计走 useQuery；弹窗按需拉取走 useQuery({enabled})；写操作走 useMutation，
//   onSuccess 里 invalidateQueries + 关弹窗清表单，失败由全局兜底 toast（弹窗留在原地可重试）
// - 管理后台以手动刷新为主：不开窗口聚焦自动重取、失败不重试（错误立即可见）
export const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (e, query) => reportError(e, query.meta),
  }),
  mutationCache: new MutationCache({
    onError: (e, _vars, _ctx, mutation) => reportError(e, mutation.options.meta),
  }),
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
  },
})
