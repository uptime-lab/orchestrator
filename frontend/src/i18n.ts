import i18n, { CustomTypeOptions } from 'i18next'
import { initReactI18next } from 'react-i18next'

import enTranslations from './locales/en.json'

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: enTranslations },
  },
  lng: 'en', // todo: add language detector once we have the languages
  fallbackLng: 'en',
  // An empty translation is deliberate here (a header with no text), and
  // i18next 27 would otherwise render the key in its place.
  returnEmptyString: true,
  interpolation: {
    escapeValue: false,
  },
})

type Leaves<T> = T extends object
  ? {
      [K in keyof T]: `${K & string}${Leaves<T[K]> extends never ? '' : `.${Leaves<T[K]>}`}`
    }[keyof T]
  : never

export type LocalizedLabelKey = Leaves<
  CustomTypeOptions['resources']['translation']
>

export default i18n
