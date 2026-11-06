<script setup lang="ts">
import { uiText, t as $t } from '@/i18n';

import type { PaymentQuote } from '@/api';
import { formatMoney, formatBaseMoney, formatPaymentAmount, getBaseCurrency, getCurrency } from '@/api/client';
defineProps<{ quote: PaymentQuote | null; loading?: boolean; error?: string; recharge?: boolean }>();
defineEmits<{ retry: [] }>();
</script>
<template>
  <div class="payment-breakdown" role="status" aria-live="polite" aria-atomic="true">
    <span v-if="loading" class="payment-note">{{ $t('正在計算支付金額…') }}</span>
    <div v-else-if="error" class="payment-quote-error">{{ uiText(error) }} <button type="button" @click="$emit('retry')">{{ $t('重試') }}</button></div>
    <template v-else-if="quote">
      <p class="payment-note">{{ $t('結算貨幣：{0}', [getBaseCurrency().code]) }}</p>
      <div><span>{{ recharge ? $t('充值本金') : $t('訂單金額') }}</span><span>{{ formatBaseMoney(quote.base_cents) }}</span></div>
      <div><span>{{ $t('支付手續費') }}<template v-if="quote.fee_bearer === 'user' && quote.fee_type === 'percent'">（{{ Number(quote.fee_rate) / 100 }}%）</template></span><span>{{ Number(quote.fee_cents) ? `+${formatBaseMoney(quote.fee_cents)}` : $t('免手續費') }}</span></div>
      <div class="payment-total"><span>{{ $t('實付金額') }}</span><strong>{{ formatBaseMoney(quote.total_cents) }}</strong></div>
      <div v-if="getCurrency().code !== getBaseCurrency().code"><span>{{ $t('參考顯示金額') }}</span><span>{{ formatMoney(quote.total_cents) }}</span></div>
      <div v-if="quote.charged_currency && quote.charged_currency !== getBaseCurrency().code" class="payment-gateway-amount"><span>{{ $t('實際網關扣款') }}</span><strong>{{ formatPaymentAmount(quote) }}</strong></div>
      <p v-if="recharge" class="payment-note">{{ $t('手續費不計入餘額，贈送按充值本金計算。') }}</p>
    </template>
  </div>
</template>
<style scoped>
.payment-breakdown { padding: 14px 16px; border: 1px solid #e5e7eb; background: #fff; border-radius: 12px; color: #475569; font-size: 13px; }
.payment-breakdown > div:not(.payment-quote-error) { display:flex; justify-content:space-between; align-items:baseline; gap:16px; padding:4px 0; }
.payment-total { margin-top:6px; border-top:1px solid #edf0f3; color:#111827; }
.payment-total strong { font-size:20px; color:var(--zc-primary); }
.payment-note { font-size:12px; line-height:1.5; color:#64748b; margin:8px 0 0; }
.payment-quote-error { color:#b91c1c; }
.payment-quote-error button { font:inherit; padding:8px 12px; border:1px solid currentColor; border-radius:6px; background:transparent; color:inherit; cursor:pointer; }
</style>
