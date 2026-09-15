import { apiFetch } from '@/lib/api'

// The model-browse / provider-directory wire contract, shared by the catalog
// page, the per-model detail page, and the provider directory so all three
// speak one set of names. Every measured number is `number | null`: an unknown
// figure is null and renders as an em dash, never a fabricated zero.

export type Modality = 'chat' | 'embeddings' | 'image' | 'video' | 'audio'
export type Tier = 'free' | 'paid' | 'subscription'
export type KeyStatus = 'healthy' | 'error' | 'unknown' | 'none'

export interface CatalogCaps {
  tools: boolean
  vision: boolean
  reasoning: boolean
}

// One platform that serves a logical model, with its own price, key state and
// live performance.
export interface CatalogProvider {
  modelDbId: number
  platform: string
  label: string
  hasKey: boolean
  keyStatus: KeyStatus
  priceIn: number | null
  priceOut: number | null
  contextWindow: number | null
  latencyMs: number | null
  throughput: number | null
  reliability: number | null
  requests: number | null
  inGateway: boolean
}

// One logical model, unifying every platform that serves it under the id a
// client sends (`canonicalId`).
export interface CatalogModel {
  id: number
  canonicalId: string
  name: string
  author: string
  tier: Tier
  contextWindow: number | null
  priceIn: number | null
  priceOut: number | null
  caps: CatalogCaps
  score: number | null
  reliability: number | null
  speed: number | null
  intelligence: number | null
  inGateway: boolean
  routable: boolean
  providers: CatalogProvider[]
}

export interface CatalogAvailabilityFacet {
  inGateway: number
  routable: number
  needsKey: number
}

export interface CatalogProviderFacet {
  platform: string
  label: string
  models: number
}

export interface CatalogAuthorFacet {
  author: string
  models: number
}

export interface CatalogFacets {
  tier: Record<Tier, number>
  availability: CatalogAvailabilityFacet
  caps: Record<keyof CatalogCaps, number>
  providers: CatalogProviderFacet[]
  authors: CatalogAuthorFacet[]
  modalities: Record<Modality, number>
}

export interface CatalogResponse {
  total: number
  models: CatalogModel[]
  facets: CatalogFacets
}

// One provider row for the provider directory (/api/catalog/providers).
export interface ProviderDirEntry {
  platform: string
  label: string
  access: Tier
  hasKey: boolean
  keyStatus: KeyStatus
  models: number
  routableModels: number
  inGatewayModels: number
  latencyMs: number | null
  reliability: number | null
  requests: number | null
  quotaRemaining: number | null
  quotaLimit: number | null
  requiresCard: boolean
  signupUrl: string | null
  docsUrl: string | null
  source: string
}

export interface ProvidersResponse {
  total: number
  providers: ProviderDirEntry[]
}

// The full model list for one modality, with the facet counts that drive the
// filter rail. `modality` is also the react-query key segment, so it is a
// union rather than a bare string.
export function fetchCatalog(modality: Modality): Promise<CatalogResponse> {
  return apiFetch(`/api/catalog/models?modality=${modality}`)
}

// Add or remove these rows from the active routing list without disturbing the
// order of untouched members. Returns the new in-gateway count.
export function setModelsInGateway(
  modelDbIds: number[],
  use: boolean,
): Promise<{ inGateway: number }> {
  return apiFetch('/api/catalog/use', {
    method: 'PUT',
    body: JSON.stringify({ modelDbIds, use }),
  })
}

// The rows to ADD when a model is turned on: only those whose key can serve it
// (healthy, or not yet checked). keyStatus is model-scoped. Adding a keyless
// row here would seat an unroutable candidate in the chain.
export function routableRowIds(model: CatalogModel): number[] {
  return model.providers
    .filter(p => p.keyStatus === 'healthy' || p.keyStatus === 'unknown')
    .map(p => p.modelDbId)
}

