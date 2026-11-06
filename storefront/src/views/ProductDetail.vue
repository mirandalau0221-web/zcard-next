<template>
  <div v-if="p" class="pd-page">
    <!-- 麪包屑 -->
    <div v-if="p.product_kind !== 'sms_channel'" class="pd-crumb">
      <router-link to="/">{{ $t('首頁') }}</router-link>
      <span class="pd-crumb-sep">/</span>
      <router-link to="/products">{{ $t('全部商品') }}</router-link>
      <span class="pd-crumb-sep">/</span>
      <span class="pd-crumb-current">{{ p.name }}</span>
    </div>

    <SocialProduct v-if="p.delivery_kind === 'social_service'" :key="p.id" :product="p" />
    <SMSProduct v-else-if="p.product_kind === 'sms_channel'" :key="p.id" :product="p" />
    <div v-else class="pd-main">
      <!-- 左欄：商品圖 -->
      <div class="pd-gallery">
        <div class="pd-cover" :role="p.cover ? 'button' : undefined" :tabindex="p.cover ? 0 : undefined" :aria-label="$t('查看商品大圖')" @click="openCover" @keydown.enter="openCover" @keydown.space.prevent="openCover">
          <img v-if="p.cover" :src="p.cover" :alt="p.name" @error="onImgError" />
          <img v-else :src="NO_IMAGE" :alt="p.name" class="pd-noimg" />
          <span v-if="p.cover" class="pd-zoom-hint">{{ $t('🔍 點擊查看大圖') }}</span>
        </div>
      </div>

      <!-- 右欄：購買區 -->
      <div class="pd-buy">
<p v-if="physicalProduct" class="muted">
  {{ $t('實體商品 ·') }}
  <template v-if="p.shipping_mode === 'fixed'">{{ $t('運費 {0} / 商品 / 訂單', [formatMoney(p.shipping_fee_cents || 0)]) }}</template>
  <template v-else-if="p.shipping_mode === 'collect'">{{ $t('運費到付') }}</template>
  <template v-else>{{ $t('包郵') }}</template>
