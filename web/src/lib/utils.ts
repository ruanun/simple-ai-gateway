import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

// shadcn/ui 约定的类名合并工具：clsx 组合 + tailwind-merge 去冲突
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// Set 开关纯函数：key 存在则移除、不存在则加入，返回新 Set（React state 需换引用）。
// 此前 Prices/ChannelModelsDialog/Apps 等多处逐字复制该样板。
export function toggleInSet<T>(set: Set<T>, key: T): Set<T> {
  const next = new Set(set)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  return next
}
