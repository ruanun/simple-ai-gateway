import { Pill } from './ui/badge'
import { useI18n } from '../lib/i18n'

// 启用/停用状态徽章：收敛全站「status === 1 ? 启用 : 停用/禁用」的复制粘贴与文案漂移（统一为「启用/停用」）。
// value 兼容数字状态位（1 = 启用）与布尔开关字段（enabled）。
export function StatusPill({ value }: { value: number | boolean }) {
  const { t } = useI18n()
  return value === 1 || value === true
    ? <Pill status="ok">{t('启用')}</Pill>
    : <Pill status="muted">{t('停用')}</Pill>
}
