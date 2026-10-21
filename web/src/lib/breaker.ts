import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { toast } from '../components/ui/sonner'

export type BreakerState = 'open' | 'half'
export type ChannelBreaker = { id: number; state: BreakerState; open_until: number; probe_level: number }
export type AccountBreaker = { id: number; channel_id: number; state: BreakerState; open_until: number; probe_level: number }
// 账号×模型配额冷却（429/403 触发的 keyed 熔断，与账号失败率熔断独立；重置账号时后端联动清除）
export type QuotaCool = { account_id: number; channel_id: number; model: string; open_until: number }
export type ProxyBreaker = { id: number; state: BreakerState; open_until: number; probe_level: number }
export type BreakerStatus = { channels: ChannelBreaker[]; accounts: AccountBreaker[]; account_quotas?: QuotaCool[]; proxies?: ProxyBreaker[] }

// 熔断状态查询：10s 轻量轮询。渠道页与密钥池弹窗共用同一 key，TanStack 自动去重为单次请求。
export function useBreakerStatus() {
  return useQuery({
    queryKey: ['/admin/breaker/status'],
    queryFn: () => api.get<BreakerStatus>('/admin/breaker/status'),
    refetchInterval: 10000,
  })
}

// 手动重置某渠道/账号/代理的熔断：渠道页/弹窗/代理页共用同一实现。成功 toast；失败（如 viewer 被
// 后端 RBAC 拦为 403、或网络错误）交给全局 MutationCache 兜底 toast（文案更准，能展示后端具体原因）。
export function useBreakerReset() {
  const qc = useQueryClient()
  const m = useMutation({
    mutationFn: (v: { level: 'channel' | 'account' | 'proxy'; id: number }) => api.post('/admin/breaker/reset', v),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['/admin/breaker/status'] })
      toast.success('已重置熔断')
    },
  })
  return (level: 'channel' | 'account' | 'proxy', id: number) => m.mutate({ level, id })
}
