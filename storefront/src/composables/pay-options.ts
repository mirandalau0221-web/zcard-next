import { t as $t } from '@/i18n';
// 方式級收銀台共享邏輯：渠道 → 顧客可見的支付方式選項（Payment.vue / Member.vue 充值同源）。
import type { ChannelItem } from '@/api';
import { formatMoney } from '@/api/client';

export interface PayOption {
  disabled?: boolean;
  availability?: string;
  channel: string;
  method: string; // 多方式渠道的方式 code；單方式渠道為空串
  name: string;
  icon?: string;
  emoji: string;
  sub: string;
  feeText: string;
  recommended: boolean;
  recommendLabel: string;
  recommendDescription: string;
}

// 方式/渠道內置 emoji 回落（未配置自定義圖標時）
const EMOJI: Record<string, string> = {
  wallet: '💰', alipay: '🅰️', wxpay: '💬', wechat: '💬', qqpay: '🐧',
  epay: '⚡', epusdt: '₮', bepusdt: '₮', upay: '₮', stripe: '🟦', paypal: '🅿️',
};

export function emojiOf(code: string, driver: string) {
  return EMOJI[code] || EMOJI[driver] || '💳';
}

// 渠道 → 收銀台方式級選項展平：多方式渠道（易支付/USDT 網關）每個方式一個選項，
// 顧客看到的是「支付寶 / 微信 / USDT·TRC20」而不是網關本身；單方式渠道保持原樣。
export function flattenPayOptions(channels: ChannelItem[]): PayOption[] {
  const out: PayOption[] = [];
  for (const c of channels) {
    const methods = (c.methods || []).filter((m) => m.name);
    if (methods.length > 0) {
      for (const m of methods) {
        out.push({
          channel: c.code, method: m.code, name: m.name,
          feeText: feeText(c), recommended: !!m.recommended, recommendLabel: m.recommend_label || $t("推薦"), recommendDescription: m.recommend_description || "",
          icon: m.icon || c.icon || undefined, emoji: emojiOf(m.code, c.driver),
          sub: c.name || (['epusdt', 'bepusdt', 'upay'].includes(c.driver) ? $t("數字貨幣收款") : $t("在線支付")),
        });
      }
    } else {
      out.push({
        channel: c.code, method: '', name: c.name,
        feeText: feeText(c), recommended: !!c.recommended, recommendLabel: c.recommend_label || $t("推薦"), recommendDescription: c.recommend_description || '',
        icon: c.icon || undefined, emoji: emojiOf(c.code, c.driver),
        sub: c.driver === 'wallet' ? $t("使用賬户餘額") : $t("在線支付"),
      });
    }
  }
  return out;
}

function feeText(c: ChannelItem): string {
  if (c.driver === 'wallet' || c.fee_bearer !== 'user' || !Number(c.fee)) {
    return ['epusdt', 'bepusdt', 'upay'].includes(c.driver) ? $t("平台免手續費") : $t("免手續費");
  }
  return c.fee_type === 'percent' ? $t("手續費 {0}%", [Number(c.fee) / 100]) : $t("手續費 {0}", [formatMoney(Number(c.fee))]);
}