</p>
        <h1 class="pd-name">{{ p.name }}</h1>
        <div class="pd-sub">
          <span class="tag">{{ physicalProduct ? $t('實體商品') : manualDelivery ? $t('人工服務') : stockTypeLabel(p.stock_type) }}</span>
          <span v-if="!physicalProduct && !smsProduct && p.points_required && p.points_required > 0" class="tag pd-points-tag">{{ p.points_required }} {{ $t('積分') }}</span>
        </div>

        <!-- 促銷價格區 -->
        <div class="pd-price-card">
          <div class="pd-price-row">
            <span class="pd-price">{{ formatMoney(displayPrice) }}</span>
            <del v-if="selectedFlash && displayPrice < basePrice" class="muted">{{ formatMoney(basePrice) }}</del>
            <span v-if="!physicalProduct && !smsProduct && p.points_required && p.points_required > 0" class="pd-price-points">{{ $t('或') }} {{ p.points_required }} {{ $t('積分兌換') }}</span>
          </div>
          <p v-if="selectedFlash" class="pd-flash-info">{{ flashSoldOut ? $t('本場秒殺已搶完') : $t('限時秒殺') }} · {{ new Date(selectedFlash.end_at * 1000).toLocaleString(localeTag) }} {{ $t('結束') }}<span v-if="!flashSoldOut"> {{ $t('· 剩餘') }} {{ selectedFlash.remaining }} {{ $t('件') }}<span v-if="selectedFlash.per_user_limit > 0"> {{ $t('· 每人限購') }} {{ selectedFlash.per_user_limit }} {{ $t('件') }}</span></span></p>
        </div>

        <!-- 彙總條：銷量/庫存（template.show_sales/show_stock 後台開關可關） -->
        <div v-if="salesShown || stockShown" class="pd-stats">
          <div v-if="salesShown" class="pd-stat"><b>{{ p.sales_count || 0 }}</b><span>{{ $t('銷量') }}</span></div>
          <div v-if="stockShown" class="pd-stat"><b>{{ stockDisplay }}</b><span>{{ $t('庫存') }}</span></div>
        </div>

        <!-- 服務保障 -->
        <div class="pd-assure">
          <span>{{ physicalProduct ? $t('📦 快遞配送') : manualDelivery ? $t('👤 人工處理') : $t('⚡ 自動發貨') }}</span>
          <span>{{ $t('🛡️ 正品保障') }}</span>
          <span>{{ $t('💬 售後無憂') }}</span>
        </div>

        <!-- SKU 選擇（卡片式） -->
        <div v-if="p.skus?.length" class="pd-field">
          <label class="pd-label">{{ $t('選擇規格') }} <span class="pd-req">*</span></label>
          <div class="pd-skus">
            <button
              v-for="s in p.skus"
              :key="s.id"
              class="pd-sku"
              :class="{ active: selectedSku === Number(s.id) }"
              @click="selectedSku = Number(s.id)"
            >
              <span class="pd-sku-name">{{ s.name }}</span>
              <span class="pd-sku-price">{{ formatMoney(flash.price(s.price_cents, s.flash_sale)) }}</span>
            </button>
          </div>
        </div>

        <!-- 數量步進器 -->
        <div class="pd-field">
          <label class="pd-label">{{ $t('購買數量') }}</label>
          <div class="pd-qty">
            <button class="pd-qty-btn" @click="quantity = Math.max(1, quantity - 1)">−</button>
            <input v-model.number="quantity" type="number" min="1" :max="reusableDelivery||smsProduct?1:99" :disabled="reusableDelivery||smsProduct" class="pd-qty-input" />
            <button class="pd-qty-btn" :disabled="reusableDelivery||smsProduct" @click="quantity = Math.min(99, quantity + 1)">＋</button>
          </div>
        </div>

        <!-- 庫存進度條 -->
        <div v-if="stockShown && p.stock_type === 'card' && p.stock >= 0" class="pd-stock-bar">
          <div class="pd-stock-track"><div class="pd-stock-fill" :style="{ width: stockPct }"></div></div>
        </div>

        <!-- 評價入口（錨點；大廠電商：評分摘要卡 → 點擊滾動到評價區） -->
        <button v-if="showReviews" class="pd-review-entry" @click="scrollToReviews">
          <span class="pd-review-entry-score">{{ p.reviews?.length ? `★ ${reviewAvg}` : $t('暫無評分') }}</span>
          <span class="pd-review-entry-count">{{ (p.reviews?.length || 0) }} {{ $t('條評價') }}</span>
          <span class="pd-review-entry-go">{{ $t('查看 ›') }}</span>
        </button>

        <!-- 自定義控件（下單收集） -->
        <OrderFields :controls="p.controls || []" v-model="controlAnswers" />
        <p v-if="reusableDelivery" class="muted">{{ $t('此規格每次購買一份即可。') }}</p>
        <p v-if="manualDelivery" class="muted">{{ $t('人工處理：付款後請在訂單詳情查看處理進度與交付結果。') }}</p>

        <div v-if="!physicalProduct" class="pd-field">
          <label class="pd-label">{{ $t('查詢密碼（取貨用，至少 4 位）') }}<span v-if="trade.queryPasswordRequired" class="pd-req">*</span></label>
          <input v-model="queryPassword" type="text" class="pd-input" :placeholder="$t('用於取貨驗證（忘記將無法取貨）')" />
        </div>
        <div v-if="physicalProduct && isGuest" class="pd-field">
          <label class="pd-label">{{ $t('郵箱（訂單通知與找回）') }} *</label>
          <input v-model="contact" type="email" autocomplete="email" class="pd-input" :placeholder="$t('填寫用於接收訂單驗證碼的郵箱')" />
        </div>
        <div v-else-if="!physicalProduct && (isGuest || trade.contactScope === 'all')" class="pd-field">
          <label class="pd-label">{{ $t('聯繫方式') }} {{ trade.contactRequired !== 'none' ? ' *' : $t('（選填）') }}</label>
          <input v-model="contact" type="text" class="pd-input" :placeholder="$t('用於訂單查詢與售後（{0}）', [contactRequiredLabel(trade.contactRequired)])" />
        </div>
        <div v-if="isGuest && captchaCfg.order" class="pd-field">
          <label class="pd-label">{{ $t('圖形驗證碼') }} <span class="pd-req">*</span></label>
          <CaptchaInput ref="captchaRef" @update:code="captchaCode = $event" @update:captcha-id="captchaId = $event" />
        </div>
        <div class="pd-field">
          <label class="pd-label">{{ $t('優惠券碼（選填）') }}</label>
          <input v-if="!smsProduct" v-model="couponCode" type="text" class="pd-input" :placeholder="$t('輸入優惠券碼')" />
        </div>

        <div v-if="error" class="error" style="margin-bottom: 12px;">{{ uiText(error) }}</div>

        <div v-if="stockUnknown" class="pd-stock-notice" role="status">
          <div>
            <strong>{{ $t('暫時無法確認庫存') }}</strong>
            <p>{{ stockRefreshMessage || $t('尚未獲取到有效庫存，這不代表已售罄。請稍後重新查詢，或聯繫客服確認。') }}</p>
          </div>
          <button type="button" :disabled="stockRefreshing || stockRetrySeconds > 0" :aria-busy="stockRefreshing" @click="refreshStock">
            {{ stockRefreshing ? $t('查詢中…') : stockRetrySeconds > 0 ? $t('{0} 秒後重試', [stockRetrySeconds]) : $t('重新查詢庫存') }}
          </button>
        </div>

        <!-- 缺貨和未知庫存禁購；不限庫存仍由後端按貨源驗證。 -->
        <p v-if="smsProduct" class="muted">{{ $t('單次短信接碼，僅限會員餘額單件購買。付款後在本站訂單頁查看號碼與短信；取消須經供貨確認後退款。') }}{{p.sms_sales_enabled ? '' : $t('接碼新購暫未開放。')}}</p>
        <div class="pd-actions">
          <button class="pd-btn-buy" :disabled="submitting || soldOut || stockUnknown || flashSoldOut || (smsProduct && !p.sms_sales_enabled)" @click="buy">
            {{ submitting ? $t('提交中…') : stockUnknown ? $t('庫存待確認') : soldOut ? $t('暫時缺貨') : $t('立即購買') }}
          </button>
          <button
            v-if="canCart"
            class="pd-btn-cart"
            :class="{ 'is-in': inCartNow }"
            :disabled="submitting || cartBusy || soldOut || stockUnknown || flashSoldOut"
            :title="inCartNow ? $t('從購物車移除') : $t('加入購物車')"
            @click="inCartNow ? removeFromCart() : addToCart()"
          >
            <template v-if="cartBusy">{{ addingCart ? $t('加入中…') : $t('處理中…') }}</template>
            <template v-else-if="inCartNow"><ThemeIcon name="trash" />{{ $t('移除購物車') }}</template>
            <template v-else><ThemeIcon name="cart" />{{ $t('加入購物車') }}</template>
          </button>
          <button v-if="!physicalProduct && !smsProduct && p.points_required && p.points_required > 0" class="pd-btn-points" :disabled="submitting || soldOut || stockUnknown || flashSoldOut" @click="exchangePoints">
            {{ stockUnknown ? $t('庫存待確認') : soldOut ? $t('已兌完') : $t('積分兌換（{0} 分）', [p.points_required]) }}
          </button>
        </div>
      </div>
    </div>

    <!-- 描述區 -->
    <div v-if="p.description && p.product_kind !== 'sms_channel'" class="pd-section pd-details">
      <h3 class="pd-section-title">{{ $t('商品詳情') }}</h3>
      <div ref="description" class="pd-desc" @click="openDescriptionImage" @keydown="descriptionKeydown" v-html="p.description"></div>
    </div>

    <!-- 評價區（template.show_reviews 開關控制；默認展示 3 條可展開） -->
    <div v-if="showReviews" id="pd-reviews" class="pd-section">
      <h3 class="pd-section-title">{{ $t('用戶評價（{0}）', [p.reviews?.length || 0]) }}</h3>
      <p v-if="!p.reviews?.length" class="muted">{{ $t('暫無評價，歡迎購買後分享使用體驗。') }}</p>
      <p class="muted"><router-link to="/member?tab=orders">{{ $t('前往我的訂單評價') }}</router-link> {{ $t('· 發貨完成後可提交，審核通過後展示。') }}</p>
      <div v-for="r in visibleReviews" :key="`${r.is_virtual}-${r.id}`" class="pd-review">
        <span class="pd-avatar">{{ (r.nickname || $t('匿'))[0] }}</span>
        <div class="pd-review-body">
          <div class="pd-review-head">
            <b>{{ r.nickname || $t('匿名用戶') }}</b>
            <span class="pd-stars">{{ '★'.repeat(Math.min(5, r.rating)) }}</span>
          </div>
          <div class="pd-review-content">{{ r.content }}</div>
        </div>
      </div>
      <button v-if="(p.reviews?.length || 0) > reviewCollapsed" class="pd-review-more" @click="reviewsExpanded = !reviewsExpanded">
        {{ reviewsExpanded ? $t('收起評價 ↑') : $t('查看全部 {0} 條評價 ↓', [(p.reviews?.length || 0)]) }}
      </button>
    </div>

    <ImageViewer v-if="previewImages.length" :images="previewImages" :initial-index="previewIndex" @close="previewImages = []" />
  </div>
  <div v-else-if="error" class="error">{{ uiText(error) }}</div>
  <div v-else class="muted" style="text-align: center; padding: 40px;">{{ $t('加載中…') }}</div>
 <ShippingCheckout ref="shippingCheckout" />
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag, locale } from '@/i18n';

