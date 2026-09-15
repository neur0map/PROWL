import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { PageHeader } from '@/components/page-header'
import type { ApiKey, Platform } from '../../../shared/types'
import { Plus, Download } from 'lucide-react'
import { useI18n } from '@/i18n'
import type { HealthData } from '@/components/keys/shared'
import { QuotaSignalsSection } from '@/components/keys/quota-signals-section'
import { UnifiedKeySection } from '@/components/keys/unified-key-section'
import { ClientProfilesSection } from '@/components/keys/client-profiles-section'
import { ProxySettingsSection } from '@/components/keys/proxy-settings-section'
import { BackupsSection } from '@/components/keys/backups-section'
import { AnthropicSection } from '@/components/keys/anthropic-section'
import { ProviderCatalogue, type DirectoryProvider, type DirectoryResponse } from '@/components/keys/provider-catalogue'
import { ProviderDrawer } from '@/components/keys/provider-drawer'
import { AddKeyDialog } from '@/components/keys/add-key-dialog'
import { ExportKeysDialog } from '@/components/keys/export-keys-dialog'
import { AgentCompatibilitySection } from '@/components/keys/agent-compatibility-section'

type KeysTab = 'providers' | 'quotaSignals' | 'apiKey' | 'anthropic' | 'agents'
const KEYS_TABS: { id: KeysTab; labelKey: string }[] = [
  { id: 'providers', labelKey: 'keys.tabProviders' },
  { id: 'quotaSignals', labelKey: 'keys.tabQuotaSignals' },
  { id: 'apiKey', labelKey: 'keys.tabApiKey' },
  { id: 'anthropic', labelKey: 'keys.tabAnthropic' },
  { id: 'agents', labelKey: 'keys.tabAgents' },
]

export default function KeysPage() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState<KeysTab>('providers')
  const [addOpen, setAddOpen] = useState(false)
  // Provider the Add key dialog opens preselected to, when the add flow was
  // entered from a checklist chip rather than the generic Add key button.
  const [addPlatform, setAddPlatform] = useState<Platform | ''>('')
  const [exportOpen, setExportOpen] = useState(false)
  // The provider the drawer is showing; null keeps it unmounted.
  const [drawerProvider, setDrawerProvider] = useState<DirectoryProvider | null>(null)

  // Set when the add flow was entered for a provider with no wire adapter, so
  // the dialog opens on the pane that can actually reach it.
  const [addCustom, setAddCustom] = useState(false)
  const [searchParams] = useSearchParams()

  // A platform arriving as ?provider= means the operator followed a link from
  // somewhere that hit a missing key — the routing table hides models whose
  // platform has none. Land them on the catalogue with that provider open
  // instead of on a page where they have to find it again.
  const requestedProvider = searchParams.get('provider')
  // Same query key as the catalogue, so react-query serves one request for
  // both and the drawer opens from cache when the page is already warm.
  const { data: directory } = useQuery<DirectoryResponse>({
    queryKey: ['providers-directory'],
    queryFn: () => apiFetch('/api/providers/directory'),
    enabled: requestedProvider !== null,
  })
  useEffect(() => {
    if (!requestedProvider) return
    setTab('providers')
    const match = directory?.providers.find(p => p.id === requestedProvider)
    if (match) setDrawerProvider(match)
  }, [requestedProvider, directory])

  const openAddKey = (platform: Platform | '' = '') => {
    setAddPlatform(platform)
    setAddOpen(true)
  }

  // Kept at page level for the header's "Check all" gate; the catalogue and
  // the drawer run the same query, deduped by react-query.
  const { data: keys = [] } = useQuery<ApiKey[]>({
    queryKey: ['keys'],
    queryFn: () => apiFetch('/api/keys'),
  })

  const { data: healthData } = useQuery<HealthData>({
    queryKey: ['health'],
    queryFn: () => apiFetch('/api/health'),
    refetchInterval: 30000,
  })

  const checkAll = useMutation({
    mutationFn: () => apiFetch('/api/health/check-all', { method: 'POST' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['health'] })
      queryClient.invalidateQueries({ queryKey: ['keys'] })
    },
  })

  return (
    <div>
      <PageHeader
        title={t('keys.pageTitle')}
        description={t('keys.pageDescription')}
        actions={
          <>
            {(tab === 'providers' || tab === 'quotaSignals') && keys.length > 0 && (
              <Button variant="outline" size="sm" onClick={() => checkAll.mutate()} disabled={checkAll.isPending}>
                {checkAll.isPending ? t('keys.checking') : t('keys.checkAll')}
              </Button>
            )}
            {keys.length > 0 && (
              <Button variant="outline" size="sm" onClick={() => setExportOpen(true)}>
                <Download className="size-3.5" />
                {t('keys.export')}
              </Button>
            )}
            {tab === 'providers' && (
              <Button size="sm" onClick={() => openAddKey()}>
                <Plus className="size-3.5" />
                {t('keys.addKey')}
              </Button>
            )}
            <SegmentedControl
              value={tab}
              onValueChange={setTab}
              options={KEYS_TABS.map(tb => ({ value: tb.id, label: t(tb.labelKey) }))}
              ariaLabel={t('keys.pageTitle')}
            />
          </>
        }
      />

      <div className="space-y-8">
        {tab === 'apiKey' && (
          <>
            <UnifiedKeySection />
            <ClientProfilesSection />
            <ProxySettingsSection />
            <BackupsSection />
          </>
        )}

        {tab === 'anthropic' && <AnthropicSection />}
        {tab === 'agents' && <AgentCompatibilitySection />}

        {tab === 'quotaSignals' && (
          <QuotaSignalsSection states={(healthData?.quotaStates ?? []).slice(0, 24)} />
        )}

        {/* One surface: the whole catalogue with the operator's own keys folded
            into each row, and the drawer as the place a key is added. The old
            split showed what you had here and what existed on a separate tab,
            which meant adding a key crossed two pages. */}
        {tab === 'providers' && <ProviderCatalogue onOpenProvider={setDrawerProvider} />}
      </div>

      <ProviderDrawer
        provider={drawerProvider}
        onClose={() => setDrawerProvider(null)}
        onAddKey={platform => {
          setDrawerProvider(null)
          openAddKey(platform as Platform)
        }}
        onAddCustomEndpoint={() => {
          setDrawerProvider(null)
          setAddCustom(true)
          setAddOpen(true)
        }}
      />

      <AddKeyDialog
        open={addOpen}
        onOpenChange={open => {
          setAddOpen(open)
          if (!open) setAddCustom(false)
        }}
        initialPlatform={addPlatform || undefined}
        initialPane={addCustom ? 'custom' : undefined}
      />
      {/* Mounted only while open so the export flow always starts at step one
          and never retains a previously typed password. */}
      {exportOpen && <ExportKeysDialog open={exportOpen} onOpenChange={setExportOpen} />}
    </div>
  )
}
