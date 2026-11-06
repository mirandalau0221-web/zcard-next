import { t as $t, locale } from '@/i18n';
/** 郵箱或國內 / 國際電話號碼走聯繫方式查詢；含字母的訂單號仍逐單查詢。 */
export function looksLikeContact(value: string): boolean {
  const text = value.trim();
  const digits = text.replace(/\D/g, '').length;
  return text.includes('@') || (/^\+?[0-9 ()-]+$/.test(text) && digits >= 6 && digits <= 15);
}

/** 百分比券存儲應付比例：9000 = 應付 90% = 9 折。 */
export function couponDiscountLabel(value: number | string): string {
  const rate = Number(value);
  return Number.isInteger(rate) && rate > 0 && rate <= 10000
    ? locale.value === "en" ? `${(10000 - rate) / 100}% off` : $t("{0} 折", [rate / 1000])
    : $t("優惠以結算頁為準");
}

/** Proto JSON 省略零值價格，購物車仍須用數值 0 參與累加。 */
export function cartPriceCents(value?: number): number {
  return Number(value ?? 0);
}