import ShippingCheckout from "@/components/ShippingCheckout.vue";
const shippingCheckout=ref<InstanceType<typeof ShippingCheckout>>();
import { loadPublicConfig } from '@/config';
import OrderFields from '@/components/OrderFields.vue';
import { useContentVideos } from "@/composables/useContentVideos";
import { useFlashOffers } from '@/composables/flash-offers';
import ThemeIcon from '@/components/ThemeIcon.vue';
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import SMSProduct from '@/components/SMSProduct.vue';
import SocialProduct from '@/components/SocialProduct.vue';
import { getProduct, createOrder, rememberOrderPassword, fetchTradeConfig, contactRequiredLabel, contactValid, type Product, type TradeConfig } from '@/api';
import { formatMoney, getToken, getBaseCurrency, initCurrency } from '@/api/client';
import { NO_IMAGE, onImgError } from '@/no-image';
import { authState } from '@/auth';
import { addToCart as addToCartStore, removeCartItem, cartItemOf, cartEnabled } from '@/cart';
import { getRefCode } from '@/ref';
import { fetchCaptchaConfig, type CaptchaConfig } from '@/api';
import { fetchSiteSeo, applySeo, stripHtml, truncate, type SiteSeoConfig } from '@/seo';
import ImageViewer from '@/components/ImageViewer.vue';
import CaptchaInput from '@/components/CaptchaInput.vue';
import { useSalesVisibility } from '@/composables/sales-visibility';

const route = useRoute();
const router = useRouter();
const p = ref<Product | null>(null);
const quantity = ref(1);
const smsProduct=computed(()=>p.value?.delivery_kind==='sms_activation');
watch(smsProduct,v=>{if(v){quantity.value=1;couponCode.value='';selectedSku.value=0;}});
const selectedSku = ref(0);
const queryPassword = ref('');
const contact = ref('');
const couponCode = ref('');
const reusableDelivery = computed(() => { const sku=p.value?.skus?.find(s => Number(s.id)===Number(selectedSku.value)); return (sku?.fulfillment_mode && sku.fulfillment_mode!=="follow"?sku.fulfillment_mode:p.value?.fulfillment_mode)==="reuse"; });
watch(reusableDelivery,v=>{if(v)quantity.value=1});
const manualDelivery = computed(() => { const sku=p.value?.skus?.find(s => s.id === selectedSku.value); return (sku?.fulfillment_mode && sku.fulfillment_mode !== "follow" ? sku.fulfillment_mode : p.value?.fulfillment_mode) === "manual"; });
const controlAnswers = ref<Record<string, string>>({});
const submitting = ref(false);
const error = ref('');
const addingCart = ref(false);
const removingCart = ref(false);
const description = ref<HTMLElement | null>(null);
useContentVideos(description);
const previewImages = ref<{ src: string; alt: string }[]>([]);
const previewIndex = ref(0);
function openCover() {
  if (!p.value?.cover) return;
  previewIndex.value = 0;
  previewImages.value = [{ src: p.value.cover, alt: p.value.name }];
}
function openDescriptionImage(event: Event) {
  if (!(event.target instanceof HTMLImageElement)) return;
  const images = [...(description.value?.querySelectorAll('img') || [])].filter(img => img.currentSrc || img.src);
  const index = images.indexOf(event.target);
  if (index < 0) return;
  event.preventDefault(); // 圖片包在鏈接內時，預覽優先，避免跳離教程。
  event.target.focus({ preventScroll: true });
  previewIndex.value = index;
  previewImages.value = images.map(img => ({ src: img.currentSrc || img.src, alt: img.alt || p.value?.name || $t("商品詳情圖片") }));
}
function descriptionKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' || event.key === ' ') openDescriptionImage(event);
}
watch([description, () => p.value?.description], () => {
  description.value?.querySelectorAll('img').forEach(img => {
    img.tabIndex = 0;
    img.setAttribute('role', 'button');
    img.setAttribute('aria-label', $t("放大查看{0}", [img.alt || $t('商品詳情圖片')]));
  });
}, { flush: 'post' });
watch(() => route.params, () => { previewImages.value = []; });

