import { shallowRef } from 'vue';
import { apiError, languageHeaders } from '@/i18n';
import { t as $t } from '@/i18n';
import { displayPrecision, formatCents, fromCents, toCents } from '../../../packages/money/index';
import { loadPublicConfig } from '../config';
import { readJSON } from './read';
// 前台 API 客戶端：fetch 封裝，金額一律「分」int64。
// 認證（）：user realm JWT 存 localStorage，請求自動帶 Bearer；
// 401 且本地有 token → 判定過期，清 token 跳登錄（遊客端點 401 不誤傷）。

// SSG 構建（vite-ssg）時服務端無同源 API：經 VITE_SSG_API 指向構建機可達的 API
// （如 http://127.0.0.1:8000）；客戶端恆用同源相對路徑。
const BASE = import.meta.env.SSR
  ? `${import.meta.env.VITE_SSG_API || 'http://127.0.0.1:8000'}/api/v1/storefront`
  : '/api/v1/storefront';
const TOKEN_KEY = 'zcard_token';
const CURRENCY_KEY = 'zcard_currency';

export function getToken(): string | null {
  if (import.meta.env.SSR) return null;
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null; // 存儲不可用（隱私模式等）：視為遊客
  }
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

interface ApiResult<T> {
  status?: number;
  data: T | null;
  error: string | null;
}

async function request<T>(method: string, path: string, body?: unknown, params?: Record<string, string | number | boolean | undefined>, silent = false, headers: Record<string,string> = {}): Promise<ApiResult<T>> {
  if (method === 'GET' && path === '/config') {
    try { return { data: await loadPublicConfig(true) as T, error: null }; }
    catch { return { data: null, get error() { return $t("店鋪配置加載失敗，請稍後重試"); } }; }
  }
  let url = `${BASE}${path}`;
  if (params) {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== '') q.set(k, String(v));
    }
    const qs = q.toString();
    if (qs) url += `?${qs}`;
  }
  // Only these idempotent reads retry; retain member pricing through Authorization.
  const publicRead = method === 'GET' && ['/products', '/categories', '/banners', '/posts'].includes(path);
  const token = getToken();
  if (publicRead) {
    try { return { data: await readJSON<T>(url, { headers: { ...languageHeaders(), ...(token ? { Authorization: `Bearer ${token}` } : {}) } }), error: null }; }
    catch { return { data: null, get error() { return $t("加載失敗，請檢查網絡後重試"); } }; }
  }
  try {
    const res = await fetch(url, {
      method,
      headers: {
        ...languageHeaders(),
        ...(body ? { 'Content-Type': 'application/json' } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}), ...headers
      },
      body: body ? JSON.stringify(body) : undefined
    });
    const text = await res.text();
    let json: any = null;
    try {
      json = text ? JSON.parse(text) : null;
    } catch {
      json = text;
    }
    if (!res.ok) {
      // token 過期/失效：清態跳登錄（帶回跳）；silent 模式（購物車等遊客可降級
      // 端點）只清 token 不跳轉——由調用方降級遊客本地購物車；無 token 的 401 不動
      if (res.status === 401 && token) {
        clearToken();
        if (!silent) {
          const redirect = encodeURIComponent(location.pathname + location.search);
          location.href = `/login?redirect=${redirect}`;
        }
      }
      return { data: null, status: res.status, error: apiError(json, res.status) };
    }
    return { data: json as T, error: null };
  } catch (e: any) {
    return { data: null, error: $t('加載失敗，請檢查網絡後重試') };
  }
}

export const api = {
  get: <T>(path: string, params?: Record<string, string | number | boolean | undefined>) => request<T>('GET', path, undefined, params),
  post: <T>(path: string, body: unknown, headers?: Record<string,string>) => request<T>('POST', path, body, undefined, false, headers),
  delete_: <T>(path: string) => request<T>('DELETE', path),
  // silent 變體：401 只清 token 不跳登錄（遊客可降級端點在調用方處理）
  getSilent: <T>(path: string, params?: Record<string, string | number | boolean | undefined>, headers?: Record<string,string>) => request<T>('GET', path, undefined, params, true, headers),
  postSilent: <T>(path: string, body: unknown) => request<T>('POST', path, body, undefined, true),
  deleteSilent: <T>(path: string) => request<T>('DELETE', path, undefined, undefined, true)
};

// ── 金額工具（鐵律 15：API 一律 int64「分」，顯示 /100 為元、提交 *100 為分）──
// 符號與小數位取後台默認貨幣（i18n.base_currency → /storefront/currencies）；
// 未加載完成回退 ¥/2。顯示與輸入獨立：顯示可補位，提交按十進制精確換算並拒絕不足一分。
// 全站金額顯示/提交必須經本組函數，禁止內聯 `/ 100` 或硬編碼符號（架構測試守護）。

