import { useId, useState, type ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { useI18n } from '@/i18n'
import type {
  CatalogAvailabilityFacet,
  CatalogCaps,
  CatalogFacets,
  Tier,
} from '@/lib/catalog'

// The six filter groups the browse rail exposes. Each group's selection is a
// Set of option keys, toggled in and out as the user checks boxes.
export type FilterGroupKey = 'avail' | 'tier' | 'caps' | 'context' | 'providers' | 'authors'
export type CatalogFilters = Record<FilterGroupKey, Set<string>>

const AVAIL: { key: keyof CatalogAvailabilityFacet; tKey: string }[] = [
  { key: 'inGateway', tKey: 'avail_inGateway' },
  { key: 'routable', tKey: 'avail_routable' },
  { key: 'needsKey', tKey: 'avail_needsKey' },
]

const COST: { key: Tier; tKey: string }[] = [
  { key: 'free', tKey: 'cost_free' },
  { key: 'paid', tKey: 'cost_paid' },
  { key: 'subscription', tKey: 'cost_subscription' },
]

const CAPS: { key: keyof CatalogCaps; tKey: string }[] = [
  { key: 'tools', tKey: 'cap_tools' },
  { key: 'vision', tKey: 'cap_vision' },
  { key: 'reasoning', tKey: 'cap_reasoning' },
]

// Numeric thresholds, so their labels are plain numbers and not localized.
const CTX_BUCKETS: { key: string; label: string }[] = [
  { key: '32000', label: '32K+' },
  { key: '128000', label: '128K+' },
  { key: '1000000', label: '1M+' },
]

// How many of a long option list to show before the "show all" expander.
const OPTION_TOP = 8

const CHECKBOX = 'size-3.5 shrink-0 accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50'

function CheckRow({ label, count, checked, disabled, onToggle }: {
  label: string
  count: number
  checked: boolean
  disabled: boolean
  onToggle: () => void
}) {
  const id = useId()
  return (
    <div className={`flex items-center gap-2 rounded-md px-1.5 py-1 text-xs ${disabled ? 'opacity-40' : 'hover:bg-muted/60'}`}>
      <input id={id} type="checkbox" checked={checked} disabled={disabled} onChange={onToggle} className={CHECKBOX} />
      <label htmlFor={id} className={`min-w-0 flex-1 truncate ${disabled ? '' : 'cursor-pointer'}`}>{label}</label>
      <span className="shrink-0 tabular-nums text-muted-foreground/70">{count}</span>
    </div>
  )
}

function FilterSection({ title, sectionKey, open, onToggle, children }: {
  title: string
  sectionKey: string
  open: boolean
  onToggle: (key: string) => void
  children: ReactNode
}) {
  return (
    <div className="border-b py-2 last:border-0">
      <button
        type="button"
        onClick={() => onToggle(sectionKey)}
        aria-expanded={open}
        className="flex w-full items-center justify-between rounded-md px-1 py-1 text-xs font-medium transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <span>{title}</span>
        <ChevronDown className={`size-3.5 text-muted-foreground transition-transform ${open ? '' : '-rotate-90'}`} />
      </button>
      {open && <div className="mt-1 space-y-0.5">{children}</div>}
    </div>
  )
}

// The provider and author groups can run into the hundreds, so each gets its
// own filter box and a "show all" expander. Already-checked options that fall
// past the cutoff are pinned into view so unchecking one is never hidden.
function OptionList({ items, selected, searchPlaceholder, onToggle }: {
  items: { key: string; label: string; count: number }[]
  selected: Set<string>
  searchPlaceholder: string
  onToggle: (value: string) => void
}) {
  const { t } = useI18n()
  const [filter, setFilter] = useState('')
  const [expanded, setExpanded] = useState(false)
  const q = filter.trim().toLowerCase()
  const matched = q
    ? items.filter(i => i.label.toLowerCase().includes(q) || i.key.toLowerCase().includes(q))
    : items
  const top = matched.slice(0, OPTION_TOP)
  const topKeys = new Set(top.map(i => i.key))
  const pinned = q || expanded ? [] : matched.filter(i => selected.has(i.key) && !topKeys.has(i.key))
  const shown = q || expanded ? matched : [...top, ...pinned]
  const remaining = matched.length - shown.length
  return (
    <div className="space-y-1">
      <input
        value={filter}
        onChange={e => setFilter(e.target.value)}
        placeholder={searchPlaceholder}
        aria-label={searchPlaceholder}
        className="w-full rounded-md border bg-background px-2 py-1 text-xs outline-none transition-colors focus:border-foreground/30"
      />
      <div className="space-y-0.5">
        {shown.map(i => (
          <CheckRow
            key={i.key}
            label={i.label}
            count={i.count}
            checked={selected.has(i.key)}
            disabled={i.count === 0}
            onToggle={() => onToggle(i.key)}
          />
        ))}
      </div>
      {!q && (remaining > 0 || expanded) && (
        <button
          type="button"
          onClick={() => setExpanded(v => !v)}
          className="rounded px-1.5 text-xs text-muted-foreground underline decoration-dotted underline-offset-2 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {expanded ? t('catalog.showLess') : t('catalog.showAll', { count: matched.length })}
        </button>
      )}
    </div>
  )
}

export function FilterRail({ facets, contextCounts, selected, collapsed, hasActive, onToggle, onToggleSection, onClear }: {
  facets: CatalogFacets
  contextCounts: Record<number, number>
  selected: CatalogFilters
  collapsed: Set<string>
  hasActive: boolean
  onToggle: (group: FilterGroupKey, value: string) => void
  onToggleSection: (key: string) => void
  onClear: () => void
}) {
  const { t } = useI18n()
  const providerItems = facets.providers.map(p => ({ key: p.platform, label: p.label, count: p.models }))
  const authorItems = facets.authors.map(a => ({ key: a.author, label: a.author, count: a.models }))
  return (
    <div className="text-sm">
      <div className="mb-1 flex items-center justify-between px-1">
        <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{t('catalog.filters')}</span>
        {hasActive && (
          <button
            type="button"
            onClick={onClear}
            className="rounded text-xs text-muted-foreground underline decoration-dotted underline-offset-2 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            {t('catalog.clear')}
          </button>
        )}
      </div>

      <FilterSection title={t('catalog.availability')} sectionKey="availability" open={!collapsed.has('availability')} onToggle={onToggleSection}>
        {AVAIL.map(o => (
          <CheckRow key={o.key} label={t(`catalog.${o.tKey}`)} count={facets.availability[o.key]} checked={selected.avail.has(o.key)} disabled={facets.availability[o.key] === 0} onToggle={() => onToggle('avail', o.key)} />
        ))}
      </FilterSection>

      <FilterSection title={t('catalog.cost')} sectionKey="cost" open={!collapsed.has('cost')} onToggle={onToggleSection}>
        {COST.map(o => (
          <CheckRow key={o.key} label={t(`catalog.${o.tKey}`)} count={facets.tier[o.key]} checked={selected.tier.has(o.key)} disabled={facets.tier[o.key] === 0} onToggle={() => onToggle('tier', o.key)} />
        ))}
      </FilterSection>

      <FilterSection title={t('catalog.capability')} sectionKey="caps" open={!collapsed.has('caps')} onToggle={onToggleSection}>
        {CAPS.map(o => (
          <CheckRow key={o.key} label={t(`catalog.${o.tKey}`)} count={facets.caps[o.key]} checked={selected.caps.has(o.key)} disabled={facets.caps[o.key] === 0} onToggle={() => onToggle('caps', o.key)} />
        ))}
      </FilterSection>

      <FilterSection title={t('catalog.context')} sectionKey="context" open={!collapsed.has('context')} onToggle={onToggleSection}>
        {CTX_BUCKETS.map(b => (
          <CheckRow key={b.key} label={b.label} count={contextCounts[Number(b.key)] ?? 0} checked={selected.context.has(b.key)} disabled={(contextCounts[Number(b.key)] ?? 0) === 0} onToggle={() => onToggle('context', b.key)} />
        ))}
      </FilterSection>

      <FilterSection title={t('catalog.provider')} sectionKey="providers" open={!collapsed.has('providers')} onToggle={onToggleSection}>
        <OptionList items={providerItems} selected={selected.providers} searchPlaceholder={t('catalog.providerSearch')} onToggle={v => onToggle('providers', v)} />
      </FilterSection>

      <FilterSection title={t('catalog.author')} sectionKey="authors" open={!collapsed.has('authors')} onToggle={onToggleSection}>
        <OptionList items={authorItems} selected={selected.authors} searchPlaceholder={t('catalog.providerSearch')} onToggle={v => onToggle('authors', v)} />
      </FilterSection>
    </div>
  )
}