// 購物車（淘寶式切換）：當前商品 + 所選 SKU 在購物車 → 按鈕變灰「移除購物車」
const canCart = computed(() => !smsProduct.value && cartEnabled.value && !(p.value?.points_required && p.value.points_required > 0));
const inCartNow = computed(() => {
  if (!p.value || !canCart.value) return false;
  return !!cartItemOf(p.value.id, selectedSku.value || 0);
});
const cartBusy = computed(() => addingCart.value || removingCart.value);

// 當前顯示價（跟隨所選 SKU）
const flash = useFlashOffers();
const selectedFlash = computed(() => flash.active((selectedSku.value ? p.value?.skus?.find(s => Number(s.id) === selectedSku.value)?.flash_sale : p.value?.flash_sale)));
const flashSoldOut = computed(() => !!selectedFlash.value && (selectedFlash.value.remaining || 0) <= 0);
const basePrice = computed(() => {
  if (!p.value) return 0;
  if (selectedSku.value) {
    const sku = p.value.skus?.find((s) => Number(s.id) === selectedSku.value);
    if (sku && sku.price_cents) return sku.price_cents;
  }
  return p.value.price_cents;
});

const displayPrice = computed(() => flash.price(basePrice.value, selectedFlash.value));

const stockDisplay = computed(() => {
  if (!p.value) return '-';
  const s = effectiveStock.value;
  if (s < -1) return p.value.stock_status === 'stale' && (p.value.stock_reference ?? -2) >= -1 ? $t("上次：{0}（待確認）", [p.value.stock_reference === -1 ? $t('不限') : p.value.stock_reference]) : $t("待確認");
  if (s === -1) return $t("不限");
  return String(s);
});
// 自營/對接均使用各自庫存；proto3 省略零值時仍按缺貨處理。
const effectiveStock = computed(()=>{if(manualDelivery.value)return p.value?.manual_stock ?? p.value?.stock ?? 0;const sku=p.value?.skus?.find(s=>Number(s.id)===Number(selectedSku.value));return sku && sku.stock !== -2 ? (sku.stock ?? 0) : (p.value?.stock ?? 0);});
const stockUnknown = computed(() => effectiveStock.value < -1);
const stockRefreshing = ref(false);
const stockRefreshMessage = ref('');
const stockRetrySeconds = ref(0);
let stockRetryTimer: ReturnType<typeof setInterval> | undefined;
function startStockCooldown() {
  if (stockRetryTimer) clearInterval(stockRetryTimer);
  stockRetrySeconds.value = 15;
  stockRetryTimer = setInterval(() => {
    stockRetrySeconds.value = Math.max(0, stockRetrySeconds.value - 1);
    if (!stockRetrySeconds.value) { clearInterval(stockRetryTimer); stockRetryTimer = undefined; }
  }, 1000);
}
onUnmounted(() => clearInterval(stockRetryTimer));
async function refreshStock() {
  if (!p.value || stockRefreshing.value || stockRetrySeconds.value > 0) return;
  const productID = p.value.id;
  stockRefreshing.value = true;
  stockRefreshMessage.value = '';
  try {
    const fresh = await getProduct(productID);
    if (p.value?.id !== productID) return;
    if (fresh.data) {
      // 只更新庫存，保留用戶已填寫的規格、數量和取貨信息。
      p.value.stock = fresh.data.stock ?? 0;
      p.value.stock_status = fresh.data.stock_status;
      p.value.stock_reference = fresh.data.stock_reference;
      p.value.stock_checked_at = fresh.data.stock_checked_at;
    }
    if (fresh.error || stockUnknown.value) {
      stockRefreshMessage.value = $t("仍未獲取到有效庫存，請稍後重試，或聯繫客服確認。");
      startStockCooldown();
    }
  } finally {
    stockRefreshing.value = false;
  }
}
const soldOut = computed(() => {
  if (!p.value) return false;
  return effectiveStock.value === 0;
});