export interface CurrencyMeta {
  symbol: string;
  position: string; // prefix | suffix
  precision: number; // 小數位（CNY=2、JPY=0）
  code: string; // 幣種代碼（CNY/USD…；切換器展示與本地存儲鍵）
  rate: string; // 換算率（decimal 字符串：1 基準貨幣 = rate 該幣；基準幣 = "1"）
}

const DEFAULT_META: CurrencyMeta = { symbol: '¥', position: 'prefix', precision: 2, code: 'CNY', rate: '1' };

let currencyMeta: CurrencyMeta = { ...DEFAULT_META };
const baseCurrencyMeta = shallowRef<CurrencyMeta>({ ...DEFAULT_META });
export function getBaseCurrency(): CurrencyMeta { return { ...baseCurrencyMeta.value }; }

export function setCurrency(meta: Partial<CurrencyMeta>) {
  currencyMeta = { ...DEFAULT_META, ...meta };
  currencyMeta.precision = displayPrecision(currencyMeta.precision);
}

export function getCurrency(): CurrencyMeta {
  return { ...currencyMeta };
}

export function formatBaseMoney(cents: number): string {
  const meta = getBaseCurrency();
  const amount = formatCents(cents, meta.precision);
  return meta.position === 'suffix' ? `${amount}${meta.symbol}` : `${meta.symbol}${amount}`;
}

export function formatPaymentAmount(quote: { total_cents: number; charged_currency?: string; charged_units?: number; charged_precision?: number }): string {
  if (!quote.charged_currency || quote.charged_currency === getBaseCurrency().code) return formatBaseMoney(quote.total_cents);
  const units = String(quote.charged_units ?? 0);
  const precision = Number(quote.charged_precision ?? 0);
  if (!/^\d+$/.test(units) || !Number.isInteger(precision) || precision < 0 || precision > 8) return formatBaseMoney(quote.total_cents);
  const padded = units.padStart(precision + 1, '0');
  const amount = precision ? `${padded.slice(0, -precision)}.${padded.slice(-precision)}` : padded;
  return `${quote.charged_currency} ${amount}`;
}

// fenToYuan 分 → 元字符串（不含符號；純整數運算，禁止浮點參與）。
// undefined/NaN 兜底 0（proto3 零值字段省略 → undefined 傳入時顯示 0.00 而非 NaN）。
export function fenToYuan(cents: number): string {
  return formatCents(cents, currencyMeta.precision);
}

export function yuanToFen(yuan: number): number {
  return toCents(yuan ?? 0);
}

export function centsToYuan(cents: number): number {
  return fromCents(cents);
}

// Display conversion is independent of base-currency form values.
function displayAmount(cents: number): string {
  return formatCents(cents, currencyMeta.precision, currencyMeta.rate);
}

// formatMoney 帶符號格式化（顯示唯一入口；符號位置感知）。
export function formatMoney(cents: number): string {
  const v = displayAmount(cents);
  return currencyMeta.position === 'suffix' ? `${v}${currencyMeta.symbol}` : `${currencyMeta.symbol}${v}`;
}

// formatSignedMoney 有符號金額（流水：正數帶 +，負數自帶 -）。
export function formatSignedMoney(cents: number): string {
  const v = displayAmount(cents);
  const body = cents > 0 ? `+${v}` : v;
  return currencyMeta.position === 'suffix' ? `${body}${currencyMeta.symbol}` : `${currencyMeta.symbol}${body}`;
}

// initCurrency 啓動加載貨幣（公開配置 i18n.base_currency + i18n.display_currency + 啓用貨幣表；
// 用戶本地選擇 zcard_currency 優先，其次站點顯示貨幣，再基礎貨幣——多幣種展示換算，結算恆基準幣；
// 失敗回退 ¥）。選擇集 listCurrencies() 供頂部切換器渲染；selectCurrency 切換後刷新生效。
let initPromise: Promise<void> | null = null;
let currencyList: (CurrencyMeta & { code: string })[] = [];

export function listCurrencies(): CurrencyMeta[] {
  return currencyList;
}

export function selectCurrency(code: string) {
  if (!code) return;
  localStorage.setItem(CURRENCY_KEY, code);
  if (code !== currencyMeta.code) window.location.reload(); // 低頻操作：整頁刷新保證全站一致
}

