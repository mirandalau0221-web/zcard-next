<template>
  <div class="order-detail">
    <!-- 頁頭：返回 + 標題 -->
    <div class="od-head">
      <router-link class="od-back" :to="isLoggedIn ? '/member?tab=orders' : '/fetch'">← {{ isLoggedIn ? $t('返回訂單列表') : $t('取貨查詢') }}</router-link>
      <h2 class="od-title">{{ $t('訂單詳情') }}</h2>
    </div>

    <div v-if="loading" class="card muted" style="padding: 48px; text-align: center;">{{ $t('加載中…') }}</div>
    <div v-else-if="error" class="card" style="padding: 24px;">
      <p class="error">{{ uiText(error) }}</p>
      <OrderAccessRecovery :order-no="orderNo" @recovered="loadOrder" />
      <details><summary>{{ $t('虛擬商品或歷史訂單：使用查詢密碼') }}</summary><form @submit.prevent="loadOrder"><label>{{ $t('下單時的查詢密碼') }}<input v-model="password" class="input" type="password" autocomplete="off" /></label><button class="btn" type="submit">{{ $t('查詢訂單') }}</button></form></details>
    </div>

    <template v-else-if="order">
      <!-- 狀態條：狀態徽章 + 訂單號 + 下單時間（鋪滿全寬） -->
      <div class="card od-status-bar">
        <span :class="statusBadge(order.status)" class="od-status-badge">{{ statusText(order.status) }}</span>
        <div class="od-status-meta">
          <div class="od-order-no">{{ $t('訂單號：') }}{{ order.order_no }}</div>
          <div class="muted">{{ $t('下單時間：') }}{{ fmtTime(order.created_at) }}</div>
        </div>
      </div>

      <div v-if="!order.items.some(i=>['sms_activation','social_service'].includes(i.delivery_kind || '')) && ['paid', 'fulfilling', 'partially_delivered'].includes(order.status)" class="card">
        <p>{{ order.status === 'partially_delivered' ? $t('已付款，部分商品已發貨，其餘商品正在安排發貨。請勿重複付款；長時間未發貨請憑訂單號聯繫客服。') : $t('已付款，商品正在安排發貨。請勿重複付款；長時間未發貨請憑訂單號聯繫客服。') }}</p>
        <button class="btn secondary" @click="loadOrder">{{ $t('刷新訂單狀態') }}</button>
      </div>
      <SMSOrder v-if="order.items.some(i=>i.delivery_kind==='sms_activation') && !['pending_payment','canceled','expired'].includes(order.status)" :order-no="order.order_no" :metadata="order.items.find(i=>i.delivery_kind==='sms_activation')?.sms_product" />
      <SocialOrder v-if="order.items.some(i=>i.delivery_kind==='social_service') && !['pending_payment','canceled','expired'].includes(order.status)" :order-no="order.order_no" @refresh="loadOrder" />
      <ShippingDetails v-if="Number(order.commerce_version)===1" :order="order" :password="password" @refresh="loadOrder" />
      <div class="od-body" :class="{'od-social-body':order.items.some(i=>i.delivery_kind==='social_service')}">
        <!-- 左列：商品清單（grid 行式：PC 四列對齊表頭；移動端每行兩行塊狀——大廠訂單詳情同構） -->
        <div class="card od-items">
          <div class="od-section-title">{{ $t('商品清單（{0}）', [order.items.length]) }}</div>
          <div class="od-item-list">
            <div class="od-item-head">
              <span>{{ $t('商品') }}</span><span class="od-ta-r">{{ $t('單價') }}</span><span class="od-ta-c">{{ $t('數量') }}</span><span class="od-ta-r">{{ $t('小計') }}</span>
            </div>
            <div v-for="(it, i) in order.items" :key="i" class="od-item">
              <div class="od-item-name">{{ it.product_name }}
                <p class="muted">{{it.goods_type === 'physical' ? $t('快遞配送') : it.fulfillment_type === 'manual' ? $t('人工服務') : it.fulfillment_type === 'upstream' ? $t('上游交付') : $t('自動交付')}} · {{itemStatus(it.fulfillment_status)}}</p>
                <p v-if="it.goods_type === 'physical'" class="muted">{{ $t('已發') }} {{ it.shipped_quantity || 0 }} {{ $t('件 · 已收') }} {{ it.received_quantity || 0 }} {{ $t('件') }}<span v-if="it.canceled_quantity"> {{ $t('· 已取消') }} {{ it.canceled_quantity }} {{ $t('件') }}</span><span v-if="it.returned_quantity"> {{ $t('· 已退貨') }} {{ it.returned_quantity }} {{ $t('件') }}</span></p>
                <p v-else-if="it.canceled_quantity" class="muted">{{ $t('已取消') }} {{ it.canceled_quantity }} {{ $t('件') }}</p>
                <dl v-if="answers(it.form_answers_json).length" class="od-answers"><div v-for="(a,j) in answers(it.form_answers_json)" :key="j"><dt>{{a.name}}</dt><dd>{{a.value}}</dd></div></dl>
              </div>
              <div class="od-item-price">{{ formatMoney(it.unit_price_cents) }}</div>
              <div class="od-item-qty"><template v-if="it.delivery_kind==='social_service'">{{ $t('按填寫配置') }}</template><template v-else>{{ $t('購買 ×') }}{{ it.quantity }}</template></div>
              <div class="od-item-sub">{{ formatMoney(Number(order.commerce_version)===1?it.paid_cents || 0:it.amount_cents ?? it.unit_price_cents * it.quantity) }}</div>
            </div>
          </div>
        </div>

        <!-- 右列：訂單摘要（金額 + 操作） -->
        <div class="od-side">
          <div class="card">
            <div class="od-section-title">{{ $t('訂單金額') }}</div>
            <div class="od-amount-row">
              <span>{{order.status === 'pending_payment' ? $t('訂單應付') : ['canceled','expired'].includes(order.status) ? $t('訂單金額') : $t('實付合計')}}</span>
              <b class="od-amount">{{ formatMoney(order.paid_total_cents || order.total_cents) }}</b>
              <span v-if="Number(order.paid_fee_cents) > 0" class="muted">{{ $t('含手續費') }} {{ formatMoney(order.paid_fee_cents || 0) }}</span>
            </div>
            <p v-if="Number(order.refunded_cents) > 0 || Number(order.refunded_fee_cents) > 0" class="muted">{{ $t('已退款') }} {{ formatMoney(Number(order.refunded_cents || 0) + Number(order.refunded_fee_cents || 0)) }}{{ $t('（含已退手續費') }} {{formatMoney(order.refunded_fee_cents || 0)}}）</p>
            <p class="muted">{{ $t('商品小計為下單優惠後的金額；手續費與整單優惠以訂單金額為準。') }}</p>
          </div>
          <div v-if="!order.items.some(i=>['sms_activation','social_service'].includes(i.delivery_kind || '')) || order.status==='pending_payment'" class="card">
            <div class="od-section-title">{{ $t('可用操作') }}</div>
            <div class="od-actions">
              <router-link class="btn od-action-btn" :to="`/payment/${order.order_no}`" v-if="order.status === 'pending_payment'">{{ $t('去支付') }}</router-link>
              <router-link
                class="btn secondary od-action-btn"
                :to="`/fetch?order_no=${order.order_no}`"
                v-if="order.items.some(it=>it.goods_type!=='physical' && !['sms_activation','social_service'].includes(it.delivery_kind || '')) && ['paid', 'fulfilling', 'partially_delivered', 'delivered', 'completed'].includes(order.status)"
              >{{ $t('查看交付結果') }}</router-link>
              <button class="btn secondary od-action-btn" v-if="isLoggedIn && order.status === 'pending_payment'" @click="cancelOrder">{{ $t('取消訂單') }}</button>
            </div>
          </div>
        </div>
      </div>
      <div v-if="order.items.every(i=>i.goods_type==='physical') && getOrderAccessToken(orderNo)" class="card"><p>{{ $t('請保存訂單訪問鏈接，換設備可通過郵箱驗證找回') }}</p><button class="btn secondary" @click="saveAccessLink">{{ copiedLink ? $t('已複製') : $t('複製訂單訪問鏈接') }}</button><p v-if="linkError" class="error" role="alert">{{ linkError }}</p></div>
      <OrderReview v-if="isLoggedIn" :order-no="orderNo" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import OrderAccessRecovery from '@/components/OrderAccessRecovery.vue';