// ── 評價（template.show_reviews 後台開關；入口錨點 + 摺疊展開）──
const showReviews = ref(false);
const reviewsExpanded = ref(false);
// 銷量在配置確認前隱藏；庫存沿用原有開關。
const { showSales, applySalesConfig } = useSalesVisibility();
const showStock = ref(false);
const physicalProduct = computed(() => (p.value?.product_property || p.value?.goods_type) === 'physical');
const stockShown = computed(() => showStock.value && p.value?.stock_visible !== false && (!physicalProduct.value || p.value?.track_inventory !== false));
const salesShown = computed(() => showSales.value && p.value?.sales_visible !== false);
const reviewCollapsed = 3;
const visibleReviews = computed(() =>
  reviewsExpanded.value ? (p.value?.reviews || []) : (p.value?.reviews || []).slice(0, reviewCollapsed),
);
const reviewAvg = computed(() => {
  const list = p.value?.reviews || [];
  if (!list.length) return '—';
  return (list.reduce((s, r) => s + (r.rating || 5), 0) / list.length).toFixed(1);
});
function scrollToReviews() {
  document.getElementById('pd-reviews')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
}
const stockPct = computed(() => {
  const s = effectiveStock.value;
  if (s == null || s <= 0) return '4%';
  return `${Math.min(100, Math.round((s / 600) * 100))}%`;
});

// 購物車：積分商品不可加購（積分單走兌換按鈕）；遊客加購進本地購物車（老項目同款）

async function addToCart() {
  if (!p.value || soldOut.value || stockUnknown.value) return;
  addingCart.value = true;
  error.value = '';
  const { error: err } = await addToCartStore({ ...p.value, max_quantity:reusableDelivery.value?1:99, stock:effectiveStock.value, price_cents: basePrice.value, flash_sale: selectedFlash.value }, quantity.value, selectedSku.value || 0);
  addingCart.value = false;
  if (err) {
    error.value = err;
    const fresh = await getProduct(p.value.id);
    if (fresh.data) p.value = fresh.data;
    return;
  }
  // 成功後按鈕經 cartState 響應式切換為「移除購物車」（無臨時態）
}

async function removeFromCart() {
  if (!p.value) return;
  const item = cartItemOf(p.value.id, selectedSku.value || 0);
  if (!item) return;
  removingCart.value = true;
  const { error: err } = await removeCartItem(item.id);
  removingCart.value = false;
  if (err) { error.value = err; return; }
}

function stockTypeLabel(t: string) {
  return ({ get card() { return $t("卡密"); }, get url() { return $t("鏈接"); }, get code() { return $t("兌換碼"); } } as Record<string, string>)[t] || t;
}

// 手機端吸底操作欄點擊後，表單校驗失敗要能看到錯誤提示：滾動到購買卡內的 error
function scrollToFormError() {
  requestAnimationFrame(() => {
    document.querySelector('.pd-buy .error')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  });
}

function toggleCheck(id: number, val: string) {
  const key = String(id);
  const cur = (controlAnswers.value[key] || '').split(',').filter(Boolean);
  const idx = cur.indexOf(val);
  if (idx >= 0) cur.splice(idx, 1);
  else cur.push(val);
  controlAnswers.value[key] = cur.join(',');
}

// 交易設置（查詢密碼強制 / 遊客聯繫方式）；遊客判定（authState 響應式——
// 登出/登錄後自動切換字段顯隱）
const trade = ref<TradeConfig>({ queryPasswordRequired: true, contactRequired: 'any' });
const captchaCfg = ref<CaptchaConfig>({ login: false, register: true, order: false, reset: true });
const captchaId = ref('');
const captchaCode = ref('');
const captchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null);
const isGuest = computed(() => !authState.loggedIn);

// 商品數據預取（setup 頂層：SSG 靜態化商品頁內容 + 輸出 SEO head）
const productResp = await getProduct(Number(route.params.id));
if (productResp.error) {
  error.value = productResp.error;
} else {
  p.value = productResp.data;
  selectedSku.value = Number(productResp.data?.skus?.[0]?.id || 0);
  if (productResp.data) applyProductSeo(productResp.data);
}

onMounted(async () => {
  // 交易配置/驗證碼（客戶端交互能力；SSG 不需要）+ 評價/銷量/庫存顯示開關
  const [cfg, capCfg, tplResp] = await Promise.all([
    fetchTradeConfig(),
    fetchCaptchaConfig(),
    loadPublicConfig(true).catch(() => null),
  ]);
  captchaCfg.value = capCfg;
  trade.value = cfg;
  const pick = (key: string) => tplResp?.entries?.find((e: any) => e.key === key)?.value_json;
  const parseFlag = (raw: string | undefined) => {
    if (raw === undefined) return true;
    try { return JSON.parse(raw) !== false; } catch { return true; }
  };
  showReviews.value = Array.isArray(tplResp?.entries) && parseFlag(pick('template.show_reviews'));
  applySalesConfig(tplResp);
  showStock.value = Array.isArray(tplResp?.entries) && parseFlag(pick('template.show_stock'));
});

/** 商品頁 SEO：title/description/keywords/canonical/og + Product/Breadcrumb JSON-LD */
watch(locale, () => { if (p.value) void applyProductSeo(p.value); });
async function applyProductSeo(product: Product) {
  await initCurrency();
  const site = await fetchSiteSeo();
  const base = site.url || (typeof window !== "undefined" ? window.location.origin : "");
  const desc = truncate(stripHtml(product.description || ''));
  const origin = typeof window !== "undefined" ? window.location.origin : site.url;
  applySeo(
    {
      title: `${product.name} - ${site.name}`,
      description: desc || undefined,
      keywords: [product.name, site.name].filter(Boolean).join(','),
      canonical: `${origin}/product/${product.id}`,
      ogType: 'product',
      ogImage: product.cover || undefined,
      jsonLd: [
        {
          '@context': 'https://schema.org',
          '@type': 'Product',
          name: product.name,
          image: product.cover || undefined,
          description: desc || undefined,
          ...(product.price_cents > 0
            ? {
                offers: {
                  '@type': 'Offer',
                  price: (product.price_cents / 100).toFixed(2),
                  priceCurrency: getBaseCurrency().code,
                  availability: 'https://schema.org/InStock',
                },
              }
            : {}),
        },
        {
          '@context': 'https://schema.org',
          '@type': 'BreadcrumbList',
          itemListElement: [
            { '@type': 'ListItem', position: 1, name: site.name, item: base + '/' },
            { '@type': 'ListItem', position: 2, get name() { return $t("全部商品"); }, item: base + '/products' },
            { '@type': 'ListItem', position: 3, name: product.name, item: `${base}/product/${product.id}` },
          ],
        },
      ],
    },
    site,
  );
}

