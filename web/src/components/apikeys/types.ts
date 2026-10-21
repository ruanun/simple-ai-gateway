import type { AdminApiKey } from '../../lib/types'

// 密钥标签（/admin/key-tags）
export type Tag = { id: number; name: string }
// 令牌 + 已绑标签（/admin/api-keys 列表项形状）
export type KeyItem = { key: AdminApiKey; tags: Tag[] }
// 应用下拉选项（仅令牌表单需要的最小形状）
export type AppOption = { id: number; name: string }
// 令牌新增/编辑表单
export type ApiKeyForm = {
  name: string
  app_id: number
  rpm: number
  daily_cost_usd: number
  whitelist: string[]
  mappings: { from: string; to: string }[]
}
