import 'i18next'

import en from '@/locales/en.json'

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation'
    ns: 'translation'
    returnEmptyString: true
    resources: {
      translation: typeof en
    }
  }
}
