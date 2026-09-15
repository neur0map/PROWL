import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'

import { apiFetch } from '@/lib/api'
import { PageHeader } from '@/components/page-header'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { QuotaSignalsSection } from '@/components/keys/quota-signals-section'
import type { HealthData } from '@/components/keys/shared'
import { useI18n } from '@/i18n'
import AnalyticsPage from './AnalyticsPage'
import LogsPage from './LogsPage'

const TABS = ['overview', 'logs', 'quota'] as const
type Tab = (typeof TABS)[number]

// What the router did, in one place. Charts, the request log and the quota a
// provider reports were three destinations for one question.
export default function ActivityPage() {
  const { t } = useI18n()
  const [params, setParams] = useSearchParams()

  const requested = params.get('tab')
  const tab: Tab = TABS.includes(requested as Tab) ? (requested as Tab) : 'overview'

  const { data: health } = useQuery<HealthData>({
    queryKey: ['health'],
    queryFn: () => apiFetch('/api/health'),
    refetchInterval: 30000,
    enabled: tab === 'quota',
  })

  return (
    <div>
      <PageHeader
        title={t('activity.title')}
        description={t('activity.subtitle')}
        actions={
          <SegmentedControl
            value={tab}
            onValueChange={next => setParams(prev => {
              const merged = new URLSearchParams(prev)
              merged.set('tab', next)
              return merged
            }, { replace: true })}
            options={[
              { value: 'overview', label: t('activity.tab_overview') },
              { value: 'logs', label: t('activity.tab_logs') },
              { value: 'quota', label: t('activity.tab_quota') },
            ]}
            ariaLabel={t('activity.title')}
          />
        }
      />

      {tab === 'overview' && <AnalyticsPage embedded />}
      {tab === 'logs' && <LogsPage embedded />}
      {tab === 'quota' && <QuotaSignalsSection states={health?.quotaStates ?? []} />}
    </div>
  )
}
