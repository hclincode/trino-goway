import 'i18next';
import type { LocaleResource } from './en_US';

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation';
    resources: {
      translation: LocaleResource;
    };
  }
}