import SMSOrder from '@/components/SMSOrder.vue';
import SocialOrder from '@/components/SocialOrder.vue';
import { ref, onMounted } from 'vue';
import { useRoute } from 'vue-router';
import { getOrder, getOrderPassword, getOrderAccessToken, orderAccessLink, rememberOrderPassword, cancelMyOrder, type OrderDetail } from '@/api';
import ShippingDetails from "@/components/ShippingDetails.vue";
import OrderReview from '@/components/OrderReview.vue';
import { getToken, formatMoney } from '@/api/client';

const route = useRoute();
const orderNo = String(route.params.orderNo || '');
const password = ref(getOrderPassword(orderNo));
const isLoggedIn = !!getToken();
const loading = ref(false);
const error = ref('');
const order = ref<OrderDetail | null>(null);
const copiedLink = ref(false); const linkError=ref('');
async function saveAccessLink(){try{await navigator.clipboard.writeText(orderAccessLink(orderNo)); copiedLink.value=true;}catch{linkError.value=$t('無法複製鏈接，請允許剪貼板權限後重試');}}

onMounted(loadOrder);
async function loadOrder() {
  loading.value = true;
  const { data, error: err } = await getOrder(orderNo, password.value || undefined).catch(() => ({ data: null, get error() { return $t("訂單不存在或無權查看"); } }));
  loading.value = false;
  if (err) { error.value = err; return; }
  order.value = data;
  error.value = '';
  if (data) rememberOrderPassword(orderNo, password.value);
}

