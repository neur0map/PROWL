import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useI18n } from '@/i18n'
import { toast } from '@/lib/toast'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogDescription, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { createSetFromSelection, routableRowIds, type CatalogModel } from '@/lib/catalog'

// Save the current selection as a named set. Only rows a key can serve go in — a
// set exists to route, so a keyless row would seat an unroutable candidate. A
// duplicate name (409) is shown next to the field so the typed name survives.
export function SaveSetDialog({ models, onClose, onSaved }: {
  models: CatalogModel[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [activate, setActivate] = useState(true)
  const [error, setError] = useState('')

  const modelDbIds = useMemo(() => models.flatMap(routableRowIds), [models])

  const save = useMutation({
    meta: { silenceToast: true },
    mutationFn: () => createSetFromSelection(name.trim(), modelDbIds, activate),
    onSuccess: result => {
      toast.success(t('catalog.setSaved', { name: result.name, count: result.models }))
      queryClient.invalidateQueries({ queryKey: ['catalog'] })
      queryClient.invalidateQueries({ queryKey: ['profiles'] })
      queryClient.invalidateQueries({ queryKey: ['fallback'] })
      onSaved()
    },
    onError: err => setError(err instanceof Error ? err.message : String(err)),
  })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim() || modelDbIds.length === 0 || save.isPending) return
    setError('')
    save.mutate()
  }

  return (
    <Dialog open onOpenChange={open => { if (!open) onClose() }}>
      <DialogPopup maxWidth="max-w-md">
        <DialogTitle>{t('catalog.saveSetTitle')}</DialogTitle>
        <DialogDescription className="mt-1">{t('catalog.saveSetBlurb')}</DialogDescription>

        <form className="mt-4 space-y-3" onSubmit={submit}>
          <div className="space-y-1">
            <Input
              autoFocus
              value={name}
              onChange={e => { setName(e.target.value); setError('') }}
              placeholder={t('catalog.saveSetPlaceholder')}
              aria-label={t('catalog.saveSetPlaceholder')}
              aria-invalid={error ? true : undefined}
            />
            {error && <p className="text-xs text-rose-600 dark:text-rose-400">{error}</p>}
          </div>

          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={activate}
              onChange={e => setActivate(e.target.checked)}
              className="size-3.5 accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            />
            <span>{t('catalog.saveSetActivate')}</span>
          </label>

          <div className="flex items-center justify-between gap-2">
            <span className="text-xs tabular-nums text-muted-foreground">{t('catalog.selected', { count: models.length })}</span>
            <div className="flex gap-2">
              <Button type="button" variant="outline" size="sm" onClick={onClose}>{t('common.cancel')}</Button>
              <Button type="submit" size="sm" disabled={!name.trim() || modelDbIds.length === 0 || save.isPending}>
                {t('catalog.saveSetCta')}
              </Button>
            </div>
          </div>
        </form>
      </DialogPopup>
    </Dialog>
  )
}