/** 下單前校驗（與後端 validateTradeRequirements 同口徑） */
function validateTradeFields(requireContact = true): boolean {
  if (physicalProduct.value) {
    if(isGuest.value && !contactValid(contact.value.trim(),'email')) { error.value=$t('請填寫有效郵箱，用於訂單通知與找回'); return false; }
    return true;
  }
  if (!physicalProduct.value && trade.value.queryPasswordRequired && queryPassword.value.trim().length < 4) {
    error.value = $t("請設置查詢密碼（至少 4 位，取貨時使用）");
    return false;
  }
  if (requireContact && (isGuest.value || trade.value.contactScope === 'all') && trade.value.contactRequired !== 'none') {
    if (!contact.value.trim()) {
      error.value = $t("請填寫聯繫方式（{0}），用於訂單查詢與售後", [contactRequiredLabel(trade.value.contactRequired)]);
      return false;
    }
    if (!contactValid(contact.value, trade.value.contactRequired)) {
      error.value = $t("聯繫方式格式不符（需要{0}）", [contactRequiredLabel(trade.value.contactRequired)]);
      return false;
    }
  }
  return true;
}

async function buy() {
  if (!p.value || soldOut.value || stockUnknown.value) return;
  // 交易設置校驗（查詢密碼 / 遊客聯繫方式）
  if (!validateTradeFields()) { scrollToFormError(); return; }
  // 必填控件校驗（proto3 空數組省略 → undefined 兜底空數組）
  for (const c of p.value.controls || []) {
    if (c.required && !controlAnswers.value[String(c.id)]?.trim()) {
      error.value = $t("請填寫「{0}」", [c.name]);
      scrollToFormError();
      return;
    }
  }
  submitting.value = true;
  error.value = '';
  const input = {
    items: [{ product_id: p.value.id, sku_id: selectedSku.value || undefined, quantity: quantity.value, control_answers:controlAnswers.value }],
    query_password: physicalProduct.value ? undefined : queryPassword.value || undefined,
    contact: contact.value || undefined,
    coupon_code: couponCode.value || undefined,
    ref_code: getRefCode() || undefined,
    captcha_id: (isGuest.value && captchaCfg.value.order) ? captchaId.value : undefined,
    captcha_code: (isGuest.value && captchaCfg.value.order) ? captchaCode.value : undefined,
  };
  const {data,error:err}=physicalProduct.value
   ? {data:await shippingCheckout.value!.open(input,p.value.shipping_countries || []),error:null}
   : await createOrder(input);
  submitting.value = false;
  if (err) { error.value = err; return; }
  if(!data)return;
  rememberOrderPassword(data!.order_no, queryPassword.value); // 支付成功自動取貨用
  router.push(`/payment/${data!.order_no}`);
}

// 積分兌換（：use_points → 服務端同事務扣積分 → 訂單直落 paid → 取貨頁交付）
async function exchangePoints() {
  if (!p.value || soldOut.value || stockUnknown.value) return;
  if (!getToken()) {
    router.push({ path: '/login', query: { redirect: route.fullPath } });
    return;
  }
  if (!validateTradeFields(false)) return;
  if (!queryPassword.value || queryPassword.value.length < 4) {
    error.value = $t("積分兌換同樣需要設置查詢密碼（取貨用，至少 4 位）");
    return;
  }
  if (!confirm($t("確認用 {0} 積分兌換本商品？", [p.value.points_required]))) return;
  submitting.value = true;
  error.value = '';
  const { data, error: err } = await createOrder({
    items: [{ product_id: p.value.id, sku_id:selectedSku.value || undefined, quantity: 1, control_answers:controlAnswers.value }],
    query_password: queryPassword.value,
    use_points: true
  });
  submitting.value = false;
  if (err) { error.value = err; return; }
  alert($t("兌換成功，訂單已支付，請前往取貨頁領取"));
  router.push('/fetch');
}
</script>

