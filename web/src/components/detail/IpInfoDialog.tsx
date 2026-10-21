import { useQuery } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Dialog } from '../ui/dialog'

type IpInfo = {
  ip: string
  private: boolean
  country: string
  country_code: string
  region: string
  city: string
  lat: number
  lon: number
  isp: string
  org: string
  asn: string
  timezone: string
  proxy: boolean
  source: string
}

// IpInfoDialog 展示单个 IP 的归属信息；打开时按需拉取，私有 IP 显示本地标记。
export function IpInfoDialog({ ip, open, onClose }: { ip: string; open: boolean; onClose: () => void }) {
  // 按 IP 缓存查询，仅弹窗打开时启用；IP 归属基本不变，5 分钟内复开同 IP 不重取
  const { data: info, isLoading, isError } = useQuery({
    queryKey: ['/admin/ip-info', ip],
    queryFn: () => api.get<IpInfo>(`/admin/ip-info/${encodeURIComponent(ip)}`),
    enabled: open && !!ip,
    staleTime: 5 * 60 * 1000,
  })

  return (
    <Dialog open={open} onClose={onClose} title={`IP 归属 · ${ip}`} width="max-w-md">
      {isLoading && <div className="text-sm text-[var(--muted-foreground)]">查询中…</div>}
      {isError && <div className="text-sm text-[var(--destructive)]">查询失败</div>}
      {info && info.private && (
        <div className="rounded-md bg-[var(--muted)] px-3 py-2 text-sm text-[var(--muted-foreground)]">
          内网 / 私有地址，无公网归属信息。
        </div>
      )}
      {info && !info.private && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-[var(--muted-foreground)]">国家/地区</dt><dd>{[info.country, info.region, info.city].filter(Boolean).join(' / ') || '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">ISP</dt><dd>{info.isp || '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">组织</dt><dd>{info.org || '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">ASN</dt><dd className="tnum">{info.asn || '—'}</dd>
          <dt className="text-[var(--muted-foreground)]">时区</dt><dd>{info.timezone || '—'}</dd>
          {(info.lat !== 0 || info.lon !== 0) && <><dt className="text-[var(--muted-foreground)]">经纬度</dt><dd className="tnum">{info.lat}, {info.lon}</dd></>}
          {info.proxy && <><dt className="text-[var(--muted-foreground)]">代理</dt><dd className="text-[#D97706]">是</dd></>}
          <dt className="text-[var(--muted-foreground)]">数据源</dt><dd className="text-xs text-[var(--muted-foreground)]">{info.source}</dd>
        </dl>
      )}
    </Dialog>
  )
}
