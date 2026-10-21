import { Tip } from '../ui/tooltip'
import { useI18n } from '../../lib/i18n'

type Props = {
  prompt: number
  completion: number
  cached: number
}

// Token 单元格：↑输入 / ↓输出 + 共计 + ⚡缓存读（请求日志列表用）
export function TokensCell({ prompt, completion, cached }: Props) {
  const { t } = useI18n()
  if (!prompt && !completion) return <span className="text-[var(--muted-foreground)]">—</span>
  return <div className="leading-snug">
    <div className="flex items-center gap-0.5 tnum">
      <Tip content={t('输入')}><span className="text-[var(--primary)]">↑{(prompt || 0).toLocaleString()}</span></Tip>
      <span className="text-[var(--muted-foreground)]">/</span>
      <Tip content={t('输出')}><span className="text-green-600">↓{(completion || 0).toLocaleString()}</span></Tip>
    </div>
    <div className="text-xs text-[var(--muted-foreground)] tnum">{t('共')} {((prompt || 0) + (completion || 0)).toLocaleString()}</div>
    {(cached || 0) > 0 && <Tip content={t('缓存读')}><div className="text-xs tnum text-emerald-600">⚡{t('缓存')} {(cached || 0).toLocaleString()}</div></Tip>}
  </div>
}
