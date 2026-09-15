import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronDown } from 'lucide-react'
import { useI18n } from '@/i18n'
import { apiFetch } from '@/lib/api'
import type {
  KeySelectionStrategy,
  RoutingData,
  RoutingStrategy,
  RoutingWeights,
} from '@/lib/routing'
import { CustomWeightsPopover } from '@/components/custom-weights-popover'
import { PeakHoursControls } from '@/components/peak-hours-controls'
import { Tooltip } from '@/components/tooltip'

// `tKey` is the i18n suffix under `strategies.*` (label) and `strategies.*Blurb`.
// It differs from the routing `key` for Manual, whose strategy id is 'priority'.
const STRATEGIES: { key: RoutingStrategy; tKey: string }[] = [
  { key: 'priority', tKey: 'manual' },
  { key: 'balanced', tKey: 'balanced' },
  { key: 'smartest', tKey: 'smartest' },
  { key: 'fastest', tKey: 'fastest' },
  { key: 'reliable', tKey: 'reliable' },
  { key: 'custom', tKey: 'custom' },
]

// The secondary routing knobs (key selection, exploration, peak hours) live
// behind a "More options" disclosure so the card opens on the strategy pills
// alone. Collapse state is remembered per browser, the same way the chain
// manager and the penalty inspector remember theirs; a fresh install (no
// stored value) starts collapsed.
const OPTIONS_COLLAPSED_KEY = 'freellmapi.routingMoreOptions.collapsed'

function readOptionsCollapsed(): boolean {
  try {
    const stored = localStorage.getItem(OPTIONS_COLLAPSED_KEY)
    return stored === null ? true : stored === '1'
  } catch {
    return true
  }
}