// The rows to REMOVE when a model is turned off: every row, keyless ones
// included. A model reports inGateway if ANY row is a chain member, so leaving
// a keyless member behind makes the switch snap back on. Turning off means
// "route to none of it", so it must clear them all — the asymmetry with
// routableRowIds is deliberate.
export function allRowIds(model: CatalogModel): number[] {
  return model.providers.map(p => p.modelDbId)
}

// Whether every model in a set is already a chain member. Drives the "Use all"
// disabled state: once nothing is left to add the button greys out. Empty in
// means false — there is nothing to turn on.
export function allInGateway(models: CatalogModel[]): boolean {
  return models.length > 0 && models.every(m => m.inGateway)
}

// Whether no model in a set is a chain member. Drives "Remove all" — with
// nothing routed there is nothing to remove.
export function noneInGateway(models: CatalogModel[]): boolean {
  return models.every(m => !m.inGateway)
}

export function fetchProviders(): Promise<ProvidersResponse> {
  return apiFetch('/api/catalog/providers')
}

// ── Sets (named routing lists) ───────────────────────────────────────────────
// A "set" is a named list the router routes through, exposed elsewhere as a
// chain. The catalog page only needs each set's id and name to switch between
// them, so this is a lean view of the /api/profiles row, not its full shape.
export interface ProfileSet {
  id: number
  name: string
}

export type PresetGroup = 'task' | 'catalogue'

// A preset is a named predicate over the catalogue. `models` is counted live by
// the server (the number you pick is the number you get) and `requirements` is
// generated from that predicate, so the chips never drift from what it selects.
export interface ProfilePreset {
  id: string
  name: string
  description: string
  group: PresetGroup
  requirements: string[]
  models: number
}

export interface FromSelectionResult {
  id: number
  name: string
  models: number
  callAs: string
  active: boolean
}

export function fetchProfiles(): Promise<ProfileSet[]> {
  return apiFetch('/api/profiles')
}

export function fetchActiveProfile(): Promise<{ activeProfileId: number | null }> {
  return apiFetch('/api/profiles/active')
}

export function fetchPresets(): Promise<{ presets: ProfilePreset[] }> {
  return apiFetch('/api/profiles/presets')
}

export function activateProfile(profileId: number): Promise<{ activeProfileId: number | null }> {
  return apiFetch('/api/profiles/active', {
    method: 'POST',
    body: JSON.stringify({ profileId }),
  })
}

// Build a new list from a preset's predicate and, when `activate`, make it the
// one the router uses. The server fills and ranks it from the same predicate it
// counted for the preset.
export function createSetFromPreset(id: string, name: string, activate: boolean): Promise<{ active: boolean }> {
  return apiFetch(`/api/profiles/presets/${id}`, {
    method: 'POST',
    body: JSON.stringify({ name, activate }),
  })
}

// Save a hand-picked selection as a new set, in the order given. Rejects an
// empty selection (400) and a duplicate name (409) — the caller surfaces the
// 409 next to the name field so the typed name survives.
export function createSetFromSelection(
  name: string,
  modelDbIds: number[],
  activate: boolean,
): Promise<FromSelectionResult> {
  return apiFetch('/api/profiles/from-selection', {
    method: 'POST',
    body: JSON.stringify({ name, modelDbIds, activate }),
  })
}

// Where each modality's per-model detail page lives. A Record rather than a
// bare `/models/${modality}` template so a modality whose detail route later
// diverges is a one-line change, and so callers cannot land on an unrouted
// path for a modality that has no detail page.
export const MODALITY_PATH: Record<Modality, string> = {
  chat: '/models/chat',
  embeddings: '/models/embeddings',
  image: '/models/image',
  video: '/models/video',
  audio: '/models/audio',
}

export function modelDetailHref(modality: Modality, canonicalId: string): string {
  return `${MODALITY_PATH[modality]}/${encodeURIComponent(canonicalId)}`
}
