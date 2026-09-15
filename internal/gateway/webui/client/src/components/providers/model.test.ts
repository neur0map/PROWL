import { describe, expect, it } from 'vitest'
import type { ProviderDirEntry } from '@/lib/catalog'
import { compareProviders, keyStateOf, signupKeyOf, type SortCol } from './model'

function provider(over: Partial<ProviderDirEntry>): ProviderDirEntry {
  return {
    platform: 'x', label: 'X', access: 'free', hasKey: false, keyStatus: 'none',
    models: 0, routableModels: 0, inGatewayModels: 0, latencyMs: null, reliability: null,
    requests: null, quotaRemaining: null, quotaLimit: null, requiresCard: false,
    signupUrl: null, docsUrl: null, source: 'litellm', ...over,
  }
}

function order(rows: ProviderDirEntry[], col: SortCol, dir: 'asc' | 'desc'): string[] {
  return [...rows].sort((a, b) => compareProviders(a, b, { col, dir })).map(r => r.platform)
}

describe('keyStateOf', () => {
  it('reads no key as missing, an errored probe as failing, and any held key as connected', () => {
    expect(keyStateOf(provider({ hasKey: false }))).toBe('missing')
    expect(keyStateOf(provider({ hasKey: true, keyStatus: 'error' }))).toBe('failing')
    expect(keyStateOf(provider({ hasKey: true, keyStatus: 'healthy' }))).toBe('connected')
    // A held-but-unchecked key is still connected — you have a credential.
    expect(keyStateOf(provider({ hasKey: true, keyStatus: 'unknown' }))).toBe('connected')
  })
})

describe('signupKeyOf', () => {
  it('splits on whether a card is required', () => {
    expect(signupKeyOf(provider({ requiresCard: true }))).toBe('card_required')
    expect(signupKeyOf(provider({ requiresCard: false }))).toBe('card_none')
  })
})

describe('compareProviders', () => {
  it('sinks an unmeasured metric to the bottom in both sort directions', () => {
    const rows = [
      provider({ platform: 'a', reliability: 90 }),
      provider({ platform: 'b', reliability: null }),
      provider({ platform: 'c', reliability: 40 }),
    ]
    expect(order(rows, 'reliability', 'desc')).toEqual(['a', 'c', 'b'])
    expect(order(rows, 'reliability', 'asc')).toEqual(['c', 'a', 'b'])
  })

  it('orders a numeric column by value in each direction', () => {
    const rows = [
      provider({ platform: 'a', models: 10 }),
      provider({ platform: 'b', models: 30 }),
      provider({ platform: 'c', models: 20 }),
    ]
    expect(order(rows, 'models', 'desc')).toEqual(['b', 'c', 'a'])
    expect(order(rows, 'models', 'asc')).toEqual(['a', 'c', 'b'])
  })

  it('sorts provider names case-insensitively', () => {
    const rows = [
      provider({ platform: 'z', label: 'zeta' }),
      provider({ platform: 'a', label: 'Alpha' }),
      provider({ platform: 'm', label: 'mistral' }),
    ]
    expect(order(rows, 'provider', 'asc')).toEqual(['a', 'm', 'z'])
  })

  it('ranks access free before paid before subscription', () => {
    const rows = [
      provider({ platform: 'sub', access: 'subscription' }),
      provider({ platform: 'free', access: 'free' }),
      provider({ platform: 'paid', access: 'paid' }),
    ]
    expect(order(rows, 'access', 'asc')).toEqual(['free', 'paid', 'sub'])
  })
})