export function StrategyCard() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [optionsCollapsed, setOptionsCollapsed] = useState(readOptionsCollapsed)

  const { data: routing } = useQuery<RoutingData>({
    queryKey: ['fallback', 'routing'],
    queryFn: () => apiFetch('/api/fallback/routing'),
    refetchInterval: 15_000,
  })

  const strategyMutation = useMutation({
    mutationFn: (payload: {
      strategy: RoutingStrategy; weights?: RoutingWeights; exploreEnabled?: boolean
      peakHoursAdjust?: boolean; peakStartHour?: number; peakEndHour?: number; peakTimezone?: string
      keySelectionStrategy?: KeySelectionStrategy; cooldownCeilingMs?: number | null
    }) =>
      apiFetch('/api/fallback/routing', { method: 'PUT', body: JSON.stringify(payload) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['fallback', 'routing'] }),
  })

  const strategy: RoutingStrategy = routing?.strategy ?? 'balanced'
  const keySelection: KeySelectionStrategy = routing?.keySelectionStrategy ?? 'auto'
  const isManual = strategy === 'priority'

  function toggleOptions() {
    setOptionsCollapsed(prev => {
      const next = !prev
      try { localStorage.setItem(OPTIONS_COLLAPSED_KEY, next ? '1' : '0') } catch { /* ignore */ }
      return next
    })
  }

  return (
    <section className="rounded-3xl border bg-card p-5">
      <div className="flex items-baseline justify-between mb-3">
        <h2 className="text-sm font-medium">{t('strategies.title')}</h2>
        {routing?.weights && (
          <span className="text-xs text-muted-foreground tabular-nums">
            {t('strategies.weightsSummary', {
              reliability: Math.round(routing.weights.reliability * 100),
              speed: Math.round(routing.weights.speed * 100),
              intelligence: Math.round(routing.weights.intelligence * 100),
            })}
            {/* These numbers are not the preset's while the peak-hours
                adjustment is firing, so say so right where they are read. */}
            {routing.peakAdjusted && (
              <Tooltip text={t('strategies.peakActiveHint')}>
                <span className="ml-1 cursor-help underline decoration-dotted underline-offset-2">
                  {t('strategies.peakActive')}
                </span>
              </Tooltip>
            )}
          </span>
        )}
      </div>

      {/* Pills left, the quiet "More options" disclosure right. Everything
          secondary hangs off that toggle so the card reads as one choice. */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="inline-flex flex-wrap items-center gap-1 rounded-xl border p-1">
          {STRATEGIES.map(s => (
            <Tooltip key={s.key} text={t(`strategies.${s.tKey}Blurb`)}>
              <button
                disabled={strategyMutation.isPending}
                onClick={() => strategyMutation.mutate({ strategy: s.key })}
                className={`px-3 py-1.5 text-xs rounded-lg transition-colors ${
                  s.key === strategy
                    ? 'bg-foreground text-background font-medium'
                    : 'text-muted-foreground hover:text-foreground hover:bg-muted'
                }`}
              >
                {t(`strategies.${s.tKey}`)}
              </button>
            </Tooltip>
          ))}
          {strategy === 'custom' && routing && (
            <CustomWeightsPopover
              saved={routing.customWeights}
              saving={strategyMutation.isPending}
              onSave={w => strategyMutation.mutate({ strategy: 'custom', weights: w })}
            />
          )}
        </div>

        <button
          type="button"
          onClick={toggleOptions}
          aria-expanded={!optionsCollapsed}
          className="inline-flex items-center gap-1 rounded-lg px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          {t('strategies.moreOptions')}
          <ChevronDown className={`size-3.5 transition-transform ${optionsCollapsed ? '-rotate-90' : ''}`} />
        </button>
      </div>

      <p className="mt-2 text-xs text-muted-foreground">
        {isManual ? t('strategies.modeManualHint') : t('strategies.modeScoreHint')}
      </p>

      {!optionsCollapsed && (
        <div className="mt-3 flex flex-col items-start gap-2 border-t pt-3">
          {/* Key selection (#919). A separate knob from the strategy above:
              that one ranks MODELS, this one picks between several keys of
              the same provider. Shown in every mode — manual chain order
              still leaves the choice of key open. */}
          <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t('strategies.keySelection')}</span>
            <select
              value={keySelection}
              disabled={strategyMutation.isPending}
              onChange={e => strategyMutation.mutate({ strategy, keySelectionStrategy: e.target.value as KeySelectionStrategy })}
              className="rounded-lg border bg-background px-2 py-1.5 text-xs text-foreground"
            >
              <option value="auto">{t('strategies.keySelectionAuto')}</option>
              <option value="least-remaining">{t('strategies.keySelectionLeastRemaining')}</option>
            </select>
            <Tooltip text={t('strategies.keySelectionHint')}>
              <span className="cursor-help underline decoration-dotted underline-offset-2">?</span>
            </Tooltip>
          </label>

          {/* Cooldown ceiling (#952). Caps the router's OWN bench guesses
              (escalation ladder, 402/403 day benches); provider-stated
              retry times are never shortened. Shown in every mode: the
              ladder runs regardless of how models are ranked. */}
          <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t('strategies.cooldownCeiling')}</span>
            <select
              value={routing?.cooldownCeilingMs == null ? '' : String(routing.cooldownCeilingMs)}
              disabled={strategyMutation.isPending}
              onChange={e => strategyMutation.mutate({ strategy, cooldownCeilingMs: e.target.value === '' ? null : Number(e.target.value) })}
              className="rounded-lg border bg-background px-2 py-1.5 text-xs text-foreground"
            >
              <option value="">{t('strategies.cooldownCeilingDefault')}</option>
              <option value={String(10 * 60_000)}>{t('strategies.cooldownCeiling10m')}</option>
              <option value={String(60 * 60_000)}>{t('strategies.cooldownCeiling1h')}</option>
              <option value={String(6 * 60 * 60_000)}>{t('strategies.cooldownCeiling6h')}</option>
            </select>
            <Tooltip text={t('strategies.cooldownCeilingHint')}>
              <span className="cursor-help underline decoration-dotted underline-offset-2">?</span>
            </Tooltip>
          </label>

          {/* Exploration toggle (#685 follow-up): a niche knob that gives
              unmeasured models a guaranteed chance to be tried so they build
              reliability/speed data. Hidden in Manual mode, where
              routeRequest ignores it. */}
          {!isManual && (
            <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={routing?.exploreEnabled ?? false}
                disabled={strategyMutation.isPending}
                onChange={e => strategyMutation.mutate({ strategy, exploreEnabled: e.target.checked })}
                className="size-3.5 accent-foreground"
              />
              <span>{t('strategies.explore')}</span>
              <Tooltip text={t('strategies.exploreHint')}>
                <span className="cursor-help underline decoration-dotted underline-offset-2">?</span>
              </Tooltip>
            </label>
          )}

          {/* Peak-hours adjustment (#760): an opt-in tweak to the preset
              weights, and off it does nothing at all. Its "(peak hours)"
              marker on the weight summary above stays visible either way —
              that one explains live behaviour. */}
          {!isManual && routing && (
            <PeakHoursControls
              routing={routing}
              strategy={strategy}
              saving={strategyMutation.isPending}
              onSave={p => strategyMutation.mutate({ strategy, ...p })}
            />
          )}
        </div>
      )}
    </section>
  )
}
