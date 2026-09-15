import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { ModelCombobox } from '@/components/model-combobox'
import { FieldError } from '@/components/ui/field-error'
import type { ApiKey, Platform } from '../../../../shared/types'
import { useI18n } from '@/i18n'
import { toast } from '@/lib/toast'
import type { FallbackEntry } from '@/lib/routing'
import { scopeCandidates, shouldOfferModelPicker, type ScopeCandidate } from '@/lib/model-scope-selection'
import { GetKeyLink, PLATFORMS } from './shared'

/** A key that just landed, plus the models the picker should offer for it.
 *  Only produced when the picker is actually worth showing (#657) — otherwise
 *  the add stays as silent as it has always been. */
export interface AddedKeyScopeOffer {
  keyId: number
  platformLabel: string
  candidates: ScopeCandidate[]
}

// The "Provider key" pane of the Add key dialog: paste a credential for a known
// provider. Extracted verbatim from the old inline KeysPage form so all field
// validation, the keyless/Cloudflare special cases, and the POST /api/keys
// mutation stay identical. On success it toasts and asks the dialog to close.
// `initialPlatform` preselects the provider (checklist-chip entry); the field
// stays editable. The dialog remounts this pane per open, so a plain initial
// state is enough.
//
// `onSuccess` may carry a scope offer for the key that was just added; the
// dialog owns that follow-up because this pane unmounts the moment it closes.
// Marks an option the gateway has no wire adapter for. Selecting it hands the
// user to the custom-endpoint pane instead of setting an unroutable platform.
const CUSTOM_OPTION_PREFIX = 'custom-endpoint:'

interface DirectoryResponse {
  providers: {
    id: string
    name: string
    platform: string
    adapter: boolean
    keyless: boolean
    apiKeyUrl: string
    configured: boolean
  }[]
}

