import type { ReactNode } from 'react'
export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
    <table className="w-full text-sm">
      <thead className="bg-[var(--muted)] text-left text-[var(--muted-foreground)]"><tr>{head}</tr></thead>
      <tbody>{children}</tbody>
    </table>
  </div>
}
export const Th = ({ children, className = '' }: { children?: ReactNode; className?: string }) => <th className={`px-3 py-2 font-medium ${className}`}>{children}</th>
export const Td = ({ children, className = '' }: { children?: ReactNode; className?: string }) => <td className={`border-t border-[var(--border)] px-3 py-2 ${className}`}>{children}</td>
