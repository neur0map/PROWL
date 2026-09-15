import { AxisBar, AxisNoData } from '@/components/model-table'
import { META_CHIP } from '@/components/keys/provider-catalogue'
import type { DirectoryProvider, ProviderClass } from '@/components/keys/provider-catalogue'
import type { ProviderDirEntry } from '@/lib/catalog'
import { useI18n } from '@/i18n'
import type { AccessKey, KeyStateKey } from './model'

// The class-dot palette reused from the provider catalogue so a free/paid/
// subscription provider reads the same colour everywhere it appears.
const ACCESS_DOT: Record<AccessKey, string> = {
  free: '#22c55e',
  paid: '#6b7280',
  subscription: '#f59e0b',
}

const KEY_STATE_DOT: Record<KeyStateKey, string> = {
  connected: 'bg-emerald-500',
  failing: 'bg-rose-500',
  missing: 'bg-muted-foreground/40',
}

export function AccessChip({ access }: { access: AccessKey }) {
  const { t } = useI18n()
  return (
    <span className={`${META_CHIP} inline-flex items-center gap-1 bg-muted text-muted-foreground`}>
      <span className="size-2 rounded-sm" style={{ background: ACCESS_DOT[access] }} />
      {t(`providerDir.access_${access}`)}
    </span>
  )
}

export function KeyStateChip({ state }: { state: KeyStateKey }) {
  const { t } = useI18n()
  return (
    <span className={`${META_CHIP} inline-flex items-center gap-1 bg-muted text-muted-foreground`}>
      <span className={`size-1.5 rounded-full ${KEY_STATE_DOT[state]}`} />
      {t(`providerDir.state_${state}`)}
    </span>
  )
}

// Reliability as the model table's axis: a measured value only exists once the
// provider served traffic, so an unmeasured one gets the explicit no-data
// placeholder instead of a bar sitting at zero.
export function ReliabilityBar({ value }: { value: number | null }) {
  if (value === null) return <AxisNoData />
  return <AxisBar value={value / 100} color="#22c55e" />
}

// The drawer speaks DirectoryProvider (the /api/providers/directory shape). The
// directory query normally holds every platform, but a catalogue row is mapped
// as a fallback so a click always opens the drawer rather than silently no-op.
export function toDirectoryProvider(p: ProviderDirEntry): DirectoryProvider {
  const cls: ProviderClass = p.access === 'free' ? 'free' : p.access === 'subscription' ? 'oauth' : 'paid'
  return {
    id: p.platform,
    name: p.label,
    class: cls,
    friction: p.requiresCard ? 'card' : 'registration',
    freeModels: p.access === 'free' ? p.models : 0,
    maxContext: 0,
    modalities: [],
    apiKeyUrl: p.signupUrl ?? '',
    docsUrl: p.docsUrl ?? '',
    env: '',
    routable: p.routableModels > 0,
    sources: p.source ? [p.source] : [],
    probe: '',
    note: undefined,
    configured: p.hasKey,
    keyCount: p.hasKey ? 1 : 0,
    modelCount: p.inGatewayModels,
    adapter: true,
    platform: p.platform,
    keyless: false,
  }
}
