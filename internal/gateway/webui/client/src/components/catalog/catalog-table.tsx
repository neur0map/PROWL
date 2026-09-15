import { useId } from 'react'
import { Link } from 'react-router-dom'
import { ChevronUp } from 'lucide-react'
import { useI18n } from '@/i18n'
import { AxisBar } from '@/components/model-table'
import { Switch } from '@/components/ui/switch'
import { formatContext } from '@/lib/routing'
import { modelDetailHref, type CatalogModel, type Modality } from '@/lib/catalog'
import { NeedsKey, formatPrice } from '@/components/catalog/model-row'

export const TABLE_COLS = ['providers', 'name', 'context', 'in', 'out', 'reliability', 'speed', 'score'] as const
export type TableSortCol = typeof TABLE_COLS[number]
export interface TableSort {
  col: TableSortCol
  dir: 'asc' | 'desc'
}

// Column label i18n keys, reusing the existing catalog/model/strategy strings
// so the table header never invents copy the rest of the app does not use.
const COL_LABEL: Record<TableSortCol, string> = {
  providers: 'catalog.detail_providers',
  name: 'common.model',
  context: 'catalog.col_context',
  in: 'catalog.col_in',
  out: 'catalog.col_out',
  reliability: 'catalog.col_reliability',
  speed: 'models.speedRank',
  score: 'strategies.scoreColumn',
}

function SortHeader({ col, sort, align, onSort }: {
  col: TableSortCol
  sort: TableSort
  align: 'left' | 'right'
  onSort: (col: TableSortCol) => void
}) {
  const { t } = useI18n()
  const active = sort.col === col
  return (
    <th
      aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
      className={`px-3 py-2 font-medium ${align === 'right' ? 'text-right' : 'text-left'}`}
    >
      <button
        type="button"
        onClick={() => onSort(col)}
        className={`inline-flex items-center gap-1 rounded transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${active ? 'text-foreground' : ''} ${align === 'right' ? 'flex-row-reverse' : ''}`}
      >
        <span>{t(COL_LABEL[col])}</span>
        {active && <ChevronUp className={`size-3 transition-transform ${sort.dir === 'desc' ? 'rotate-180' : ''}`} />}
      </button>
    </th>
  )
}

function TableRow({ model, modality, selected, onSelect, onUse }: {
  model: CatalogModel
  modality: Modality
  selected: boolean
  onSelect: (id: number, on: boolean) => void
  onUse: (model: CatalogModel, use: boolean) => void
}) {
  const { t } = useI18n()
  const m = model
  const selectId = useId()
  return (
    <tr className="border-b last:border-0 transition-colors hover:bg-muted/40">
      <td className="px-3 py-2">
        <input
          id={selectId}
          type="checkbox"
          checked={selected}
          onChange={e => onSelect(m.id, e.target.checked)}
          aria-label={t('catalog.selectModel', { name: m.name })}
          className="size-3.5 accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
      </td>
      <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{m.providers.length}</td>
      <td className="px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <Link
            to={modelDetailHref(modality, m.canonicalId)}
            className="min-w-0 truncate rounded font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            {m.name}
          </Link>
          <span className="shrink-0 text-[10px] text-muted-foreground">{m.author}</span>
        </div>
      </td>
      <td className="px-3 py-2 text-right tabular-nums">{m.contextWindow == null ? '—' : formatContext(m.contextWindow)}</td>
      <td className="px-3 py-2 text-right tabular-nums">{m.priceIn == null ? '—' : formatPrice(m.priceIn)}</td>
      <td className="px-3 py-2 text-right tabular-nums">{m.priceOut == null ? '—' : formatPrice(m.priceOut)}</td>
      <td className="px-3 py-2"><div className="flex justify-end"><AxisBar value={m.reliability == null ? undefined : m.reliability / 100} color="#22c55e" /></div></td>
      <td className="px-3 py-2"><div className="flex justify-end"><AxisBar value={m.speed == null ? undefined : m.speed / 100} color="#3b82f6" /></div></td>
      <td className="px-3 py-2 text-right font-mono text-xs font-medium tabular-nums">{m.score == null ? '—' : m.score.toFixed(3)}</td>
      <td className="px-3 py-2 text-right">
        {m.routable
          ? <Switch checked={m.inGateway} onCheckedChange={c => onUse(m, c)} aria-label={`${t('catalog.use')} ${m.name}`} />
          : <NeedsKey provider={m.providers[0]} />}
      </td>
    </tr>
  )
}

// The parent owns the ordering (header clicks call onSort and re-sort the whole
// set) and the progressive row budget, so this renders only what it is handed.
export function CatalogTable({ models, modality, selection, sort, onSelect, onUse, onSort }: {
  models: CatalogModel[]
  modality: Modality
  selection: Set<number>
  sort: TableSort
  onSelect: (id: number, on: boolean) => void
  onUse: (model: CatalogModel, use: boolean) => void
  onSort: (col: TableSortCol) => void
}) {
  const { t } = useI18n()
  return (
    <div className="overflow-x-auto rounded-2xl border">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b bg-muted/30 text-xs text-muted-foreground">
            <th className="w-8 px-3 py-2"><span className="sr-only">{t('catalog.col_use')}</span></th>
            <SortHeader col="providers" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="name" sort={sort} align="left" onSort={onSort} />
            <SortHeader col="context" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="in" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="out" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="reliability" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="speed" sort={sort} align="right" onSort={onSort} />
            <SortHeader col="score" sort={sort} align="right" onSort={onSort} />
            <th className="px-3 py-2 text-right font-medium">{t('catalog.col_use')}</th>
          </tr>
        </thead>
        <tbody>
          {models.map(m => (
            <TableRow key={m.id} model={m} modality={modality} selected={selection.has(m.id)} onSelect={onSelect} onUse={onUse} />
          ))}
        </tbody>
      </table>
    </div>
  )
}
