import { displayPrecision, formatCents, fromCents, toCents } from '../../../packages/money/index';
// 金額工具（鐵律 15：DB/API 一律 int64「分」，前端顯示 /100 為元、提交 *100 為分）。
// 符號與小數位取後台默認貨幣（settings i18n.base_currency → currencies 表）；
// 未加載完成回退 ¥/2。顯示與輸入獨立：兩位之後按有效位顯示，提交按十進制精確換算並拒絕不足一分。
// 全站金額顯示/提交必須經本文件，禁止內聯 `xxx / 100` 或硬編碼符號（架構測試守護）。

import { reactive } from "vue";
import { fetchSettings, fetchCurrencies } from "@/service/api";
import { localStg } from "@/utils/storage";

export interface CurrencyMeta {
  code: string;
  symbol: string; // 貨幣符號（¥/$）
  position: string; // prefix | suffix
  precision: number; // 小數位（CNY=2、JPY=0）
}

const DEFAULT_META: CurrencyMeta = { code: "CNY", symbol: "¥", position: "prefix", precision: 2 };

const current = reactive<CurrencyMeta>({ ...DEFAULT_META });

export function setCurrency(meta: Partial<CurrencyMeta>) {
  Object.assign(current, DEFAULT_META, meta);
  current.precision = displayPrecision(current.precision);
}

export function getCurrency(): CurrencyMeta {
  return { ...current };
}

export function currencyUnit(): string {
  return current.code;
}

// fenToYuan 分 → 元字符串（不含符號；純整數運算，禁止浮點參與）。
export function fenToYuan(cents: number): string {
  return formatCents(cents, current.precision);
}

export function yuanToFen(yuan: number): number {
  return toCents(yuan ?? 0);
}

export function centsToYuan(cents: number): number {
  return fromCents(cents);
}

// formatMoney 帶符號格式化（顯示唯一入口；符號位置感知）。
export function formatMoney(cents: number): string {
  const v = fenToYuan(cents);
  return current.position === "suffix" ? `${v}${current.symbol}` : `${current.symbol}${v}`;
}

// formatSignedMoney 有符號金額（流水/調賬：正數帶 +，負數 fenToYuan 自帶 -）。
export function formatSignedMoney(cents: number): string {
  const v = fenToYuan(cents);
  const body = cents > 0 ? `+${v}` : v;
  return current.position === "suffix" ? `${body}${current.symbol}` : `${current.symbol}${body}`;
}

// ── 默認貨幣加載（後台 i18n.base_currency → /admin/currencies 符號/位置/小數位）──

let initPromise: Promise<void> | null = null;

export function initCurrency(force = false): Promise<void> {
  if (force) initPromise = null;
  if (!initPromise) {
    // 未登錄（登錄頁）時跳過：本初始化走需鑑權的 admin 接口，401 會觸發
    // 全局登出級聯（onBackendFail → resetStore），曾把剛登錄成功的會話清掉。
    // 登錄成功後由 auth store 以 force=true 重新初始化。
    if (!localStg.get("token")) {
      initPromise = Promise.resolve();
      return initPromise;
    }
    initPromise = (async () => {
      try {
        const [settingsRes, currencyRes] = await Promise.all([
          fetchSettings("i18n"),
          fetchCurrencies(),
        ]);
        let baseCode = "CNY";
        const items: any[] = ((settingsRes as any)?.data as any)?.items || [];
        const entry = items.find((it: any) => it.key === "base_currency");
        if (entry?.value_json) {
          try {
            const v = JSON.parse(entry.value_json);
            if (typeof v === "string" && v) baseCode = v;
          } catch {
            /* 非法配置回退默認 */
          }
        }
        const list: any[] = ((currencyRes as any)?.data as any)?.currencies || [];
        const cur = list.find((c: any) => c.code === baseCode);
        current.code = baseCode;
        if (cur) {
          setCurrency({ code: baseCode, symbol: cur.symbol, position: cur.position, precision: cur.precision ?? 0 });
        }
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
    ? `${formatMoney(total)}（本金 ${formatMoney(principal)}＋贈送 ${formatMoney(gift)}）`
    : formatMoney(principal);
  return `${match[1]} ${amount}${match[2] ? `（用戶 #${match[2]}）` : ''}`;
}

export const TRANSACTION_TYPES: Record<string, string> = {
  adjust: '人工調賬', recharge: '餘額充值', order_pay: '訂單支付', payment: '訂單支付',
  order_refund: '訂單退款', refund: '訂單退款', ticket_urgent: '工單付費加急',
  commission: '分銷佣金入賬', commission_debt: '佣金欠款扣回', commission_reversal: '佣金退回',
  withdraw: '提現', freeze: '餘額凍結', unfreeze: '餘額解凍', giftcard: '禮品卡兌換',
  earn_recharge: '充值贈送積分', redeem: '積分兌換',
};
export function transactionType(type?: string): string {
  return TRANSACTION_TYPES[type || ''] || '其他收支';
}
export function transactionAmount(row: { direction: string; amount_cents: number }): number {
  return (row.direction === 'out' ? -1 : 1) * Math.abs(row.amount_cents);
}
export function transactionReference(row: { id?: number; display_reference?: string; reference?: string }): string {
  if (row.display_reference) return row.display_reference;
  const [kind, id] = (row.reference || '').split(':');
  const labels: Record<string, string> = { order_pay: '訂單', order_refund: '退款記錄', ticket_urgent: '工單', recharge: '充值支付記錄', giftcard: '禮品卡', commission: '佣金記錄', adjust: '調賬記錄' };
  return labels[kind] && id ? `${labels[kind]} ${kind === 'ticket_urgent' ? '' : '#'}${id}` : `流水 #${row.id || '—'}`;
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