export function AddKeyForm({ onSuccess, initialPlatform, onUseCustomEndpoint }: {
  onSuccess: (offer?: AddedKeyScopeOffer) => void
  initialPlatform?: Platform
  // Offered when the provider the user wants is not one Prowl has an adapter
  // for. Omitted, the hint is not shown.
  onUseCustomEndpoint?: () => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [platform, setPlatform] = useState<Platform | ''>(initialPlatform ?? '')
  const [apiKey, setApiKey] = useState('')
  const [accountId, setAccountId] = useState('')
  const [label, setLabel] = useState('')
  const [addAttempted, setAddAttempted] = useState(false)
  // Several credentials for one provider in one go (#705). Pooling keys is the
  // point of this app, and the only bulk path was the file importer, so anyone
  // holding five Groq keys reopened this dialog five times. Off by default: the
  // single-key field masks what you type, and a textarea cannot.
  const [several, setSeveral] = useState(false)

  // #707: the platform dropdown had no search and no way to skip providers that
  // already have keys, which is painful at thirty-odd entries. The shared
  // ModelCombobox already does search + arrow keys, so this reuses it and only
  // supplies the options. PLATFORMS keeps its curated order — it is sorted by
  // recommendation, not alphabetically — and the search box handles "find it
  // fast". The added/not-added split reads the same ['keys'] query the
  // Providers tab owns, so it costs no extra request.
  const { data: keys = [] } = useQuery<ApiKey[]>({
    queryKey: ['keys'],
    queryFn: () => apiFetch('/api/keys'),
  })
  const addedPlatforms = useMemo(() => new Set(keys.map(k => k.platform)), [keys])
  const [hideAdded, setHideAdded] = useState(false)

  // #657 post-add scope picker: the platform's model list. This is the same
  // ['fallback'] query the Models page, the fallback chain and the command
  // palette already run — react-query dedupes it, so opening this pane costs at
  // most one GET that the rest of the dashboard was going to make anyway. That
  // is why the picker needs no new endpoint and no widening of POST /api/keys
  // (whose response only carries a model COUNT, not the ids). An empty or
  // still-loading list simply means no picker, which is the old behaviour.
  const { data: catalog = [] } = useQuery<FallbackEntry[]>({
    queryKey: ['fallback'],
    queryFn: () => apiFetch('/api/fallback'),
  })

  // Whether the key that just landed is worth offering the picker for, and the
  // rows to offer. Anything missing — no id back, a keyless sentinel, a catalog
  // too small or not loaded — returns undefined, and the add stays silent.
  function scopeOffer(keyId: number | undefined, added: string): AddedKeyScopeOffer | undefined {
    if (typeof keyId !== 'number') return undefined
    const provider = PLATFORMS.find(p => p.value === added)
    // Keyless gateways are excluded from scope editing on the key row too — no
    // credential means nothing was bought per model group.
    if (!provider || provider.keyless) return undefined
    const candidates = scopeCandidates(catalog, added)
    if (!shouldOfferModelPicker(candidates)) return undefined
    return { keyId, platformLabel: provider.label, candidates }
  }

  // Every provider the catalogue lists, not a second hardcoded copy of it.
  // The Providers tab already runs this query, so react-query dedupes it and
  // the picker costs no extra request. Providers with no wire adapter stay
  // selectable and route to the custom-endpoint pane, which is the only way
  // they can actually be reached.
  const { data: directory } = useQuery<DirectoryResponse>({
    queryKey: ['providers-directory'],
    queryFn: () => apiFetch('/api/providers/directory'),
  })

  const platformOptions = useMemo(() => {
    const entries = directory?.providers ?? []
    if (entries.length === 0) {
      return PLATFORMS
        .filter(p => !hideAdded || !addedPlatforms.has(p.value) || p.value === platform)
        .map(p => ({
          value: p.value,
          label: p.label,
          sub: addedPlatforms.has(p.value) ? t('keys.discoverAlreadyAdded') : undefined,
        }))
    }
    return entries
      .map(entry => ({
        value: entry.adapter ? entry.platform : `${CUSTOM_OPTION_PREFIX}${entry.id}`,
        label: entry.name,
        sub: entry.adapter && addedPlatforms.has(entry.platform as Platform)
          ? t('keys.discoverAlreadyAdded')
          : undefined,
        note: entry.adapter ? undefined : t('keys.customEndpointOnly'),
      }))
      // The selected provider always stays listed, so hiding added ones never
      // blanks out the trigger label.
      .filter(o => !hideAdded || o.sub === undefined || o.value === platform)
  }, [directory, hideAdded, addedPlatforms, platform, t])

  const addKey = useMutation({
    meta: { silenceToast: true },
    mutationFn: (body: { platform: string; key: string; label?: string }) =>
      apiFetch<{ id: number; notice?: string | null }>('/api/keys', { method: 'POST', body: JSON.stringify(body) }),
    onSuccess: (data, variables) => {
      queryClient.invalidateQueries({ queryKey: ['keys'] })
      queryClient.invalidateQueries({ queryKey: ['health'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      queryClient.invalidateQueries({ queryKey: ['keys-providers'] })
      toast.success(t('keys.keyAdded'))
      // Server notice when the key is for a platform with no models in the
      // current catalog tier yet (#438) — surfaced as a toast now that the
      // dialog closes on success.
      if (data?.notice) toast.info(data.notice)
      onSuccess(scopeOffer(data?.id, variables.platform))
    },
  })

  // Reuses the bulk endpoint the file importer already posts to, which dedupes
  // against every stored key and reports per-key failures.
  const addSeveral = useMutation({
    meta: { silenceToast: true },
    mutationFn: (body: { keys: { platform: string; keyName?: string; keyValue: string }[] }) =>
      apiFetch<{ imported: number; total: number; errors: { key: string; error: string }[] }>(
        '/api/keys/import-selected', { method: 'POST', body: JSON.stringify(body) },
      ),
    onSuccess: (data) => {
      for (const key of ['keys', 'health', 'fallback', 'keys-providers']) {
        queryClient.invalidateQueries({ queryKey: [key] })
      }
      toast.success(t('keys.importResult', { imported: data.imported, failed: data.total - data.imported }))
      onSuccess()
    },
  })

  const needsAccountId = platform === 'cloudflare'
  const isKeyless = directory?.providers.find(p => p.adapter && p.platform === platform)?.keyless
    ?? PLATFORMS.find(p => p.value === platform)?.keyless
    ?? false
  // Cloudflare pairs each token with an account id, and keyless providers have
  // nothing to paste, so neither can take a list.
  const canPasteSeveral = !isKeyless && !needsAccountId
  const severalMode = several && canPasteSeveral
  // One per line or comma-separated, deduped, blanks dropped.
  const keyList = severalMode
    ? [...new Set(apiKey.split(/[\n,]+/).map(s => s.trim()).filter(Boolean))]
    : []

  // Field-level validation: the submit stays clickable and reveals what is
  // missing instead of being silently disabled.
  const platformError = !platform ? t('validation.required') : null
  const keyError = severalMode
    ? (keyList.length === 0 ? t('validation.required') : null)
    : (!isKeyless && !apiKey.trim() ? t('validation.required') : null)
  const accountIdError = needsAccountId && !accountId.trim() ? t('validation.required') : null
  const pending = addKey.isPending || addSeveral.isPending

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (platformError || keyError || accountIdError) {
      setAddAttempted(true)
      return
    }
    setAddAttempted(false)
    if (severalMode) {
      addSeveral.mutate({
        keys: keyList.map(keyValue => ({ platform, keyValue, keyName: label || undefined })),
      })
      return
    }
    // Keyless providers submit an empty key; the backend stores a sentinel.
    const key = isKeyless ? '' : (needsAccountId ? `${accountId}:${apiKey}` : apiKey)
    addKey.mutate({ platform, key, label: label || undefined })
  }

  return (
    <div>
      <form onSubmit={handleSubmit} className="flex flex-wrap gap-3">
        <div className="space-y-1.5">
          <Label className="text-xs">{t('keys.platform')}</Label>
          <ModelCombobox
            value={platform}
            options={platformOptions}
            onSelect={v => {
              // A provider with no adapter cannot be a platform value; the
              // only way to reach it is the custom-endpoint form.
              if (v.startsWith(CUSTOM_OPTION_PREFIX)) {
                onUseCustomEndpoint?.()
                return
              }
              setPlatform(v as Platform)
            }}
            ariaLabel={t('keys.platform')}
            triggerPlaceholder={t('keys.selectPlatform')}
            placeholder={t('keys.filterPlaceholder')}
            emptyText={t('keys.noFilterMatch')}
            align="start"
            ariaInvalid={addAttempted && !!platformError}
            triggerClassName={`flex h-8 w-[220px] items-center justify-between gap-2 whitespace-nowrap rounded-lg border bg-transparent px-3 text-sm outline-none transition-colors hover:bg-muted/50 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30 ${addAttempted && platformError ? 'border-destructive' : 'border-input'}`}
            header={
              <div className="flex items-center gap-2 border-b px-3 py-2">
                <input
                  id="hide-added-platforms"
                  type="checkbox"
                  checked={hideAdded}
                  onChange={e => setHideAdded(e.target.checked)}
                  className="size-3.5 accent-foreground"
                />
                <label htmlFor="hide-added-platforms" className="text-xs text-muted-foreground">
                  {t('keys.hideAdded')}
                </label>
              </div>
            }
          />
          {addAttempted && <FieldError error={platformError} />}
          {(() => {
            const fromDirectory = directory?.providers.find(p => p.adapter && p.platform === platform)?.apiKeyUrl
            const url = fromDirectory || PLATFORMS.find(p => p.value === platform)?.url
            return url ? <div className="pt-0.5"><GetKeyLink url={url} /></div> : null
          })()}
          {/* This list is the set of providers Prowl has a wire adapter for,
              which is far smaller than the catalogue on the Providers tab.
              Without saying so, searching for anything else looks like "not
              supported" when in fact any OpenAI-compatible endpoint routes
              through the custom pane. */}
          {onUseCustomEndpoint && (
            <p className="pt-1 text-xs text-muted-foreground">
              {t('keys.providerNotListed')}{' '}
              <button
                type="button"
                onClick={onUseCustomEndpoint}
                className="underline underline-offset-2 hover:text-foreground"
              >
                {t('keys.addAsCustomEndpoint')}
              </button>
            </p>
          )}
        </div>
        {needsAccountId && (
          <div className="space-y-1.5">
            <Label className="text-xs">{t('keys.accountId')}</Label>
            <Input
              value={accountId}
              onChange={e => setAccountId(e.target.value)}
              placeholder="a1b2c3d4…"
              className="w-[200px] font-mono text-xs"
              aria-invalid={addAttempted && !!accountIdError}
            />
            {addAttempted && <FieldError error={accountIdError} />}
          </div>
        )}
        <div className="space-y-1.5 flex-1 min-w-[240px]">
          <div className="flex items-center justify-between gap-2">
            <Label className="text-xs">{needsAccountId ? t('keys.apiToken') : t('keys.customApiKey')}</Label>
            {canPasteSeveral && (
              <button
                type="button"
                onClick={() => setSeveral(v => !v)}
                className={`text-[11px] underline-offset-2 hover:underline ${severalMode ? 'text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
              >
                {t('keys.pasteSeveral')}
              </button>
            )}
          </div>
          {severalMode ? (
            <Textarea
              value={apiKey}
              onChange={e => setApiKey(e.target.value)}
              placeholder={'gsk_first…\ngsk_second…'}
              rows={3}
              className="font-mono text-xs"
              aria-invalid={addAttempted && !!keyError}
            />
          ) : (
            <Input
              type="password"
              value={isKeyless ? '' : apiKey}
              onChange={e => setApiKey(e.target.value)}
              placeholder={isKeyless ? t('keys.noKeyNeededPlaceholder') : (needsAccountId ? t('keys.bearerTokenPlaceholder') : t('keys.pasteKeyPlaceholder'))}
              className="font-mono text-xs"
              disabled={isKeyless}
              aria-invalid={addAttempted && !!keyError}
            />
          )}
          {addAttempted && <FieldError error={keyError} />}
          {isKeyless && (
            <p className="text-[11px] text-muted-foreground">
              {t('keys.keylessHint')}
            </p>
          )}
        </div>
        <div className="space-y-1.5">
          <Label className="text-xs">{t('keys.label')}</Label>
          <div className="flex flex-wrap items-center space-x-3">
            <Input
              value={label}
              onChange={e => setLabel(e.target.value)}
              placeholder={t('keys.customDisplayNameOptional')}
              className="w-[160px]"
            />
            <Button type="submit" size="sm" disabled={pending}>
              {pending
                ? t('keys.adding')
                : severalMode && keyList.length > 1
                  ? t('keys.importSelected', { count: keyList.length })
                  : isKeyless ? t('keys.enable') : t('keys.addKey')}
            </Button>
          </div>
        </div>
      </form>
      {(addKey.isError || addSeveral.isError) && (
        <p className="text-destructive text-xs mt-2">{((addKey.error ?? addSeveral.error) as Error).message}</p>
      )}
    </div>
  )
}
