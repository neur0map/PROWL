import { Link } from 'react-router-dom'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { compactNumber, META_CHIP } from '@/components/keys/provider-catalogue'
import { useI18n } from '@/i18n'
import type { ProviderDirEntry } from '@/lib/catalog'
import { AccessChip, KeyStateChip, ReliabilityBar } from './shared'
import { keyStateOf, type SortCol, type SortState } from './model'

const EM_DASH = '—'

function SortHeader({
  col,
  label,
  align = 'left',
  sort,
  onSort,
}: {
  col: SortCol
  label: string
  align?: 'left' | 'right'
  sort: SortState
  onSort: (col: SortCol) => void
}) {
  const active = sort.col === col
  return (
    <th
      aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}
      className={`py-2 pr-3 font-medium ${align === 'right' ? 'text-right' : ''}`}
    >
      <button
        type="button"
        onClick={() => onSort(col)}
        className={`inline-flex items-center gap-1 rounded transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
          align === 'right' ? 'flex-row-reverse' : ''
        } ${active ? 'text-foreground' : ''}`}
      >
        {label}
        {active && (sort.dir === 'asc'
          ? <ChevronUp className="size-3" aria-hidden="true" />
          : <ChevronDown className="size-3" aria-hidden="true" />)}
      </button>
    </th>
  )
}

export function ProviderTable({
  rows,
  sort,
  onSort,
  drawerHref,
  modelsHref,
}: {
  rows: ProviderDirEntry[]
  sort: SortState
  onSort: (col: SortCol) => void
  drawerHref: (platform: string) => string
  modelsHref: (platform: string) => string
}) {
  const { t } = useI18n()
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[880px] text-sm">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            <SortHeader col="provider" label={t('catalog.col_provider')} sort={sort} onSort={onSort} />
            <SortHeader col="access" label={t('providerDir.access')} sort={sort} onSort={onSort} />
            <SortHeader col="key" label={t('providerDir.keyState')} sort={sort} onSort={onSort} />
            <SortHeader col="models" label={t('providerDir.col_models')} align="right" sort={sort} onSort={onSort} />
            <SortHeader col="routable" label={t('providerDir.col_routable')} align="right" sort={sort} onSort={onSort} />
            <SortHeader col="latency" label={t('providerDir.col_latency')} align="right" sort={sort} onSort={onSort} />
            <SortHeader col="reliability" label={t('providerDir.col_reliability')} sort={sort} onSort={onSort} />
            <SortHeader col="quota" label={t('providerDir.col_quota')} align="right" sort={sort} onSort={onSort} />
            <SortHeader col="requests" label={t('providerDir.col_requests')} align="right" sort={sort} onSort={onSort} />
            <th className="py-2 pr-3"><span className="sr-only">{t('providerDir.viewModels')}</span></th>
          </tr>
        </thead>
        <tbody>
          {rows.map(p => (
            <tr
              key={p.platform}
              className="group/row border-b bg-card transition-colors last:border-0 hover:[&>td]:bg-muted/50 [&>td:first-child]:rounded-l-lg [&>td:last-child]:rounded-r-lg"
            >
              <td className="py-2 pl-3 pr-3 align-middle">
                <div className="flex min-w-0 items-center gap-2">
                  <Link
                    to={drawerHref(p.platform)}
                    className="min-w-0 truncate rounded font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {p.label}
                  </Link>
                  <span className="hidden truncate font-mono text-[11px] text-muted-foreground/70 sm:inline">{p.platform}</span>
                  <span className={`${META_CHIP} hidden bg-muted text-muted-foreground/70 xl:inline`} title={t('providerDir.source')}>
                    {p.source}
                  </span>
                </div>
              </td>
              <td className="py-2 pr-3 align-middle"><AccessChip access={p.access} /></td>
              <td className="py-2 pr-3 align-middle"><KeyStateChip state={keyStateOf(p)} /></td>
              <td className="py-2 pr-3 align-middle text-right tabular-nums">{compactNumber(p.models)}</td>
              <td className="py-2 pr-3 align-middle text-right tabular-nums">{compactNumber(p.routableModels)}</td>
              <td className="py-2 pr-3 align-middle text-right tabular-nums text-muted-foreground">
                {p.latencyMs === null ? EM_DASH : `${p.latencyMs} ms`}
              </td>
              <td className="py-2 pr-3 align-middle"><ReliabilityBar value={p.reliability} /></td>
              <td className="py-2 pr-3 align-middle text-right tabular-nums text-muted-foreground">
                {p.quotaRemaining === null
                  ? <span className="text-[11px] text-muted-foreground/60">{t('providerDir.quotaUnknown')}</span>
                  : compactNumber(p.quotaRemaining)}
              </td>
              <td className="py-2 pr-3 align-middle text-right tabular-nums text-muted-foreground">
                {p.requests === null ? EM_DASH : compactNumber(p.requests)}
              </td>
              <td className="py-2 pr-3 align-middle text-right">
                <Link
                  to={modelsHref(p.platform)}
                  className="inline-flex items-center rounded text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {t('providerDir.viewModels')}
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
