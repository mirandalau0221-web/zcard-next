import { computed, ref } from 'vue';
import english from './locales/en.json';

export type Locale = 'zh_TW' | 'en';
export const SUPPORTED_LOCALES = [
  { value: 'zh_TW' as const, label: '繁體中文', tag: 'zh-TW' },
  { value: 'en' as const, label: 'English', tag: 'en' },
];
const STORAGE_KEY = 'zcard_locale';
const active = ref<Locale>('zh_TW');
const available = ref<Locale[]>(['zh_TW']);
const siteDefault = ref<Locale>('zh_TW');
let customerChoice: Locale | null = null;
export const locale = computed(() => active.value);
export const enabledLocales = computed(() => available.value);
export const localeTag = computed(() => active.value === 'zh_TW' ? 'zh-TW' : 'en');
const dictionary: Record<string, string> = english;

export function normalizeLocale(value: unknown): Locale | null {
  if (typeof value !== 'string') return null;
  const tag = value.trim().toLowerCase().replace(/_/g, '-');
  if (tag === 'zh' || tag.startsWith('zh-')) return 'zh_TW';
  if (tag === 'en' || /^en-[a-z]{2}$/.test(tag)) return 'en';
  return null;
}
function savedLocale(): Locale | null {
  try { return normalizeLocale(localStorage.getItem(STORAGE_KEY)); } catch { return null; }
}
function updateDocumentLanguage() {
  if (typeof document !== 'undefined') document.documentElement.lang = localeTag.value;
}
/** Only an enabled customer choice overrides the site's default. */
export function applyLocaleConfig(entries: { key: string; value_json: string }[]) {
  const value = (key: string): unknown => {
    try { return JSON.parse(entries.find(entry => entry.key === key)?.value_json || 'null'); }
    catch { return null; }
  };
  const configured = value('i18n.enabled_locales');
  const enabled = Array.isArray(configured)
    ? [...new Set(configured.map(normalizeLocale).filter((item): item is Locale => item !== null))]
    : [];
  available.value = enabled.length ? enabled : ['zh_TW'];
  const requestedDefault = normalizeLocale(value('i18n.default_locale'));
  siteDefault.value = requestedDefault && available.value.includes(requestedDefault)
    ? requestedDefault : available.value[0];
  const saved = typeof window === 'undefined' ? null : customerChoice || savedLocale();
  customerChoice = saved && available.value.includes(saved) ? saved : null;
  active.value = saved && available.value.includes(saved) ? saved : siteDefault.value;
  // Remove a disabled preference so re-enabling it cannot resurrect a stale choice.
  if (saved && !available.value.includes(saved)) {
    try { localStorage.removeItem(STORAGE_KEY); } catch { /* Storage is optional. */ }
  }
  updateDocumentLanguage();
}
export function selectLocale(next: Locale) {
  if (!available.value.includes(next)) return false;
  customerChoice = next;
  active.value = next;
  try { localStorage.setItem(STORAGE_KEY, next); } catch { /* Storage is optional. */ }
  updateDocumentLanguage();
  return true;
}
export function languageHeaders(): Record<string, string> {
  return { 'Accept-Language': localeTag.value };
}
/** The source phrase is the Chinese fallback and the stable dictionary key. */
export function t(source: string, parameters: readonly unknown[] = []): string {
  const key = source.trim().replace(/\s+/g, ' ');
  const translated = active.value === 'en' ? dictionary[key] ?? key : key;
  return translated.replace(/\{(\d+)\}/g, (token, index: string) =>
    Number(index) < parameters.length ? String(parameters[Number(index)] ?? '') : token);
}
export function formatLocaleDate(value: Date | number, options?: Intl.DateTimeFormatOptions): string {
  const date = value instanceof Date ? value : new Date(value);
  return date.toLocaleDateString(localeTag.value, options);
}
/** Known business errors use stable reasons; unknown errors never show raw server text. */
export function apiError(payload: unknown, status?: number): string {
  const problem = payload && typeof payload === 'object' ? payload as Record<string, unknown> : {};
  const reason = typeof problem.reason === 'string' ? problem.reason : '';
  const message = typeof problem.message === 'string' ? problem.message : '';
  if (active.value === 'zh_TW' && message) return message;
  // The API localizes known reasons. Old servers may still return Chinese messages.
  if (message && active.value === 'en' && !/[\p{Script=Han}]/u.test(message)) return message;
  if (message && dictionary[message.trim().replace(/\s+/g, ' ')]) return t(message);
  if (/CURRENCY_MISMATCH/.test(reason)) return t('賬户幣種與基礎貨幣不一致，請聯繫管理員遷移');
  if (/STOCK|SOLD_OUT/.test(reason)) return t('庫存不足，請調整數量後重試');
  if (/BALANCE|FUNDS|POINTS_INSUFFICIENT/.test(reason)) return t('餘額或積分不足，請檢查後重試');
  if (/CAPTCHA|VERIFICATION|VERIFY_CODE/.test(reason)) return t('驗證碼錯誤或已過期，請重新獲取');
  if (/LOGIN_FAILED|PASSWORD_INVALID|INVALID_CREDENTIALS/.test(reason)) return t('賬號或密碼錯誤');
  if (status === 401) return t('登錄已過期，請重新登錄');
  if (status === 403) return t('無法執行此操作，請檢查賬户權限');
  if (status === 404) return t('請求的內容不存在或已下架');
  if (status === 429) return t('請求過於頻繁，請稍後重試');
  if (status && status >= 500) return t('服務暫時不可用，請稍後重試');
  return t('操作失敗，請檢查輸入後重試');
}

let messagePatterns: { source: string; expression: RegExp; indexes: number[] }[] | undefined;
/** Re-localize cached UI feedback without resetting forms or replaying requests. */
export function uiText(message: string): string {
  if (!message) return '';
  const normalized = message.trim().replace(/\s+/g, ' ');
  if (dictionary[normalized]) return t(normalized);
  const exact = Object.entries(dictionary).find(([, value]) => value === normalized);
  if (exact) return t(exact[0]);
  if (!messagePatterns) {
    const escape = (part: string) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    messagePatterns = Object.entries(dictionary).flatMap(([source, english]) =>
      /\{\d+\}/.test(source) ? [source, english].map(phrase => {
        const indexes: number[] = [];
        let cursor = 0;
        let pattern = '^';
        for (const match of phrase.matchAll(/\{(\d+)\}/g)) {
          pattern += escape(phrase.slice(cursor, match.index)) + '(.+?)';
          indexes.push(Number(match[1]));
          cursor = match.index! + match[0].length;
        }
        pattern += escape(phrase.slice(cursor)) + '$';
        return { source, expression: new RegExp(pattern), indexes };
      }) : []);
  }
  for (const pattern of messagePatterns) {
    const match = pattern.expression.exec(normalized);
    if (!match) continue;
    const parameters: string[] = [];
    pattern.indexes.forEach((index, position) => { parameters[index] = match[position + 1]; });
    return t(pattern.source, parameters);
  }
  return message;
}
