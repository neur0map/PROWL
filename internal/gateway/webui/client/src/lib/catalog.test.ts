import { describe, expect, it } from 'vitest'
import { allInGateway, allRowIds, noneInGateway, routableRowIds, type CatalogModel, type CatalogProvider, type KeyStatus } from './catalog'

const provider = (modelDbId: number, keyStatus: KeyStatus): CatalogProvider => ({
  modelDbId,
  platform: 'p' + modelDbId,
  label: 'P' + modelDbId,
  hasKey: keyStatus !== 'none',
  keyStatus,
  priceIn: null,
  priceOut: null,
  contextWindow: null,
  latencyMs: null,
  throughput: null,
  reliability: null,
  requests: null,
  inGateway: true,
})

const model = (providers: CatalogProvider[]): CatalogModel => ({
  id: providers[0].modelDbId,
  canonicalId: 'author/nemotron',
  name: 'Nemotron 3 Nano 30B',
  author: 'author',
  tier: 'free',
  contextWindow: null,
  priceIn: null,
  priceOut: null,
  caps: { tools: false, vision: false, reasoning: false },
  score: null,
  reliability: null,
  speed: null,
  intelligence: null,
  inGateway: true,
  routable: providers.some(p => p.keyStatus === 'healthy' || p.keyStatus === 'unknown'),
  providers,
})

describe('use-toggle row selection', () => {
  // The live "Nemotron 3 Nano 30B" case: one usable row and one keyless row,
  // both chain members. Turning off must clear both or the model still reports
  // inGateway and the switch snaps back on.
  const m = model([provider(310, 'unknown'), provider(351, 'none')])

  it('turning on adds only rows a key can serve', () => {
    expect(routableRowIds(m)).toEqual([310])
  })

  it('turning off removes every row, keyless ones included', () => {
    expect(allRowIds(m)).toEqual([310, 351])
  })

  it('drops error/none rows from the on-set but keeps healthy and unknown', () => {
    const mixed = model([
      provider(1, 'healthy'),
      provider(2, 'unknown'),
      provider(3, 'error'),
      provider(4, 'none'),
    ])
    expect(routableRowIds(mixed)).toEqual([1, 2])
    expect(allRowIds(mixed)).toEqual([1, 2, 3, 4])
  })
})

describe('filtered bulk-state helpers', () => {
  const on = { ...model([provider(1, 'healthy')]), inGateway: true }
  const off = { ...model([provider(2, 'healthy')]), inGateway: false }

  it('allInGateway is true only when every model is already a chain member', () => {
    expect(allInGateway([on, on])).toBe(true)
    expect(allInGateway([on, off])).toBe(false)
  })

  it('noneInGateway is true only when no model is a chain member', () => {
    expect(noneInGateway([off, off])).toBe(true)
    expect(noneInGateway([off, on])).toBe(false)
  })

  it('an empty filtered set can neither be fully on nor removed', () => {
    // The bulk pair is hidden when nothing is filtered; this only pins the
    // helpers' boundary — allInGateway is false with no models to turn on.
    expect(allInGateway([])).toBe(false)
    expect(noneInGateway([])).toBe(true)
  })
})
