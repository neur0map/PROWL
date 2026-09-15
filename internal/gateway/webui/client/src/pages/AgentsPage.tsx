import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { useI18n } from '@/i18n'
import { PageHeader } from '@/components/page-header'
import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'

interface KeyResponse {
  apiKey: string
}

// Mask everything after the recognisable 13-char prefix, the same body the
// Keys page hides, so the two surfaces reveal a credential identically.
function masked(value: string): string {
  if (!value) return '…'
  return value.slice(0, 13) + '\u2022'.repeat(32)
}

// A titled code block with a copy button. `shown` may be masked; `copy` is
// always the real text, so a masked credential still copies whole and one the
// user has not revealed never appears on screen.
function CodeBlock({
  label,
  hint,
  shown,
  copy,
}: {
  label: string
  hint?: string
  shown: string
  copy: string
}) {
  const { t } = useI18n()
  return (
    <div>
      <p className="text-xs font-medium">{label}</p>
      {hint && <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>}
      <div className="mt-2 flex items-start gap-2 rounded-xl bg-muted/50 px-3 py-2">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all font-mono text-[11px]">
          {shown}
        </code>
        <CopyButton text={copy} className="size-7 shrink-0" label={t('common.copy')} />
      </div>
    </div>
  )
}

export default function AgentsPage() {
  const { t } = useI18n()
  const [reveal, setReveal] = useState(false)
  const { data: keyData } = useQuery<KeyResponse>({
    queryKey: ['api-key'],
    queryFn: () => apiFetch('/api/settings/api-key'),
  })

  const origin = import.meta.env.DEV
    ? `http://${window.location.hostname}:${__SERVER_PORT__}`
    : window.location.origin
  const baseUrl = `${origin}/v1`

  const realKey = keyData?.apiKey ?? ''
  // A copyable placeholder before the key has loaded, so a snippet copied early
  // still shows where the key goes rather than an empty assignment.
  const keyForCopy = realKey || 'prowl-gate-...'
  const keyShown = realKey ? (reveal ? realKey : masked(realKey)) : '\u2026'
  const keyInline = reveal ? keyForCopy : masked(realKey)

  const env = (key: string) => `OPENAI_BASE_URL=${baseUrl}\nOPENAI_API_KEY=${key}`
  const curl = (key: string) =>
    `curl ${baseUrl}/chat/completions \\\n` +
    `  -H "Authorization: Bearer ${key}" \\\n` +
    `  -H "Content-Type: application/json" \\\n` +
    `  -d '{"model":"auto","messages":[{"role":"user","content":"Hello"}]}'`

  const startCmd = 'prowl gateway'
  const useCmd = 'prowl -m auto "refactor this function"'

  return (
    <div>
      <PageHeader title={t('agents.title')} description={t('agents.description')} />

      <section className="mb-6 rounded-3xl border bg-card p-5">
        <h2 className="text-sm font-medium">{t('agents.connectionTitle')}</h2>
        <p className="mt-0.5 max-w-prose text-xs text-muted-foreground">{t('agents.connectionHint')}</p>
        <div className="mt-4 space-y-3">
          <div className="flex items-center gap-2">
            <span className="w-24 shrink-0 text-xs text-muted-foreground">{t('agents.baseUrl')}</span>
            <code className="min-w-0 flex-1 truncate rounded-lg bg-muted px-3 py-2 font-mono text-xs select-all">{baseUrl}</code>
            <CopyButton text={baseUrl} className="size-7 shrink-0" label={t('common.copy')} />
          </div>
          <div className="flex items-center gap-2">
            <span className="w-24 shrink-0 text-xs text-muted-foreground">{t('agents.unifiedKey')}</span>
            <code className="min-w-0 flex-1 truncate rounded-lg bg-muted px-3 py-2 font-mono text-xs tabular-nums select-all">{keyShown}</code>
            <Button variant="outline" size="sm" className="shrink-0" onClick={() => setReveal(value => !value)}>
              {reveal ? t('common.hide') : t('common.show')}
            </Button>
            <CopyButton text={realKey} className="size-7 shrink-0" label={t('common.copy')} />
          </div>
        </div>
      </section>

      <section className="mb-6 rounded-3xl border bg-card p-5">
        <h2 className="text-sm font-medium">{t('agents.openaiTitle')}</h2>
        <p className="mt-0.5 max-w-prose text-xs text-muted-foreground">{t('agents.openaiHint')}</p>
        <div className="mt-4 space-y-4">
          <CodeBlock label={t('agents.envLabel')} shown={env(keyInline)} copy={env(keyForCopy)} />
          <CodeBlock label={t('agents.curlLabel')} hint={t('agents.curlHint')} shown={curl(keyInline)} copy={curl(keyForCopy)} />
        </div>
      </section>

      <section className="rounded-3xl border bg-card p-5">
        <h2 className="text-sm font-medium">{t('agents.prowlTitle')}</h2>
        <p className="mt-0.5 max-w-prose text-xs text-muted-foreground">{t('agents.prowlHint')}</p>
        <div className="mt-4 space-y-4">
          <CodeBlock label={t('agents.prowlStartLabel')} hint={t('agents.prowlStartHint')} shown={startCmd} copy={startCmd} />
          <CodeBlock label={t('agents.prowlUseLabel')} hint={t('agents.prowlUseHint')} shown={useCmd} copy={useCmd} />
        </div>
      </section>
    </div>
  )
}
