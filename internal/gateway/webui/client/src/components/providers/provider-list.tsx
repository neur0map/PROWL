import { Link } from 'react-router-dom'
import { compactNumber } from '@/components/keys/provider-catalogue'
import { useI18n } from '@/i18n'
import type { ProviderDirEntry } from '@/lib/catalog'
import { AccessChip, KeyStateChip, ReliabilityBar } from './shared'
import { keyStateOf } from './model'

const EM_DASH = '—'

export function ProviderList({
  rows,
  drawerHref,
  modelsHref,
}: {
  rows: ProviderDirEntry[]
  drawerHref: (platform: string) => string
  modelsHref: (platform: string) => string
}) {
  const { t } = useI18n()
  return (
    <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
      {rows.map(p => (
        <div
          key={p.platform}
          className="relative rounded-xl border bg-card px-3 py-2.5 transition-colors hover:border-muted-foreground/30"
        >
          <div className="flex items-center gap-2">
            <Link
              to={drawerHref(p.platform)}
              className="min-w-0 flex-1 truncate rounded font-medium after:absolute after:inset-0 after:rounded-xl focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {p.label}
            </Link>
            <AccessChip access={p.access} />
          </div>
          <div className="mt-1 flex min-w-0 items-center gap-2">
            <span className="min-w-0 truncate font-mono text-[11px] text-muted-foreground/70">{p.platform}</span>
            <KeyStateChip state={keyStateOf(p)} />
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
            <span className="tabular-nums">
              <span className="text-foreground">{compactNumber(p.models)}</span> {t('providerDir.col_models')}
            </span>
            <span className="tabular-nums">
              <span className="text-foreground">{compactNumber(p.routableModels)}</span> {t('providerDir.col_routable')}
            </span>
            <span className="tabular-nums" title={t('providerDir.col_latency')}>
              {p.latencyMs === null ? EM_DASH : `${p.latencyMs} ms`}
            </span>
            <ReliabilityBar value={p.reliability} />
            <span className="tabular-nums" title={t('providerDir.col_quota')}>
              {p.quotaRemaining === null ? t('providerDir.quotaUnknown') : compactNumber(p.quotaRemaining)}
            </span>
          </div>
          <div className="mt-2">
            <Link
              to={modelsHref(p.platform)}
              className="relative z-[1] inline-flex items-center rounded text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {t('providerDir.viewModels')}
            </Link>
          </div>
        </div>
      ))}
    </div>
  )
}
