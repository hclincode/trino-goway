import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { en_US } from './en_US';

const LANG_KEY = 'lang';
const DEFAULT_LANG = 'en_US';

/** Detection order: localStorage 'lang' -> navigator -> en_US. */
function detectLanguage(): string {
  try {
    const stored = localStorage.getItem(LANG_KEY);
    if (stored) return stored;
  } catch {
    // localStorage unavailable; fall through
  }
  try {
    const nav = navigator.language?.toLowerCase();
    // Only en_US is implemented today; keep the hook for future locales.
    if (nav?.startsWith('en')) return 'en_US';
  } catch {
    // navigator unavailable; fall through
  }
  return DEFAULT_LANG;
}

void i18n.use(initReactI18next).init({
  resources: {
    en_US: { translation: en_US },
  },
  lng: detectLanguage(),
  fallbackLng: DEFAULT_LANG,
  interpolation: { escapeValue: false },
});

export default i18n;