async function cancelOrder() {
  if (!confirm($t("確認取消訂單 {0}？", [orderNo]))) return;
  const { error: err } = await cancelMyOrder(orderNo);
  if (err) { error.value = err; return; }
  // 重新加載：狀態變 canceled 後按鈕消失
  const { data } = await getOrder(orderNo, password.value || undefined).catch(() => ({ data: null }));
  order.value = data;
}

function answers(raw?:string):{name:string;value:string}[]{try{const x=JSON.parse(raw || '[]');return Array.isArray(x)?x:[]}catch{return []}}
function itemStatus(s?:string){return ({get partial() { return $t("部分完成"); },get shipped() { return $t("已發貨"); },get received() { return $t("已收貨"); },get pending() { return $t("待處理"); },get delivering() { return $t("處理中"); },get manual() { return $t("待人工核對"); },get failed() { return $t("處理異常"); },get delivered() { return $t("已完成交付"); },get refunded() { return $t("已退款"); }} as Record<string,string>)[s || 'pending'] || s;}
function statusText(s: string): string {
  if(order.value?.items.some(i=>i.delivery_kind==='social_service')){if(s==='delivered')return $t('已完成');if(s==='partially_delivered')return $t('部分完成');}

  return ({
    get pending_payment() { return $t("待支付"); }, get paid() { return $t("已支付"); }, get fulfilling() { return $t("履約中"); }, get partially_delivered() { return $t("部分發貨"); },
    get delivered() { return $t("已發貨"); }, get completed() { return $t("已完成"); }, get canceled() { return $t("已取消"); }, get expired() { return $t("已過期"); },
    get refund_pending() { return $t("退款中"); }, get refunded() { return $t("已退款"); }, get manual_pending() { return $t("待人工發貨"); },
  } as Record<string, string>)[s] || s;
}
function statusBadge(s: string): string {
  return ({
    pending_payment: 'badge orange', paid: 'badge blue', fulfilling: 'badge blue', partially_delivered: 'badge green',
    delivered: 'badge green', completed: 'badge green', canceled: 'badge gray', expired: 'badge gray',
    refund_pending: 'badge orange', refunded: 'badge red', manual_pending: 'badge orange',
  } as Record<string, string>)[s] || 'badge gray';
}
function fmtTime(ts: number): string {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '';
}
</script>

