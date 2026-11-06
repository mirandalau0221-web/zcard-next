<template>
  <div class="pay-page">
    <!-- 加載態 -->
    <div v-if="phase === 'loading'" class="pay-center-card">
      <div class="pay-loading-icon pulse">⏳</div>
      <div class="pay-loading-title">{{ $t('正在加載訂單…') }}</div>
    </div>

    <div v-else-if="phase === 'error'" class="pay-center-card">
      <div class="pay-state-title">{{ $t('暫時無法查詢訂單') }}</div>
      <div class="muted" role="alert">{{ uiText(error) }}</div>
      <p class="muted">{{ $t('已付款請勿重複支付。可通過下單郵箱驗證恢復訂單訪問。') }}</p>
      <OrderAccessRecovery :order-no="orderNo" @recovered="retryOrder" />
      <details><summary>{{ $t('虛擬商品或歷史訂單：使用查詢密碼') }}</summary>
      <form @submit.prevent="retryOrder">
        <label for="order-query-password">{{ $t('查詢密碼（遊客訂單）') }}</label>
        <input id="order-query-password" v-model="queryPassword" class="input" type="password" autocomplete="off" />
        <div class="pay-btn-row">
          <button class="btn btn-primary" type="submit" :disabled="retrying">{{ retrying ? $t('查詢中…') : $t('重新查詢') }}</button>
          <router-link class="btn btn-outline" :to="`/fetch?order_no=${orderNo}`">{{ $t('前往取貨') }}</router-link>
        </div>
      </form></details>
    </div>

    <!-- 已取消 / 已過期 -->
    <div v-else-if="phase === 'closed'" class="pay-center-card">
      <div class="pay-state-icon gray">{{ order?.status === 'canceled' ? '🚫' : '⏰' }}</div>
      <div class="pay-state-title">{{ order?.status === 'refunded' ? $t('訂單已退款') : order?.status === 'canceled' ? $t('訂單已取消') : $t('訂單已超時') }}</div>
      <div class="muted">{{ $t('訂單號：') }}{{ orderNo }} · {{ order?.status === 'refunded' ? $t('請核對退款記錄') : $t('未完成支付') }}</div>
      <div class="pay-btn-row">
        <router-link class="btn btn-primary" to="/products">{{ $t('重新選購') }}</router-link>
        <router-link class="btn btn-outline" to="/member?tab=orders">{{ $t('我的訂單') }}</router-link>
      </div>
    </div>

    <!-- 支付成功 🎉 -->
    <div v-else-if="phase === 'success'" class="pay-center-card">
      <div class="pay-state-icon green pop">✓</div>
      <div class="pay-state-title">{{ $t('支付成功') }}</div>
      <div class="muted" style="margin-bottom: 4px;">{{ $t('訂單號：') }}{{ orderNo }}</div>
      <div style="margin-bottom: 6px;">{{ $t('支付金額') }} <b class="pay-amount">{{ formatBaseMoney(order?.paid_total_cents || order?.total_cents || 0) }}</b></div>

      <div v-if="Number(order?.paid_fee_cents) > 0" class="muted">{{ $t('含支付手續費') }} {{ formatBaseMoney(order?.paid_fee_cents || 0) }}</div>
      <!-- 自動取貨：卡密直接展示（會話內記憶查詢密碼；失敗降級提示去取貨頁） -->
      <DeliveryResults v-if="!order?.items.some(i=>['sms_activation','social_service'].includes(i.delivery_kind || '')) && delivery?.items.length" :items="delivery.items" />
      <p v-if="Number(order?.commerce_version)===1" class="muted">{{ $t('實體商品已進入配送流程，請在訂單詳情查看包裹並確認收貨。') }}</p>
      <div v-if="order?.items.some(i=>i.goods_type!=='physical' && !['sms_activation','social_service'].includes(i.delivery_kind || ''))" class="pay-fetch-hint">
        <span>{{ ['delivered', 'completed'].includes(order?.status || '') ? $t('交付已完成，憑訂單號與查詢密碼查看結果') : $t('已付款，正在安排交付。人工服務請在訂單詳情查看進度，請勿重複付款。') }}</span>
        <router-link class="btn btn-primary" :to="`/fetch?order_no=${orderNo}`">{{ $t('前往取貨') }}</router-link>
      </div>

      <p v-if="order?.items.some(i=>i.delivery_kind==='social_service')" class="muted">{{ $t('付款已確認，請前往訂單詳情查看服務進度與退款狀態。') }}</p>
      <p v-if="order?.items.some(i=>i.delivery_kind==='sms_activation')" class="muted">{{ $t('正在獲取號碼，請前往訂單詳情查看短信和取消/完成狀態。') }}</p>
      <p v-if="physicalOnly" class="muted">{{ $t('請保存訂單訪問鏈接，換設備可通過郵箱驗證找回') }}</p>
      <p v-else class="muted">{{ $t('請保存訂單號和下單時的查詢密碼，遊客也可隨時查詢、取貨，無需註冊。') }}</p>
      <div class="pay-btn-row">
        <button class="btn btn-primary" @click="checkOnce(true)">{{ physicalOnly ? $t('刷新訂單狀態') : $t('刷新發貨結果') }}</button>
        <button v-if="physicalOnly && getOrderAccessToken(orderNo)" class="btn btn-outline" @click="saveAccessLink">{{ copiedAccessLink ? $t('已複製') : $t('複製訂單訪問鏈接') }}</button>
        <router-link class="btn btn-outline" :to="`/order/${orderNo}`">{{ $t('查看訂單詳情') }}</router-link>
        <router-link class="btn btn-outline" to="/products">{{ $t('繼續購物') }}</router-link>
      </div>
    </div>

    <!-- 等待回調（輪詢超時兜底） -->
    <div v-else-if="phase === 'waiting'" class="pay-center-card">
      <div class="pay-state-icon amber pulse">🕒</div>
      <div class="pay-state-title">{{ $t('支付處理中') }}</div>
      <div class="muted">{{ $t('如已完成支付，到賬可能有數秒延遲') }}</div>
      <div class="pay-btn-row">
        <button class="btn btn-primary" @click="checkOnce(true)">{{ $t('刷新支付狀態') }}</button>
        <router-link class="btn btn-outline" :to="`/fetch?order_no=${orderNo}`">{{ $t('前往取貨') }}</router-link>
      </div>
    </div>

    <!-- 掃碼支付中 -->
    <div v-else-if="phase === 'qrcode'" class="pay-qr-layout">
      <div class="pay-qr-main">
        <div class="pay-qr-head">
          <span class="pay-channel-icon">{{ payingChannel ? emojiOf(selected.method || payingChannel.code, payingChannel.driver) : '💳' }}</span>
          <div>
            <div class="pay-qr-title">{{ payingChannel?.name || $t('掃碼支付') }}</div>
            <div class="muted">{{ $t('請使用手機掃一掃完成支付') }}</div>
          </div>
          <span v-if="countdownText" class="pay-countdown" :class="{ danger: countdownDanger }">{{ countdownText }}</span>
        </div>
        <div class="pay-qr-box">
          <img v-if="qrDataUrl" :src="qrDataUrl" :alt="$t('支付二維碼')" />
          <div v-else class="pay-qr-loading">{{ $t('生成二維碼中…') }}</div>
        </div>
        <PaymentBreakdown :quote="paidQuote" />
        <div class="pay-qr-hint">
          <span class="dot-loader"><span></span><span></span><span></span></span> {{ $t('正在檢測支付結果，付款成功後可查看交付進度與結果') }} </div>
        <button class="pay-change" @click="backToSelect">{{ $t('← 更換支付方式') }}</button>
      </div>
      <aside class="pay-side">
        <div class="pay-side-row"><span class="muted">{{ $t('訂單號') }}</span><span class="pay-mono">{{ orderNo }}</span></div>
        <div class="pay-side-row"><span class="muted">{{ $t('下單時間') }}</span><span>{{ fmtTime(order?.created_at) }}</span></div>
        <div class="pay-side-row"><span class="muted">{{ $t('商品') }}</span><span>{{ itemCount }} {{ $t('件') }}</span></div>
        <div class="pay-side-row total"><span>{{ $t('實付') }}</span><b class="pay-amount">{{ formatBaseMoney(paidQuote?.total_cents || order?.total_cents || 0) }}</b></div>
      </aside>
    </div>

    <!-- 跳轉支付中 -->
    <div v-else-if="phase === 'redirect'" class="pay-center-card">
      <div class="pay-state-icon blue">🚀</div>
      <div class="pay-state-title">{{ $t('正在前往收銀台') }}</div>
      <PaymentBreakdown :quote="paidQuote" />
      <div class="muted">{{ $t('使用 {0} 完成支付；支付後本頁自動檢測', [payingChannel?.name || $t('所選渠道')]) }}</div>
      <div class="pay-btn-row">
        <button class="btn btn-primary" @click="openRedirect">{{ $t('重新打開收銀台') }}</button>
        <button v-if="!redirectParams" class="btn btn-outline" @click="copyLink">{{ $t('複製支付鏈接') }}</button>
        <button class="btn btn-outline" @click="backToSelect">{{ $t('更換支付方式') }}</button>
      </div>
      <div class="pay-qr-hint" style="margin-top: 14px;">
        <span class="dot-loader"><span></span><span></span><span></span></span> {{ $t('正在檢測支付結果…') }} </div>
    </div>

    <!-- 選擇支付（默認態） -->
    <div v-else class="pay-select">
      <!-- 訂單摘要 -->
      <div class="pay-summary">
        <div class="pay-summary-head">
          <span class="muted">{{ $t('訂單號') }}</span>
          <span class="pay-mono">{{ orderNo }}</span>
          <span v-if="countdownText" class="pay-countdown" :class="{ danger: countdownDanger }">⏱ {{ countdownText }}</span>
        </div>
        <div class="pay-summary-amount">
          <span>{{ $t('訂單金額') }}</span>
          <b class="pay-amount">{{ formatMoney(order?.total_cents || 0) }}</b>
        </div>
        <div class="pay-summary-meta">
          <span class="muted">{{ $t('下單時間') }} {{ fmtTime(order?.created_at) }}</span>
          <span class="muted">{{ $t('商品數量：{0}', [itemCount]) }}</span>
        </div>
      </div>

      <!-- 渠道網格（方式級收銀台，與充值頁共用組件） -->
      <div class="pay-channels">
        <div class="pay-channels-title">{{ $t('選擇支付方式') }}</div>
        <PayChannelGrid
          :options="payOptions"
          :channel="selected.channel"
          :method="selected.method"
          @select="selectPayment"
        />
        <div v-if="hasWallet" class="pay-btn-row">
          <button class="btn btn-outline" :disabled="balanceRefreshing" @click="refreshBalance">{{ balanceRefreshing ? $t('查詢餘額中…') : $t('刷新餘額') }}</button>
          <router-link class="btn btn-outline" to="/member?tab=recharge">{{ $t('去充值') }}</router-link>
        </div>
      </div>

      <div v-if="error" class="error" style="margin-bottom: 12px;">{{ uiText(error) }}</div>

      <PaymentBreakdown :quote="quote" :loading="quoteLoading" :error="quoteError" @retry="refreshQuote" />
      <button class="pay-submit" :disabled="!selectedAvailable || submitting || !quote || quoteLoading" @click="pay">
        {{ submitting ? $t('創建支付中…') : quote ? $t('立即支付 {0}', [formatPaymentAmount(quote)]) : $t('等待計算金額') }}
      </button>
      <div class="pay-assure">{{ $t('🔒 支付過程安全加密 · 付款後按商品交付方式處理') }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import DeliveryResults from '@/components/DeliveryResults.vue';
import { submitPaymentForm as submitForm } from "@/utils/payment-form";
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import QRCode from 'qrcode';
import OrderAccessRecovery from '@/components/OrderAccessRecovery.vue';
import { getOrderAccessToken, orderAccessLink, getBalance, createPayment, fetchPaymentChannels, getOrder, fetchDelivery, getOrderPassword, rememberOrderPassword, type ChannelItem, type OrderDetail, type FetchDeliveryReply } from '@/api';
import { getToken, formatMoney, formatBaseMoney, formatPaymentAmount } from '@/api/client';
import { flattenPayOptions, emojiOf } from '@/composables/pay-options';
import PayChannelGrid from '@/components/PayChannelGrid.vue';
import PaymentBreakdown from '@/components/PaymentBreakdown.vue';
import { usePaymentQuote } from '@/composables/payment-quote';
import type { PaymentQuote } from '@/api';

const route = useRoute();
const router = useRouter();
const orderNo = String(route.params.orderNo || '');

// ── 六態：loading → select / qrcode / redirect / success / waiting / closed ──
type Phase = 'loading' | 'select' | 'qrcode' | 'redirect' | 'success' | 'waiting' | 'closed' | 'error';
const phase = ref<Phase>('loading');

const order = ref<OrderDetail | null>(null);
const channels = ref<ChannelItem[]>([]);
// 方式級選擇：channel=渠道碼 + method=方式 code（單方式渠道 method 為空串）
const selected = ref<{ channel: string; method: string }>({ channel: '', method: '' });
let selectionTouched = false;
function selectPayment(channel: string, method: string) { selectionTouched = true; selected.value = { channel, method }; error.value = ''; }
const payingChannel = ref<ChannelItem | null>(null);
const submitting = ref(false);
const error = ref('');
const queryPassword = ref('');
const physicalOnly=computed(()=>!!order.value?.items?.length && order.value.items.every(i=>i.goods_type==='physical'));
const copiedAccessLink=ref(false);
async function saveAccessLink(){try{await navigator.clipboard.writeText(orderAccessLink(orderNo));copiedAccessLink.value=true;}catch{error.value=$t('無法複製鏈接，請允許剪貼板權限後重試');}}
const retrying = ref(false);
const paidQuote = ref<PaymentQuote | null>(null);
const { quote, quoteLoading, quoteError, refreshQuote } = usePaymentQuote(() =>
  order.value?.status === 'pending_payment' && selected.value.channel ? {
    order_no: orderNo, channel: selected.value.channel, method: selected.value.method,
    scene: 'purchase', query_password: getOrderPassword(orderNo), order_access_token: getOrderAccessToken(orderNo),
  } : null,
);


// 二維碼 / 跳轉
const qrDataUrl = ref('');
const redirectUrl = ref('');
const redirectParams = ref<Record<string, string> | null>(null);

// 自動取貨結果
const delivery = ref<FetchDeliveryReply | null>(null);
const copied = ref<number | null>(null);
const copiedAll = ref(false);

// 輪詢（4s；3 分鐘超時進等待回調態）
const PAID_STATES = ['paid', 'fulfilling', 'partially_delivered', 'delivered', 'completed'];
let pollTimer: ReturnType<typeof setInterval> | null = null;
let pollCount = 0;
const POLL_MAX = 45;

// 倒計時
const countdown = ref<number | null>(null);
let cdTimer: ReturnType<typeof setInterval> | null = null;
const countdownText = computed(() => {
  if (countdown.value === null) return '';
  if (countdown.value <= 0) return $t("等待確認狀態");
  const m = Math.floor(countdown.value / 60);
  const s = countdown.value % 60;
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
});
const countdownDanger = computed(() => countdown.value !== null && countdown.value <= 300);

const itemCount = computed(() => order.value?.items?.reduce((s, i) => s + i.quantity, 0) || 0);

// 渠道 → 收銀台方式級選項（共享邏輯見 composables/pay-options.ts）
const walletBalance = ref<number | null>(null);
const walletState = ref<'loading' | 'ready' | 'error'>('loading');
const balanceRefreshing = ref(false);
const hasWallet = computed(() => channels.value.some(c => c.driver === 'wallet'));
let balanceRequest = 0;
let disposed = false;
let balanceIdentity: string | null = null;
const payOptions = computed(() => flattenPayOptions(channels.value).map(option => {
  if (channels.value.find(c => c.code === option.channel)?.driver !== 'wallet') return option;
  const available = walletBalance.value;
  const need = Number(order.value?.total_cents || 0);
  const ready = walletState.value === 'ready' && available !== null;
  const shortage = ready ? Math.max(0, need - available) : 0;
  return { ...option, disabled: !ready || shortage > 0,
    availability: !ready ? (walletState.value === 'error' ? $t("餘額查詢失敗，請重試") : $t("正在查詢可用餘額…"))
      : $t("可用餘額 {0}{1}", [formatMoney(available), shortage > 0 ? $t("，還差 {0}", [formatMoney(shortage)]) : '']) };
}));
const selectedAvailable = computed(() => payOptions.value.some(o => !o.disabled && o.channel === selected.value.channel && o.method === selected.value.method));
watch(payOptions, options => {
  if (options.some(o => !o.disabled && o.channel === selected.value.channel && o.method === selected.value.method)) return;
  // A selected wallet becoming unavailable requires a deliberate new choice.
  if (selected.value.channel) { selectionTouched = true; error.value = $t("所選支付方式暫不可用，請刷新餘額或選擇其他支付方式"); selected.value = { channel: '', method: '' }; return; }
  if (selectionTouched) return;
  const first = options.find(o => !o.disabled);
  if (first) selected.value = { channel: first.channel, method: first.method };
});
async function refreshBalance() {
  if (!hasWallet.value || disposed) return;
  const token = getToken();
  const request = ++balanceRequest;
  if (balanceIdentity !== token) { walletBalance.value = null; balanceIdentity = token; }
  balanceRefreshing.value = true;
  if (walletBalance.value === null) walletState.value = 'loading';
  if (!token) { walletBalance.value = null; walletState.value = 'error'; balanceRefreshing.value = false; return; }
  const { data } = await getBalance().catch(() => ({ data: null }));
  if (disposed || request !== balanceRequest) return;
  balanceRefreshing.value = false;
  if (token !== getToken()) { walletBalance.value = null; walletState.value = 'error'; return; }
  const value = Number(data?.available_cents ?? 0);
  walletBalance.value = data && Number.isSafeInteger(value) && value >= 0 ? value : null;
  walletState.value = walletBalance.value === null ? 'error' : 'ready';
}
function refreshOnFocus() { if (phase.value === 'select') void refreshBalance(); }


onMounted(async () => {
  // 支付回跳/分享兜底：?pwd= 直填會話記憶（不落 URL 歷史——replace 清參）
  const qpwd = route.query.pwd;
  if (typeof qpwd === 'string' && qpwd) {
    rememberOrderPassword(orderNo, qpwd);
    router.replace({ path: `/payment/${orderNo}` });
  }
  window.addEventListener('focus', refreshOnFocus);
  window.addEventListener('storage', refreshOnFocus);
  await initializePayment();
});

async function initializePayment() {
  if (!await refreshOrder()) return;
  if (phase.value === 'success') { await loadDelivery(); if (!order.value?.commerce_version && !['delivered', 'completed'].includes(order.value?.status || '')) startPolling(); return; }
  if (phase.value === 'closed') return;
  const { data } = await fetchPaymentChannels();
  if (disposed) return;
  channels.value = (data?.channels || []).filter(c=>!order.value?.items.some(i=>['sms_activation','social_service'].includes(i.delivery_kind || '')) || c.driver==='wallet');
  await refreshBalance();
  if (disposed) return;
  const first = payOptions.value.find(o => !o.disabled);
  if (!selectionTouched) selected.value = first ? { channel: first.channel, method: first.method } : { channel: '', method: '' };
  startPolling();
}

async function retryOrder() {
  retrying.value = true;
  if (queryPassword.value) rememberOrderPassword(orderNo, queryPassword.value);
  try { await initializePayment(); } finally { retrying.value = false; }
}

onUnmounted(() => { disposed = true; balanceRequest++; window.removeEventListener('focus', refreshOnFocus); window.removeEventListener('storage', refreshOnFocus); stopPolling(); stopCountdown(); });

// ── 訂單加載與狀態分發 ──
// 遊客訂單查詢需帶下單時密碼（會話記憶）；登錄本人訂單免密——兩參數都傳由後端裁決
async function refreshOrder() {
  const pwd = getOrderPassword(orderNo);
  const { data, error: loadError } = await getOrder(orderNo, pwd || undefined).catch(() => ({ data: null, get error() { return $t("網絡異常，請重試"); } }));
  if (disposed) return false;
  if (!data) {
    error.value = loadError || $t("訂單加載失敗，請重試");
    if (!order.value) phase.value = 'error';
    return false;
  }
  error.value = '';
  order.value = data;
  if (data.expires_at) startCountdown(data.expires_at);
  decidePhase();
  return true;
}

function decidePhase() {
  const st = order.value?.status;
  if (!st) return;
  if (PAID_STATES.includes(st)) phase.value = 'success';
  else if (st === 'canceled' || st === 'expired' || st === 'refunded') phase.value = 'closed';
  else if (phase.value === 'loading' || phase.value === 'error') phase.value = 'select';
}

// ── 輪詢 ──
function startPolling() {
  stopPolling();
  pollCount = 0;
  pollTimer = setInterval(async () => {
    pollCount += 1;
    if (!await refreshOrder()) { stopPolling(); return; }
    const st = order.value?.status;
    if (st && PAID_STATES.includes(st)) {
      phase.value = 'success';
      await loadDelivery();
      if (order.value?.commerce_version || ['delivered', 'completed'].includes(st) || pollCount >= POLL_MAX) stopPolling();
      return;
    }
    if (phase.value === 'closed') { stopPolling(); return; }
    if (pollCount >= POLL_MAX) { stopPolling(); phase.value = 'waiting'; }

  }, 4000);
}
function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

async function checkOnce(manual = false) {
  await refreshOrder();
  decidePhase();
  if (phase.value === 'success') { await loadDelivery(); if (!order.value?.commerce_version && !['delivered', 'completed'].includes(order.value?.status || '')) startPolling(); return; }
  if (manual && phase.value === 'waiting') window.alert($t("暫未檢測到支付，請稍後再試"));
  if (phase.value === 'qrcode' || phase.value === 'redirect' || phase.value === 'waiting') startPolling();
}

// ── 倒計時 ──
function startCountdown(expiresAt: number) {
  stopCountdown();
  const tick = () => { countdown.value = Math.max(0, expiresAt - Math.floor(Date.now() / 1000)); };
  tick();
  cdTimer = setInterval(tick, 1000);
}
function stopCountdown() {
  if (cdTimer) { clearInterval(cdTimer); cdTimer = null; }
}

// ── 支付創建 ──
async function pay() {
  if (!selectedAvailable.value || submitting.value || !quote.value || quoteLoading.value) return;
  submitting.value = true;
  error.value = '';
  qrDataUrl.value = '';
  redirectUrl.value = '';
  redirectParams.value = null;
  payingChannel.value = channels.value.find((c) => c.code === selected.value.channel) || null;
  const { data, error: err } = await createPayment(orderNo, selected.value.channel, selected.value.method, quote.value.quote_key, getOrderPassword(orderNo));
  submitting.value = false;
  if (err || !data) { error.value = err || $t("創建支付失敗"); if (payingChannel.value?.driver === 'wallet') { walletBalance.value = null; await refreshBalance(); } await refreshQuote(); return; }
  paidQuote.value = data.quote || quote.value;

  const payload = data.payload || '';
  // 餘額支付：同步扣款即完成，不走收銀台跳轉（後端 payload 為本頁地址，避免彈窗）
  if (payingChannel.value?.driver === 'wallet') {
    await refreshOrder();
    decidePhase();
    if (phase.value === 'success') loadDelivery();
    return;
  }
  if (data.type === 'qrcode') {
    let content = payload;
    try { const parsed = JSON.parse(payload); content = parsed.code_url || payload; } catch { /* 原文即內容 */ }
    qrDataUrl.value = await makeQr(content);
    phase.value = 'qrcode';
  } else if (data.type === 'params') {
    // 易支付表單 POST：自動提交
    try {
      const p = JSON.parse(payload);
      redirectUrl.value = p.url || '';
      redirectParams.value = p.params || {};
      phase.value = 'redirect';
      openRedirect();
    } catch { error.value = $t("支付參數異常"); }
  } else {
    let url = payload;
    try { const parsed = JSON.parse(payload); url = parsed.url || payload; } catch { /* 原文即 URL */ }
    redirectUrl.value = url;
    phase.value = 'redirect';
    openRedirect();
  }
  startPolling();
}

async function makeQr(content: string): Promise<string> {
  if (content.startsWith('data:image') || content.startsWith('http')) return content;
  try {
    return await QRCode.toDataURL(content, { width: 220, margin: 1, errorCorrectionLevel: 'M' });
  } catch {
    return '';
  }
}

function openRedirect() {
  if (!redirectUrl.value) return;
  if (redirectParams.value) { submitForm(redirectUrl.value, redirectParams.value); return; }
  // 新窗口打開 + noopener 防反向控制（大廠同款紀律）
  window.open(redirectUrl.value, '_blank', 'noopener');
}

async function copyLink() {
  try {
    await navigator.clipboard.writeText(redirectUrl.value);
    window.alert($t("支付鏈接已複製"));
  } catch { /* 忽略 */ }
}

function backToSelect() {
  stopPolling();
  phase.value = 'select';
  void refreshBalance();
  qrDataUrl.value = '';
  redirectUrl.value = '';
  redirectParams.value = null;
}

// ── 支付成功：自動取貨（會話內記憶的查詢密碼；失敗降級去取貨頁）──
async function loadDelivery() {
 if(order.value?.items.every(i=>i.goods_type==='physical'))return;
  const pwd = getOrderPassword(orderNo);
  if (!pwd && !getToken() && !getOrderAccessToken(orderNo)) return;
  const { data } = await fetchDelivery(orderNo, pwd).catch(() => ({ data: null }));
  if (data) delivery.value = data;
}

async function copyOne(content: string, index: number) {
  try {
    await navigator.clipboard.writeText(content);
    copied.value = index;
    setTimeout(() => { if (copied.value === index) copied.value = null; }, 1500);
  } catch { /* 忽略 */ }
}
async function copyAll() {
  if (!delivery.value) return;
  try {
    await navigator.clipboard.writeText(delivery.value.items.map((i) => i.content).join('\n'));
    copiedAll.value = true;
    setTimeout(() => (copiedAll.value = false), 1500);
  } catch { /* 忽略 */ }
}

function fmtTime(ts?: number): string {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '-';
}
</script>

<style scoped>
.pay-page { max-width: 760px; margin: 0 auto; display: flex; flex-direction: column; gap: 16px; }

/* ── 居中態卡（成功/等待/關閉/跳轉）── */
.pay-center-card {
  background: #fff; border: 1px solid #e5e7eb; border-radius: 14px;
  padding: 36px 24px; text-align: center;
  box-shadow: 0 4px 16px rgba(15, 23, 42, 0.05);
}
.pay-state-icon {
  width: 64px; height: 64px; margin: 0 auto 14px;
  border-radius: 999px; display: flex; align-items: center; justify-content: center;
  font-size: 30px;
}
.pay-state-icon.green { background: #dcfce7; color: #16a34a; font-size: 34px; font-weight: 800; }
.pay-state-icon.amber { background: #fef3c7; }
.pay-state-icon.gray { background: #f3f4f6; }
.pay-state-icon.blue { background: #dbeafe; }
.pay-state-title { font-size: 20px; font-weight: 800; color: #111827; margin-bottom: 6px; }
.pay-loading-icon { font-size: 36px; margin-bottom: 10px; }
.pay-loading-title { color: #6b7280; }
.pulse { animation: pulse 1.6s ease-in-out infinite; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
.pop { animation: pop 0.45s cubic-bezier(0.34, 1.56, 0.64, 1); }
@keyframes pop { 0% { transform: scale(0.4); opacity: 0; } 100% { transform: scale(1); opacity: 1; } }

.pay-amount { color: #ff5722; font-size: 24px; font-weight: 800; }
.pay-btn-row { display: flex; gap: 10px; justify-content: center; margin-top: 18px; flex-wrap: wrap; }
.pay-mono { font-family: ui-monospace, Menlo, monospace; font-size: 13px; word-break: break-all; }

/* ── 成功態卡密 ── */
.pay-cards { text-align: left; background: #f8fafc; border: 1px solid #f1f5f9; border-radius: 12px; padding: 14px; margin-top: 18px; }
.pay-cards-head { display: flex; justify-content: space-between; align-items: center; font-size: 13px; font-weight: 700; margin-bottom: 10px; }
.pay-copy-all { border: none; background: none; color: var(--zc-primary); font-size: 13px; cursor: pointer; }
.pay-copy-all:hover { text-decoration: underline; }
.pay-card-row {
  display: flex; align-items: center; gap: 10px;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 8px;
  padding: 10px 12px; margin-bottom: 8px;
}
.pay-card-row:last-child { margin-bottom: 0; }
.pay-card-index { font-size: 11px; color: #9ca3af; width: 22px; flex-shrink: 0; }
.pay-card-code { flex: 1; font-family: ui-monospace, Menlo, monospace; font-size: 13px; word-break: break-all; user-select: all; }
.pay-card-copy {
  border: none; background: none; cursor: pointer; flex-shrink: 0;
  font-size: 12px; color: var(--zc-primary); padding: 4px 8px; border-radius: 6px; opacity: 0.55; transition: all 0.15s;
}
.pay-card-row:hover .pay-card-copy { opacity: 1; }
.pay-card-copy:hover { background: var(--zc-primary-soft); }
.pay-fetch-hint {
  display: flex; align-items: center; justify-content: center; gap: 12px; flex-wrap: wrap;
  background: var(--zc-primary-soft); border: 1px solid var(--zc-primary-tint); border-radius: 10px; padding: 14px; margin-top: 18px; font-size: 13px;
}

/* ── 掃碼態 ── */
.pay-qr-layout { display: grid; grid-template-columns: 1fr; gap: 16px; }
@media (min-width: 768px) { .pay-qr-layout { grid-template-columns: 1.6fr 1fr; } }
.pay-qr-main { background: #fff; border: 1px solid #e5e7eb; border-radius: 14px; padding: 20px; text-align: center; }
.pay-qr-head { display: flex; align-items: center; gap: 10px; text-align: left; margin-bottom: 16px; }
.pay-qr-title { font-size: 15px; font-weight: 700; color: #111827; }
.pay-countdown {
  margin-left: auto; font-family: ui-monospace, Menlo, monospace; font-size: 13px; font-weight: 700;
  background: #dcfce7; color: #15803d; padding: 3px 10px; border-radius: 999px;
}
.pay-countdown.danger { background: #fee2e2; color: #b91c1c; }
.pay-channel-icon {
  width: 38px; height: 38px; border-radius: 10px; flex-shrink: 0;
  background: #f1f5f9; display: inline-flex; align-items: center; justify-content: center; font-size: 18px;
}
.pay-qr-box {
  width: 244px; margin: 0 auto; background: #fff; border: 1px solid #e5e7eb;
  border-radius: 12px; padding: 12px; box-shadow: 0 4px 12px rgba(15, 23, 42, 0.06);
}
.pay-qr-box img { width: 100%; display: block; }
.pay-qr-loading { padding: 96px 0; color: #9ca3af; font-size: 13px; }
.pay-qr-amount { margin-top: 14px; font-size: 14px; }
.pay-qr-hint { margin-top: 12px; font-size: 13px; color: #6b7280; display: flex; align-items: center; justify-content: center; gap: 8px; }
.pay-change {
  margin-top: 14px; border: none; background: none; color: var(--zc-primary); font-size: 13px; cursor: pointer;
}
.pay-change:hover { text-decoration: underline; }

/* 點動畫（檢測中） */
.dot-loader { display: inline-flex; gap: 4px; }
.dot-loader span {
  width: 6px; height: 6px; border-radius: 999px; background: var(--zc-primary);
  animation: bounce 1.2s infinite ease-in-out;
}
.dot-loader span:nth-child(2) { animation-delay: 0.15s; }
.dot-loader span:nth-child(3) { animation-delay: 0.3s; }
@keyframes bounce { 0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; } 40% { transform: scale(1); opacity: 1; } }

/* ── 側欄摘要 ── */
.pay-side {
  background: #fff; border: 1px solid #e5e7eb; border-radius: 14px; padding: 16px;
  display: flex; flex-direction: column; gap: 12px; align-self: start;
}
.pay-side-row { display: flex; justify-content: space-between; align-items: center; gap: 8px; font-size: 13px; }
.pay-side-row.total {
  border-top: 1px solid #f3f4f6; padding-top: 12px; font-weight: 700; font-size: 14px;
  justify-content: space-between;
}

/* ── 選擇態 ── */
.pay-select { display: flex; flex-direction: column; gap: 16px; }
.pay-summary { background: linear-gradient(135deg, var(--zc-primary-soft), #fff); border: 1px solid var(--zc-primary-tint); border-radius: 14px; padding: 20px; }
.pay-summary-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.pay-summary-head .pay-countdown { margin-left: auto; }
.pay-summary-amount { display: flex; align-items: baseline; gap: 10px; margin-top: 10px; font-size: 14px; color: #374151; }
.pay-summary-meta { display: flex; gap: 14px; margin-top: 8px; flex-wrap: wrap; }

.pay-channels { background: #fff; border: 1px solid #e5e7eb; border-radius: 14px; padding: 18px; }
.pay-channels-title { font-size: 15px; font-weight: 700; color: #111827; margin-bottom: 14px; }

.pay-submit {
  width: 100%; padding: 14px 0; border: none; cursor: pointer;
  border-radius: 12px; font-size: 16px; font-weight: 700; color: #fff;
  background: linear-gradient(90deg, var(--zc-primary), var(--zc-primary-hover));
  box-shadow: 0 6px 18px color-mix(in srgb, var(--zc-primary) 30%, transparent); transition: all 0.15s;
}
.pay-submit:hover:not(:disabled) { transform: translateY(-1px); box-shadow: 0 8px 24px color-mix(in srgb, var(--zc-primary) 40%, transparent); }
.pay-submit:disabled { opacity: 0.5; cursor: not-allowed; }
.pay-assure { text-align: center; font-size: 12px; color: #9ca3af; }
</style>
