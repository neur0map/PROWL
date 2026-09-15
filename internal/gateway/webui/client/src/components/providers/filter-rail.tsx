import { useI18n } from '@/i18n'
import type { AccessKey, KeyStateKey, SignupKey } from './model'

interface FacetOption<T extends string> {
  key: T
  label: string
  count: number
}

function FacetGroup<T extends string>({
  title,
  group,
  options,
  selected,
  onToggle,
}: {
  title: string
  group: string
  options: FacetOption<T>[]
  selected: Set<T>
  onToggle: (key: T) => void
}) {
  return (
    <div>
      <h3 className="px-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">{title}</h3>
      <div className="mt-1.5 space-y-0.5">
        {options.map(o => {
          const active = selected.has(o.key)
          const disabled = o.count === 0 && !active
          const id = `facet-${group}-${o.key}`
          return (
            <label
              key={o.key}
              htmlFor={id}
              className={`flex items-center gap-2 rounded-lg px-2 py-1 text-xs transition-colors ${
                disabled ? 'cursor-not-allowed opacity-40' : 'cursor-pointer hover:bg-muted'
              } ${active ? 'text-foreground' : 'text-muted-foreground'}`}
            >
              <input
                id={id}
                type="checkbox"
                checked={active}
                disabled={disabled}
                onChange={() => onToggle(o.key)}
                className="size-3.5 shrink-0 rounded border-input accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
              <span className="min-w-0 flex-1 truncate">{o.label}</span>
              <span className="tabular-nums opacity-60">{o.count}</span>
            </label>
          )
        })}
      </div>
    </div>
  )
}

export function FilterRail({
  access,
  keyState,
  signup,
  accessCounts,
  keyCounts,
  signupCounts,
  onToggleAccess,
  onToggleKey,
  onToggleSignup,
  onClear,
}: {
  access: Set<AccessKey>
  keyState: Set<KeyStateKey>
  signup: Set<SignupKey>
  accessCounts: Record<AccessKey, number>
  keyCounts: Record<KeyStateKey, number>
  signupCounts: Record<SignupKey, number>
  onToggleAccess: (key: AccessKey) => void
  onToggleKey: (key: KeyStateKey) => void
  onToggleSignup: (key: SignupKey) => void
  onClear: () => void
}) {
  const { t } = useI18n()
  const anySelected = access.size + keyState.size + signup.size > 0

  const accessOptions: FacetOption<AccessKey>[] = [
    { key: 'free', label: t('providerDir.access_free'), count: accessCounts.free },
    { key: 'paid', label: t('providerDir.access_paid'), count: accessCounts.paid },
    { key: 'subscription', label: t('providerDir.access_subscription'), count: accessCounts.subscription },
  ]
  const keyOptions: FacetOption<KeyStateKey>[] = [
    { key: 'connected', label: t('providerDir.state_connected'), count: keyCounts.connected },
    { key: 'failing', label: t('providerDir.state_failing'), count: keyCounts.failing },
    { key: 'missing', label: t('providerDir.state_missing'), count: keyCounts.missing },
  ]
  const signupOptions: FacetOption<SignupKey>[] = [
    { key: 'card_required', label: t('providerDir.card_required'), count: signupCounts.card_required },
    { key: 'card_none', label: t('providerDir.card_none'), count: signupCounts.card_none },
  ]

  return (
    <aside className="w-full shrink-0 space-y-5 md:w-52">
      {anySelected && (
        <button
          type="button"
          onClick={onClear}
          className="rounded-lg px-2 text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {t('catalog.clear')}
        </button>
      )}
      <FacetGroup title={t('providerDir.access')} group="access" options={accessOptions} selected={access} onToggle={onToggleAccess} />
      <FacetGroup title={t('providerDir.keyState')} group="key" options={keyOptions} selected={keyState} onToggle={onToggleKey} />
      <FacetGroup title={t('providerDir.signup')} group="signup" options={signupOptions} selected={signup} onToggle={onToggleSignup} />
    </aside>
  )
}
