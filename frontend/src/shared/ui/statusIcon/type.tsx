export enum StatusEnum {
  Unreachable = 'unknown',
  Online = 'online',
  Offline = 'offline',
  Standby = 'standby',
}

// TODO: Replace titles with localization keys when i18n support is added
export const nodeStatusDescriptions: Record<StatusEnum, { title: string }> = {
  [StatusEnum.Unreachable]: { title: 'Unreachable' },
  [StatusEnum.Online]: { title: 'Online' },
  [StatusEnum.Offline]: { title: 'Offline' },
  [StatusEnum.Standby]: { title: 'Standby' },
}

export type StatusIconSize = 'xsmall' | 'small' | 'medium' | 'large'
export type StatusIconColor = 'gray' | 'green' | 'red' | 'orange'
export type StatusIconVariant = 'solid' | 'outline'