<style scoped>
.pd-flash-info { margin:8px 0 0; color:#b91c1c; font-size:13px; line-height:1.7; overflow-wrap:anywhere; }
.pd-page { max-width: 1024px; margin: 0 auto; padding: 0 24px; display: flex; flex-direction: column; gap: 20px; }
.pd-crumb { font-size: 13px; color: #9ca3af; display: flex; gap: 6px; align-items: center; }
.pd-crumb a { color: #6b7280; text-decoration: none; }
.pd-crumb a:hover { color: var(--zc-primary); }
.pd-crumb-current { color: #374151; }

.pd-main { display: grid; grid-template-columns: 1fr; gap: 20px; align-items: start; }
@media (min-width: 768px) { .pd-main { grid-template-columns: 1.05fr 1fr; } }

/* ── 左欄圖 ── */
.pd-cover {
  position: relative; aspect-ratio: 1/1; border-radius: 14px; overflow: hidden;
  background: #f1f5f9; cursor: zoom-in; border: 1px solid #e5e7eb;
}
.pd-cover img { width: 100%; height: 100%; object-fit: cover; }
/* 無圖佔位（SVG data URI）：contain 完整顯示，不提示放大 */
.pd-noimg { object-fit: contain !important; cursor: default; }
.pd-cover-placeholder {
  width: 100%; height: 100%; display: flex; align-items: center; justify-content: center;
  font-size: 56px; font-weight: 700; color: var(--zc-primary-border);
  background: linear-gradient(135deg, var(--zc-primary-soft), var(--zc-primary-tint));
}
.pd-zoom-hint {
  position: absolute; right: 10px; bottom: 10px;
  padding: 4px 10px; border-radius: 999px; font-size: 11px;
  background: rgba(255,255,255,0.85); color: #6b7280;
}

/* ── 右欄購買區（獨立白卡：固定白底，不受全站背景圖影響）── */
.pd-buy {
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 14px;
  padding: 18px 20px;
}
.pd-name { font-size: 20px; font-weight: 700; color: #111827; line-height: 1.4; word-break: break-word; overflow-wrap: anywhere; }
.pd-sub { display: flex; gap: 8px; margin-top: 8px; }
.pd-points-tag { background: #eef2ff; color: #4338ca; }

.pd-price-card {
  margin-top: 14px; padding: 14px 16px; border-radius: 12px;
  background: linear-gradient(135deg, #fff3ed, #fff);
  border: 1px solid rgba(255, 87, 34, 0.25);
}
.pd-price-row { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.pd-price { color: #ff5722; font-size: 28px; font-weight: 800; }
.pd-price-points { color: #4338ca; font-size: 13px; font-weight: 600; }

.pd-stats {
  display: flex; border-top: 1px solid #f3f4f6; border-bottom: 1px solid #f3f4f6;
  margin-top: 14px; padding: 10px 0;
}
.pd-stat { flex: 1; text-align: center; border-right: 1px solid #f3f4f6; }
.pd-stat:last-child { border-right: none; }
.pd-stat b { display: block; font-size: 15px; color: #111827; }
.pd-stat span { font-size: 11px; color: #9ca3af; }

.pd-assure {
  display: flex; gap: 14px; flex-wrap: wrap;
  font-size: 11px; color: #6b7280; padding: 10px 0;
}

.pd-field { margin-top: 12px; }
.pd-label { display: block; font-size: 13px; font-weight: 600; color: #4b5563; margin-bottom: 6px; }
.pd-req { color: #ef4444; }

.pd-input {
  width: 100%; padding: 9px 12px; border: 1px solid #d1d5db; border-radius: 8px;
  font-size: 14px; outline: none; transition: all 0.15s;
}
.pd-input:focus { border-color: var(--zc-primary); box-shadow: 0 0 0 2px color-mix(in srgb, var(--zc-primary) 15%, transparent); }

.pd-skus { display: flex; gap: 8px; flex-wrap: wrap; }
.pd-sku {
  border: 2px solid #e5e7eb; border-radius: 10px; padding: 8px 14px;
  background: #fff; cursor: pointer; text-align: center; transition: all 0.15s;
  min-width: 90px;
  max-width: 100%; /* 超長規格名不撐破容器（配合 name 斷詞） */
}
.pd-sku:hover { border-color: color-mix(in srgb, var(--zc-primary) 40%, transparent); }
.pd-sku.active { border-color: var(--zc-primary); background: var(--zc-primary-soft); }
.pd-sku-name { display: block; font-size: 13px; font-weight: 600; color: #111827; word-break: break-word; overflow-wrap: anywhere; }
.pd-sku-price { display: block; font-size: 12px; color: #ff5722; margin-top: 2px; }

.pd-qty { display: inline-flex; border: 1px solid #d1d5db; border-radius: 8px; overflow: hidden; }
.pd-qty-btn {
  width: 36px; height: 36px; border: none; background: #f8fafc; cursor: pointer;
  font-size: 16px; color: #374151; transition: all 0.15s;
}
.pd-qty-btn:hover { background: var(--zc-primary-soft); color: var(--zc-primary); }
.pd-qty-input {
  width: 56px; height: 36px; border: none; border-left: 1px solid #e5e7eb; border-right: 1px solid #e5e7eb;
  text-align: center; font-size: 14px; outline: none;
}

.pd-stock-track { height: 6px; background: #f1f5f9; border-radius: 999px; overflow: hidden; }
.pd-stock-fill { height: 100%; background: #16a34a; border-radius: 999px; transition: width 0.3s; }

.pd-options { display: flex; gap: 14px; flex-wrap: wrap; }
.pd-option { display: flex; align-items: center; gap: 4px; font-size: 14px; }

.pd-stock-notice { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-top: 16px; padding: 14px; border: 1px solid #f4d58d; border-radius: 10px; background: #fff9eb; color: #775015; }
.pd-stock-notice > div { flex: 1; min-width: 180px; }
.pd-stock-notice p { margin: 5px 0 0; font-size: 13px; line-height: 1.6; }
.pd-stock-notice button { min-height: 44px; padding: 8px 12px; border: 1px solid #b98d39; border-radius: 8px; background: #fff; color: inherit; cursor: pointer; font: inherit; white-space: nowrap; }
.pd-stock-notice button:disabled { opacity: 0.6; cursor: wait; }
.pd-actions { display: flex; gap: 10px; margin-top: 18px; flex-wrap: wrap; }
.pd-btn-buy {
  flex: 1; min-width: 140px; padding: 12px 0; border: none; cursor: pointer;
  border-radius: 10px; font-size: 15px; font-weight: 700; color: #fff;
  background: linear-gradient(90deg, var(--zc-primary), var(--zc-primary-hover));
  box-shadow: 0 4px 12px color-mix(in srgb, var(--zc-primary) 30%, transparent); transition: all 0.15s;
}
.pd-btn-buy:hover:not(:disabled) { box-shadow: 0 6px 18px color-mix(in srgb, var(--zc-primary) 40%, transparent); transform: translateY(-1px); }
.pd-btn-cart {
  display: inline-flex; align-items: center; justify-content: center; gap: 8px;
  flex: 1; min-width: 140px; padding: 12px 0; cursor: pointer;
  border-radius: 10px; font-size: 15px; font-weight: 700;
  background: #fff; color: var(--zc-primary); border: 2px solid var(--zc-primary); transition: all 0.15s;
}
.pd-btn-cart:hover:not(:disabled) { background: var(--zc-primary-soft); }
/* 已在購物車：灰色「移除」形態（淘寶式切換） */
.pd-btn-cart.is-in {
  background: #f3f4f6; color: #6b7280; border-color: #d1d5db;
}
.pd-btn-cart.is-in:hover:not(:disabled) {
  background: #e5e7eb; color: #374151; border-color: #9ca3af;
}
.pd-btn-points {
  flex: 1; min-width: 140px; padding: 12px 0; cursor: pointer;
  border-radius: 10px; font-size: 15px; font-weight: 700;
  background: #f3f4f6; color: var(--zc-primary); border: none; transition: all 0.15s;
}
.pd-btn-points:hover:not(:disabled) { background: var(--zc-primary-soft); }
.pd-actions button:disabled { opacity: 0.5; cursor: not-allowed; }

/* ── 描述/評價 ── */
.pd-section { background: #fff; border: 1px solid #e5e7eb; border-radius: 12px; padding: 20px; }
.pd-section-title {
  font-size: 15px; font-weight: 700; color: #111827;
  border-left: 3px solid var(--zc-primary); padding-left: 10px; margin-bottom: 14px;
}
.pd-desc { font-size: 14px; line-height: 1.8; color: #374151; word-break: break-word; overflow-wrap: anywhere; }
.pd-desc :deep(img) { max-width: 100%; border-radius: 8px; }
.pd-desc :deep(p) { margin: 8px 0; }
.pd-desc :deep(ol), .pd-desc :deep(ul) { margin: 12px 0; padding-inline-start: 1.75em; list-style-position: outside; }
.pd-desc :deep(li) { padding-inline-start: 0.25em; }
.pd-desc :deep(li + li) { margin-top: 8px; }
.pd-desc :deep(li > ol), .pd-desc :deep(li > ul) { margin: 8px 0; }
.pd-desc > :deep(:first-child) { margin-top: 0; }
.pd-desc > :deep(:last-child) { margin-bottom: 0; }

.pd-review { display: flex; gap: 12px; padding: 12px 0; border-bottom: 1px solid #f3f4f6; }
.pd-review:last-child { border-bottom: none; }
/* 評價入口（評分摘要錨點卡） */
.pd-review-entry {
  display: flex; align-items: center; gap: 10px; width: 100%; margin-top: 12px;
  padding: 10px 14px; border: 1px solid #e5e7eb; border-radius: 10px;
  background: #fafbfc; cursor: pointer; font-family: inherit; font-size: 13px;
  transition: border-color 0.15s, background 0.15s;
}
.pd-review-entry:hover { border-color: color-mix(in srgb, var(--zc-primary) 45%, transparent); background: var(--zc-primary-soft); }
.pd-review-entry-score { font-weight: 700; color: #b45309; }
.pd-review-entry-count { color: #374151; }
.pd-review-entry-go { margin-left: auto; color: var(--zc-primary); font-weight: 600; }
.pd-review-more {
  display: block; width: 100%; margin-top: 10px; padding: 9px 0;
  border: 1px dashed #d1d5db; border-radius: 8px; background: none;
  color: var(--zc-primary); font-size: 13px; font-weight: 600; cursor: pointer; font-family: inherit;
}
.pd-review-more:hover { border-color: var(--zc-primary); background: var(--zc-primary-soft); }
.pd-avatar {
  width: 36px; height: 36px; border-radius: 999px; flex-shrink: 0;
  background: var(--zc-primary-soft); color: var(--zc-primary); font-size: 14px; font-weight: 700;
  display: flex; align-items: center; justify-content: center;
}
.pd-review-head { display: flex; gap: 8px; align-items: center; margin-bottom: 4px; }
.pd-stars { color: #f59e0b; font-size: 12px; }
.pd-review-content { font-size: 13px; color: #4b5563; }

.pd-desc :deep(img) { cursor: zoom-in; }
.pd-desc :deep(img:focus-visible), .pd-cover:focus-visible { outline: 2px solid var(--zc-primary); outline-offset: 3px; }
/* ── 手機端（大廠商品頁範式：無麪包屑、緊湊留白、底部吸底操作欄）── */
@media (max-width: 768px) {
  .pd-page { padding: 0 12px calc(76px + env(safe-area-inset-bottom)); gap: 12px; }
  .pd-crumb { display: none; }
  .pd-buy { padding: 14px; border-radius: 12px; }
  .pd-name { font-size: 17px; }
  .pd-price { font-size: 24px; }
  .pd-price-card { margin-top: 12px; padding: 12px 14px; }
  .pd-section { padding: 14px; border-radius: 12px; }
  .pd-details { padding: 20px; }
  .pd-actions {
    position: fixed; left: 0; right: 0; bottom: 0; z-index: 100;
    margin: 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom));
    background: #fff; border-top: 1px solid #e5e7eb;
    box-shadow: 0 -4px 16px rgba(15, 23, 42, 0.06);
  }
  .pd-actions button { min-width: 0; flex: 1; }
}
</style>
