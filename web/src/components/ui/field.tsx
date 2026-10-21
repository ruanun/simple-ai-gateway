import type { ReactNode } from 'react'
export function Field({ label, error, children }: { label: string; error?: string; children: ReactNode }) {
  return <label className="block space-y-1">
    <span className="text-xs font-medium text-[var(--muted-foreground)]">{label}</span>
    {children}
    {error && <span className="text-xs text-[var(--destructive)]">{error}</span>}
  </label>
}
