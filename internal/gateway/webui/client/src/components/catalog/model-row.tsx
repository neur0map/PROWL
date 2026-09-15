import { useId } from 'react'
import { Link } from 'react-router-dom'
import { useI18n } from '@/i18n'
import { AxisBar } from '@/components/model-table'
import { Switch } from '@/components/ui/switch'
import { formatContext } from '@/lib/routing'
import {
  modelDetailHref,
  type CatalogCaps,
  type CatalogModel,
  type CatalogProvider,
  type Modality,
  type Tier,
} from '@/lib/catalog'

// USD per 1M tokens, trailing zeros trimmed: 0.10 → $0.1, 3.00 → $3, and small
// per-million prices kept honest (0.0004 → $0.0004) rather than rounded to $0.
export function formatPrice(perMillion: number): string {
  return '$' + perMillion.toFixed(4).replace(/\.?0+$/, '')
}

const CAP_CHIP: Record<keyof CatalogCaps, string> = {
  tools: 'bg-violet-600/15 text-violet-700 dark:bg-violet-400/15 dark:text-violet-400',
  vision: 'bg-cyan-600/15 text-cyan-700 dark:bg-cyan-400/15 dark:text-cyan-400',
  reasoning: 'bg-emerald-600/15 text-emerald-700 dark:bg-emerald-400/15 dark:text-emerald-400',
}

const TIER_CHIP: Record<Tier, string> = {
  free: 'bg-muted text-muted-foreground',
  paid: 'bg-amber-600/15 text-amber-700 dark:bg-amber-400/15 dark:text-amber-400',
  subscription: 'bg-indigo-600/15 text-indigo-700 dark:bg-indigo-400/15 dark:text-indigo-400',
}

const CHIP = 'text-[10px] rounded-full px-1.5 py-0.5 whitespace-nowrap'

function Chips({ tier, caps }: { tier: Tier; caps: CatalogCaps }) {
  const { t } = useI18n()
  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <span className={`${CHIP} ${TIER_CHIP[tier]}`}>{t(`catalog.cost_${tier}`)}</span>
      {(['tools', 'vision', 'reasoning'] as const).map(k => caps[k] && (
        <span key={k} className={`${CHIP} ${CAP_CHIP[k]}`}>{t(`catalog.cap_${k}`)}</span>
      ))}
    </div>
  )
}

function PriceMeta({ model }: { model: CatalogModel }) {
  const { t } = useI18n()
  if (model.priceIn == null && model.priceOut == null) {
    return model.tier === 'paid' ? null : <span>{t('catalog.free')}</span>
  }
  return (
    <>
      <span title={t('catalog.col_in')}>↓{model.priceIn == null ? '—' : formatPrice(model.priceIn)}{t('catalog.perM')}</span>
      <span title={t('catalog.col_out')}>↑{model.priceOut == null ? '—' : formatPrice(model.priceOut)}{t('catalog.perM')}</span>
    </>
  )
}

// Shown in place of the Use switch when no key can serve the model yet: a muted
// one-line "Needs a key" chip (the provider is named in its title) with the
// add-key link inline after it, so the cell never wraps into the row.
export function NeedsKey({ provider }: { provider: CatalogProvider | undefined }) {
  const { t } = useI18n()
  const platform = provider?.platform ?? ''
  return (
    <div className="flex min-w-0 items-center justify-end gap-1.5">
      <span
        title={t('catalog.needsKey', { provider: provider?.label ?? platform })}
        className="truncate rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
      >
        {t('catalog.avail_needsKey')}
      </span>
      <Link
        to={`/keys?provider=${encodeURIComponent(platform)}`}
        className="shrink-0 rounded text-[10px] font-medium text-foreground underline decoration-dotted underline-offset-2 hover:no-underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {t('catalog.addKeyCta')}
      </Link>
    </div>
  )
}

export function ModelRow({ model, modality, selected, onSelect, onUse }: {
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
    <div className="group/row flex items-center gap-3 border-b px-3 py-2.5 last:border-0 transition-colors hover:bg-muted/40">
      <input
        id={selectId}
        type="checkbox"
        checked={selected}
        onChange={e => onSelect(m.id, e.target.checked)}
        aria-label={t('catalog.selectModel', { name: m.name })}
        className="size-3.5 shrink-0 accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <span aria-hidden="true" className="flex size-7 shrink-0 items-center justify-center rounded-lg border bg-muted text-xs font-semibold text-muted-foreground">
        {m.author.trim().charAt(0).toUpperCase() || '?'}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <Link
            to={modelDetailHref(modality, m.canonicalId)}
            className="min-w-0 flex-1 truncate rounded text-sm font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <span className="text-muted-foreground">{m.author}:</span> {m.name}
          </Link>
          <Chips tier={m.tier} caps={m.caps} />
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[11px] tabular-nums text-muted-foreground">
          <span>{m.providers.length >= 2 ? t('catalog.providersServing', { count: m.providers.length }) : (m.providers[0]?.label ?? '')}</span>
          <span>{m.contextWindow == null ? '—' : formatContext(m.contextWindow)}</span>
          <PriceMeta model={m} />
        </div>
      </div>
      <div className="hidden shrink-0 items-center gap-3 lg:flex">
        <AxisBar value={m.reliability == null ? undefined : m.reliability / 100} color="#22c55e" />
        <AxisBar value={m.speed == null ? undefined : m.speed / 100} color="#3b82f6" />
        <AxisBar value={m.intelligence == null ? undefined : m.intelligence / 100} color="#a855f7" />
      </div>
      <span className="hidden w-12 shrink-0 text-right font-mono text-xs font-medium tabular-nums sm:inline" title={t('catalog.scoreHint')}>
        {m.score == null ? '—' : m.score.toFixed(3)}
      </span>
      <div className="flex w-36 shrink-0 items-center justify-end">
        {m.routable
          ? <Switch checked={m.inGateway} onCheckedChange={c => onUse(m, c)} aria-label={`${t('catalog.use')} ${m.name}`} />
          : <NeedsKey provider={m.providers[0]} />}
      </div>
    </div>
  )
}
