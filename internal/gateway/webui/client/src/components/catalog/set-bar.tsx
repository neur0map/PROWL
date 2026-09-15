import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Plus } from 'lucide-react'
import { useI18n } from '@/i18n'
import { CopyButton } from '@/components/copy-button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  activateProfile,
  createSetFromPreset,
  fetchActiveProfile,
  fetchPresets,
  fetchProfiles,
  type PresetGroup,
  type ProfilePreset,
} from '@/lib/catalog'

// One dense row above the results: which set the router is using, a switcher, a
// copyable auto:<name> id, and a menu that mints a new set from a preset. Task
// presets sort before catalogue ones so "for a kind of work" leads.
const CHIP = 'text-[10px] rounded-full px-1.5 py-0.5 bg-muted text-muted-foreground'
const PRESET_GROUP_ORDER: PresetGroup[] = ['task', 'catalogue']

export function SetBar() {
  const { t } = useI18n()
  const queryClient = useQueryClient()

  const { data: profiles } = useQuery({ queryKey: ['profiles'], queryFn: fetchProfiles })
  const { data: active } = useQuery({ queryKey: ['profiles', 'active'], queryFn: fetchActiveProfile })
  const { data: presetData } = useQuery({ queryKey: ['profiles', 'presets'], queryFn: fetchPresets })

  // The active list is what "in gateway" is measured against, so a switch has to
  // re-read the catalogue as well as the list queries.
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['profiles'] })
    queryClient.invalidateQueries({ queryKey: ['fallback'] })
    queryClient.invalidateQueries({ queryKey: ['catalog'] })
  }

  const activate = useMutation({
    mutationFn: (profileId: number) => activateProfile(profileId),
    onSuccess: invalidate,
  })
  const fromPreset = useMutation({
    mutationFn: (preset: ProfilePreset) => createSetFromPreset(preset.id, preset.name, true),
    onSuccess: invalidate,
  })

  const activeId = active?.activeProfileId ?? null
  const activeName = (activeId != null && profiles?.find(p => p.id === activeId)?.name) || ''

  if (!profiles) return null

  const presets = presetData?.presets ?? []
  const grouped = PRESET_GROUP_ORDER
    .map(group => ({ group, items: presets.filter(p => p.group === group) }))
    .filter(g => g.items.length > 0)

  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-xl border bg-card px-3 py-2 text-xs">
      <span className="font-medium text-muted-foreground">{t('catalog.activeSet')}</span>

      {profiles.length > 0 && (
        <Select
          value={activeId != null ? String(activeId) : undefined}
          onValueChange={v => { if (v) activate.mutate(Number(v)) }}
        >
          <SelectTrigger size="sm" aria-label={t('catalog.switchSet')} disabled={activate.isPending}>
            <SelectValue>{(v: string) => (v ? profiles?.find(p => p.id === Number(v))?.name ?? activeName : activeName)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {profiles.map(p => (
              <SelectItem key={p.id} value={String(p.id)}>{p.name}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}

      {activeName && (
        <span className="inline-flex items-center gap-1">
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
            auto:{activeName.toLowerCase()}
          </code>
          <CopyButton text={`auto:${activeName.toLowerCase()}`} className="size-6" label={t('common.copy')} />
        </span>
      )}

      {grouped.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger
            disabled={fromPreset.isPending}
            className="inline-flex items-center gap-1 rounded-lg border px-2 py-1 text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40"
          >
            <Plus className="size-3" />
            {t('catalog.newSetFromPreset')}
            <ChevronDown className="size-3" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-80">
            {grouped.map(({ group, items }) => (
              <DropdownMenuGroup key={group}>
                <DropdownMenuLabel>{t(`chains.presetGroup_${group}`)}</DropdownMenuLabel>
                {items.map(preset => (
                  <DropdownMenuItem
                    key={preset.id}
                    disabled={preset.models === 0 || fromPreset.isPending}
                    onClick={() => fromPreset.mutate(preset)}
                    className="flex-col items-start gap-1 py-1.5"
                  >
                    <span className="flex w-full items-center gap-2">
                      <span className="flex-1 truncate font-medium">{preset.name}</span>
                      <span className="tabular-nums text-muted-foreground">{preset.models}</span>
                    </span>
                    {preset.requirements.length > 0 && (
                      <span className="flex flex-wrap items-center gap-1">
                        <span className="text-[10px] text-muted-foreground">{t('chains.presetRequires')}</span>
                        {preset.requirements.map(req => (
                          <span key={req} className={CHIP}>{req}</span>
                        ))}
                      </span>
                    )}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuGroup>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      <span className="ms-auto text-[11px] text-muted-foreground">{t('catalog.setsHint')}</span>
    </div>
  )
}