<style scoped>
.od-answers{margin:8px 0;font-size:13px}.od-answers>div{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}.od-answers dt{font-weight:600}.od-answers dd{margin:0;overflow-wrap:anywhere;white-space:pre-wrap;min-width:0}
.order-detail { display: flex; flex-direction: column; gap: 16px; }

/* 頁頭 */
.od-head { display: flex; align-items: center; gap: 14px; }
.od-back { font-size: 13px; color: #6b7280; text-decoration: none; padding: 4px 8px; border-radius: 6px; transition: all 0.15s; }
.od-back:hover { color: var(--zc-primary); background: var(--zc-primary-soft); }
.od-title { font-size: 20px; margin: 0; color: #111827; }

/* 狀態條：全寬卡片 */
.od-status-bar { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.od-status-badge { font-size: 13px; padding: 3px 12px; }
.od-status-meta { display: flex; flex-direction: column; gap: 2px; }
.od-order-no { font-size: 14px; font-weight: 600; color: #1f2329; word-break: break-all; }

/* 主體：左清單 + 右摘要（大廠訂單詳情經典兩欄；窄屏堆疊） */
.od-body { display: flex; gap: 16px; align-items: flex-start; }
.od-items { flex: 1; min-width: 0; }
.od-side { width: 280px; flex-shrink: 0; display: flex; flex-direction: column; gap: 16px; }
@media (max-width: 860px) {
  .od-body { flex-direction: column; }
  .od-social-body .od-items { width: 100%; box-sizing: border-box; }
  .od-side { width: 100%; order: -1; } /* 摘要+操作置頂：先看到金額與操作再看清單（大廠移動端訂單詳情） */
}

.od-section-title { font-size: 14px; font-weight: 700; color: #111827; margin-bottom: 12px; }

/* 商品清單：行式 grid（PC 四列帶表頭；表頭列寬與商品行同模板保證對齊） */
.od-item-head, .od-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 96px 64px 104px;
  gap: 8px; align-items: center;
}
.od-item-head {
  font-size: 12px; color: #6b7280;
  padding: 8px 12px; background: #f9fafb; border-radius: 8px 8px 0 0;
}
.od-item { padding: 14px 12px; border-bottom: 1px solid #f3f4f6; }
.od-item:last-child { border-bottom: none; }
.od-item-name { font-size: 14px; font-weight: 500; color: #1f2329; line-height: 1.5; min-width: 0; }
.od-item-price { text-align: right; color: #6b7280; font-size: 13px; font-variant-numeric: tabular-nums; }
.od-item-qty { text-align: center; color: #6b7280; font-size: 13px; }
.od-item-sub {
  text-align: right; font-weight: 700; color: #111827; font-size: 15px;
  font-variant-numeric: tabular-nums;
}
.od-ta-r { text-align: right; }
.od-ta-c { text-align: center; }

/* 移動端：表頭隱藏，每行兩行塊狀（名稱+數量 / 單價+小計——大廠訂單詳情商品行） */
@media (max-width: 768px) {
  .od-item-head { display: none; }
  .od-item {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      "name qty"
      "price sub";
    row-gap: 6px; padding: 12px 2px;
  }
  .od-item-name { grid-area: name; }
  .od-item-qty { grid-area: qty; text-align: right; color: #9ca3af; }
  .od-item-price { grid-area: price; text-align: left; }
  .od-item-sub { grid-area: sub; }
}

/* 摘要卡 */
.od-amount-row {
  display: flex; align-items: baseline; justify-content: space-between;
  padding: 4px 0 8px; font-size: 13px; color: #6b7280;
}
.od-amount { font-size: 24px; font-weight: 800; color: #ff5722; font-variant-numeric: tabular-nums; }
.od-actions { display: flex; flex-direction: column; gap: 10px; }
.od-action-btn { text-align: center; }
</style>
