import type { ProviderDirEntry, Tier } from '@/lib/catalog'

// Pure directory model — the facet keys, the key-state derivation and the
// sort comparator. Kept free of React so it can be reasoned about and tested
// on its own.

export type AccessKey = Tier
export type KeyStateKey = 'connected' | 'failing' | 'missing'
export type SignupKey = 'card_required' | 'card_none'

export type SortCol =
  | 'provider'
  | 'access'
  | 'key'
  | 'models'
  | 'routable'
  | 'latency'
  | 'reliability'
  | 'quota'
  | 'requests'

export interface SortState {
  col: SortCol
  dir: 'asc' | 'desc'
}

// A fresh column sorts the way that column reads best: names and low-cardinality
// ranks ascending, measured metrics highest-first.
export const DEFAULT_DIR: Record<SortCol, 'asc' | 'desc'> = {
  provider: 'asc',
  access: 'asc',
  key: 'asc',
  models: 'desc',
  routable: 'desc',
  latency: 'asc',
  reliability: 'desc',
  quota: 'desc',
  requests: 'desc',
}

const ACCESS_RANK: Record<AccessKey, number> = { free: 0, paid: 1, subscription: 2 }
const KEY_RANK: Record<KeyStateKey, number> = { connected: 0, failing: 1, missing: 2 }

// A held-but-unchecked key still counts as connected: you have a credential,
// only its last probe is unknown. Failing is reserved for a probe that failed.
export function keyStateOf(p: ProviderDirEntry): KeyStateKey {
  if (!p.hasKey) return 'missing'
  if (p.keyStatus === 'error') return 'failing'
  return 'connected'
}

export function signupKeyOf(p: ProviderDirEntry): SignupKey {
  return p.requiresCard ? 'card_required' : 'card_none'
}

function sortValue(p: ProviderDirEntry, col: SortCol): number | string | null {
  switch (col) {
    case 'provider': return p.label.toLowerCase()
    case 'access': return ACCESS_RANK[p.access]
    case 'key': return KEY_RANK[keyStateOf(p)]
    case 'models': return p.models
    case 'routable': return p.routableModels
    case 'latency': return p.latencyMs
    case 'reliability': return p.reliability
    case 'quota': return p.quotaRemaining
    case 'requests': return p.requests
  }
}

// Comparator for the directory. An unmeasured metric (null) always sinks to the
// bottom, whichever way the column is sorted, so a sort never buries the rows
// that do have numbers under the ones that do not.
export function compareProviders(a: ProviderDirEntry, b: ProviderDirEntry, sort: SortState): number {
  const av = sortValue(a, sort.col)
  const bv = sortValue(b, sort.col)
  if (av === null && bv === null) return 0
  if (av === null) return 1
  if (bv === null) return -1
  const cmp = typeof av === 'string' && typeof bv === 'string' ? av.localeCompare(bv) : Number(av) - Number(bv)
  return sort.dir === 'asc' ? cmp : -cmp
}
