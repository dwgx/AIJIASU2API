import type { TranslationKey } from './i18n'
import type { PanelSettings } from './types'

/**
 * Settings is a map, not one long form: eight sections, each deep-linkable
 * with `#<id>`. This registry is the single source for the rail, the section
 * order, the per-section dirty dots and the reset scope.
 */
export type SettingsSectionId =
  | 'general'
  | 'appearance'
  | 'language'
  | 'connection'
  | 'leases'
  | 'updates'
  | 'integrations'
  | 'audit'

export type SettingsGroupId = 'basics' | 'runtime' | 'system'

/** Editable fields, i.e. everything the settings draft can differ on. */
export type SettingsFieldKey = keyof PanelSettings

export interface SettingsSectionDef {
  id: SettingsSectionId
  group: SettingsGroupId
  labelKey: TranslationKey
  descriptionKey: TranslationKey
  /** Fields reset by "Reset section" and counted by the dirty dot. */
  fields: readonly SettingsFieldKey[]
}

export interface SettingsGroupDef {
  id: SettingsGroupId
  labelKey: TranslationKey
}

export const SETTINGS_FIELD_KEYS: readonly SettingsFieldKey[] = [
  'transport',
  'healthIntervalSec',
  'leaseTtl',
  'subscriptionRefreshSec',
  'theme',
]

export const SETTINGS_GROUPS: readonly SettingsGroupDef[] = [
  { id: 'basics', labelKey: 'settings.group.basics' },
  { id: 'runtime', labelKey: 'settings.group.runtime' },
  { id: 'system', labelKey: 'settings.group.system' },
]

export const SETTINGS_SECTIONS: readonly SettingsSectionDef[] = [
  {
    id: 'general',
    group: 'basics',
    labelKey: 'settings.nav.general',
    descriptionKey: 'settings.section.general.desc',
    fields: [],
  },
  {
    id: 'appearance',
    group: 'basics',
    labelKey: 'settings.nav.appearance',
    descriptionKey: 'settings.section.appearance.desc',
    fields: ['theme'],
  },
  {
    id: 'language',
    group: 'basics',
    labelKey: 'settings.nav.language',
    descriptionKey: 'settings.section.language.desc',
    fields: [],
  },
  {
    id: 'connection',
    group: 'runtime',
    labelKey: 'settings.nav.connection',
    descriptionKey: 'settings.section.connection.desc',
    fields: ['transport', 'healthIntervalSec'],
  },
  {
    id: 'leases',
    group: 'runtime',
    labelKey: 'settings.nav.leases',
    descriptionKey: 'settings.section.leases.desc',
    fields: ['leaseTtl', 'subscriptionRefreshSec'],
  },
  {
    id: 'updates',
    group: 'runtime',
    labelKey: 'settings.nav.updates',
    descriptionKey: 'settings.section.updates.desc',
    fields: [],
  },
  {
    id: 'integrations',
    group: 'system',
    labelKey: 'settings.nav.integrations',
    descriptionKey: 'settings.section.integrations.desc',
    fields: [],
  },
  {
    id: 'audit',
    group: 'system',
    labelKey: 'settings.nav.audit',
    descriptionKey: 'settings.section.audit.desc',
    fields: [],
  },
]

function isSettingsSectionId(value: string): value is SettingsSectionId {
  return SETTINGS_SECTIONS.some((section) => section.id === value)
}

/** `#connection` → `connection`; anything unknown falls back to General. */
export function settingsSectionFromHash(hash: string): SettingsSectionId {
  const id = hash.replace(/^#/, '')
  return isSettingsSectionId(id) ? id : 'general'
}

export function sectionForField(field: SettingsFieldKey): SettingsSectionId {
  const section = SETTINGS_SECTIONS.find((candidate) => candidate.fields.includes(field))
  return section?.id ?? 'general'
}

export function sectionsForGroup(group: SettingsGroupId): readonly SettingsSectionDef[] {
  return SETTINGS_SECTIONS.filter((section) => section.group === group)
}