export function initCurrency(): Promise<void> {
  if (!initPromise) {
    initPromise = (async () => {
      try {
        const [cfgRes, curRes] = await Promise.all([
          api.get<any>('/config'),
          api.get<{ currencies: CurrencyMeta[] }>('/currencies')
        ]);
        let baseCode = 'CNY';
        let displayCode = '';
        const entries: { key: string; value_json: string }[] = cfgRes.data?.entries || [];
        for (const entry of entries) {
          if (entry.key !== 'i18n.base_currency' && entry.key !== 'i18n.display_currency') continue;
          try {
            const v = JSON.parse(entry.value_json ?? '');
            if (typeof v === 'string' && v) {
              if (entry.key === 'i18n.base_currency') baseCode = v;
              else displayCode = v;
            }
          } catch {
            /* 非法配置回退默認 */
          }
        }
        const all = (curRes.data?.currencies || []) as any[];
        currencyList = all.map((c) => ({
          code: c.code, symbol: c.symbol, position: c.position,
          precision: displayPrecision(c.precision ?? 0), rate: String(c.rate_json ?? '1'),
        }));
        baseCurrencyMeta.value = { ...(currencyList.find(c => c.code === baseCode) || { ...DEFAULT_META, code: baseCode }), rate: '1' };
        currencyList = currencyList.map(c => c.code === baseCode ? { ...c, rate: '1' } : c);
        let stored: string | null = null;
        try { stored = localStorage.getItem(CURRENCY_KEY); } catch { /* Keep the site default when storage is unavailable. */ }
        const picked =
          currencyList.find((c) => c.code === stored) ||
          currencyList.find((c) => c.code === displayCode) || // 站點默認展示貨幣（結算仍基準幣）
          currencyList.find((c) => c.code === baseCode) ||
          { code: baseCode, symbol: '¥', position: 'prefix', precision: 2, rate: '1' };
        setCurrency(picked);
      } catch {
        /* 加載失敗回退默認 ¥/2 */
      }
    })();
  }
  return initPromise;
}

// 兼容已保存的充值備註：只轉換系統生成的整段文案，金額跟隨當前貨幣。
// 原始流水與賬務金額保持不變，其他人工備註原樣顯示。
export function formatTransactionRemark(remark?: string): string {
  if (!remark) return '';
  const match = /^(充值到賬|對接賬戶自助充值到賬)（(?:用戶 #(\d+)，)?本金\s*(\d+)\s*分\s*[+＋]\s*贈送\s*(\d+)\s*分）$/.exec(remark);
  if (!match) return remark;
  const principal = Number(match[3]);
  const gift = Number(match[4]);
  const total = principal + gift;
  if (![principal, gift, total].every(Number.isSafeInteger)) return remark;
  const amount = gift > 0
    ? $t("{0}（本金 {1}＋贈送 {2}）", [formatMoney(total), formatMoney(principal), formatMoney(gift)])
    : formatMoney(principal);
  return `${$t(match[1])} ${amount}${match[2] ? $t("（用戶 #{0}）", [match[2]]) : ''}`;
}

export const TRANSACTION_TYPES: Record<string, string> = {
  get adjust() { return $t("人工調賬"); }, get recharge() { return $t("餘額充值"); }, get order_pay() { return $t("訂單支付"); }, get payment() { return $t("訂單支付"); },
  get order_refund() { return $t("訂單退款"); }, get refund() { return $t("訂單退款"); }, get ticket_urgent() { return $t("工單付費加急"); },
  get commission() { return $t("分銷佣金入賬"); }, get commission_debt() { return $t("佣金欠款扣回"); }, get commission_reversal() { return $t("佣金退回"); },
  get withdraw() { return $t("提現"); }, get freeze() { return $t("餘額凍結"); }, get unfreeze() { return $t("餘額解凍"); }, get giftcard() { return $t("禮品卡兌換"); },
  get earn_recharge() { return $t("充值贈送積分"); }, get redeem() { return $t("積分兌換"); },
};
export function transactionType(type?: string): string {
  return TRANSACTION_TYPES[type || ''] || $t("其他收支");
}
export function transactionAmount(row: { direction: string; amount_cents: number }): number {
  return (row.direction === 'out' ? -1 : 1) * Math.abs(row.amount_cents);
}
export function transactionReference(row: { id?: number; display_reference?: string; reference?: string }): string {
  if (row.display_reference) return row.display_reference;
  const [kind, id] = (row.reference || '').split(':');
  const labels: Record<string, string> = { get order_pay() { return $t("訂單"); }, get order_refund() { return $t("退款記錄"); }, get ticket_urgent() { return $t("工單"); }, get recharge() { return $t("充值支付記錄"); }, get giftcard() { return $t("禮品卡"); }, get commission() { return $t("佣金記錄"); }, get adjust() { return $t("調賬記錄"); } };
  return labels[kind] && id ? `${labels[kind]} ${kind === 'ticket_urgent' ? '' : '#'}${id}` : $t("流水 #{0}", [row.id || '—']);
}
export function transactionRemark(row: { id?: number; type?: string; remark?: string; reference?: string; display_reference?: string }): string {
  const remark = row.remark?.trim();
  if (!remark || remark === row.reference) return transactionType(row.type);
  if (TRANSACTION_TYPES[remark]) return TRANSACTION_TYPES[remark];
  if (/^(order_pay|order_refund|ticket_urgent|recharge|giftcard|commission|adjust):[^\s]+$/.test(remark)) {
    return transactionReference({ ...row, reference: remark, display_reference: remark === row.reference ? row.display_reference : undefined });
  }
  return formatTransactionRemark(row.remark);
}
