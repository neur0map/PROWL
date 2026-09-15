import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { useI18n } from '@/i18n'
import { apiFetch } from '@/lib/api'
import type { TokenUsageData } from '@/lib/routing'
import { PageHeader } from '@/components/page-header'
import { GettingStarted } from '@/components/getting-started'
import { ChainManager } from '@/components/chain-manager'
import { PenaltyInspector } from '@/components/penalty-inspector'
import { TokenUsageBar } from '@/components/token-usage-bar'
import { StrategyCard } from '@/components/routing/strategy-card'
import { ChainOrder } from '@/components/routing/chain-order'
import { buttonVariants } from '@/components/ui/button'

function Section({ label, blurb, children }: { label: string; blurb?: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</h2>
        {blurb && <p className="mt-1 text-xs text-muted-foreground">{blurb}</p>}
      </div>
      {children}
    </section>
  )
}

// The gateway's routing setup, one job per section: pick a strategy, keep named
// lists, order what is turned on, cap spend, and read live router pressure.
// Choosing WHICH models to use lives on the browse page (/models).
export default function RoutingPage() {
  const { t } = useI18n()

  const { data: tokenUsage } = useQuery<TokenUsageData>({
    queryKey: ['fallback', 'token-usage'],
    queryFn: () => apiFetch('/api/fallback/token-usage'),
  })

  return (
    <div>
      <PageHeader
        title={t('routing.title')}
        description={t('routing.subtitle')}
        actions={
          <Link to="/models" className={buttonVariants({ variant: 'outline', size: 'sm' })}>
            {t('routing.openCatalog')}
          </Link>
        }
      />

      <div className="space-y-8">
        {/* First-run checklist: hides itself once the install has keys + a request. */}
        <GettingStarted />

        <Section label={t('routing.strategySection')}>
          <StrategyCard />
        </Section>

        <Section label={t('routing.listsSection')}>
          <ChainManager />
        </Section>

        <Section label={t('routing.orderSection')} blurb={t('routing.orderBlurb')}>
          <ChainOrder />
        </Section>

        {tokenUsage && tokenUsage.totalBudget > 0 && (
          <Section label={t('routing.limitsSection')}>
            <TokenUsageBar data={tokenUsage} />
          </Section>
        )}

        <Section label={t('routing.pressureSection')}>
          <PenaltyInspector />
        </Section>
      </div>
    </div>
  )
}
