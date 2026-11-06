<template>
  <div class="cart-page">
    <div v-if="cartSettingLoaded && !cartEnabled" class="cart-empty" role="status">
      <p class="cart-empty-text">{{ cartSettingError || $t('購物車已關閉，請前往商品詳情頁直接購買') }}</p>
      <button v-if="cartSettingError" class="btn secondary" :disabled="retrying" @click="retrySetting">{{ retrying ? $t('讀取中…') : $t('重試') }}</button>
      <router-link class="btn btn-primary" to="/products">{{ $t('瀏覽商品') }}</router-link>
    </div>
    <div v-else-if="!loaded || !cartSettingLoaded" class="card" style="text-align: center;"><span class="muted">{{ $t('加載中…') }}</span></div>

    <template v-else>
      <!-- 空購物車 -->
      <div v-if="!items.length" class="cart-empty">
        <div class="cart-empty-icon"><ThemeIcon name="cart" /></div>
        <div class="cart-empty-text">{{ $t('購物車還是空的') }}</div>
        <router-link class="btn btn-primary" to="/products">{{ $t('去逛逛') }}</router-link>
      </div>

      <template v-else>
        <!-- 全選欄 -->
        <div class="cart-toolbar">
          <label class="cart-check">
            <input type="checkbox" :checked="allSelected" @change="toggleAll" />
            <span>{{ $t('全選') }}</span>
            <span class="muted">{{ $t('（有效') }} {{ validItems.length }} {{ $t('項）') }}</span>
          </label>
          <span class="muted">{{ $t('失效/缺貨商品不可結算') }}</span>
        </div>

        <!-- 商品列表 -->
        <div v-for="it in items" :key="it.id" class="cart-item" :class="{ invalid: !it.valid || (it.stock ?? 0) === 0 || it.stock < -1 }">
          <input type="checkbox" :disabled="!it.valid || (it.stock ?? 0) === 0 || it.stock < -1" v-model="selected" :value="it.id" class="cart-item-check" />
          <router-link v-if="it.valid" :to="`/product/${it.product_id}`" class="cart-item-cover">
            <img v-if="it.product_cover" :src="it.product_cover" :alt="it.product_name" @error="onImgError" />
            <img v-else :src="NO_IMAGE" :alt="it.product_name" style="object-fit: contain;" />
          </router-link>
          <div v-else class="cart-item-cover">
            <img :src="NO_IMAGE" :alt="it.product_name" style="object-fit: contain;" />
          </div>
          <div class="cart-item-info">
            <router-link v-if="it.valid" :to="`/product/${it.product_id}`" class="cart-item-name">{{ it.product_name }}</router-link>
            <span v-else class="cart-item-name">{{ it.product_name }}</span>
            <div class="cart-item-badges">
              <span v-if="!it.valid" class="badge red">{{ $t('已失效') }}</span>
              <span v-else-if="(it.stock ?? 0) === 0 || it.stock < -1" class="badge orange">{{ it.stock < -1 ? $t('庫存待確認') : $t('缺貨') }}</span>
              <span v-if="it.points_only" class="tag">{{ $t('積分商品') }}</span>
            </div>
            <div class="cart-item-sku muted" v-if="it.sku_id">{{ skuNames[it.id] || $t('規格 #{0}', [it.sku_id]) }}</div>
          </div>
          <div class="cart-item-price">
            <div class="cart-price">{{ formatMoney(itemPrice(it)) }}</div>
            <div class="muted">{{ $t('單價') }}</div>
          </div>
          <div class="cart-item-qty">
            <button class="cart-qty-btn" :disabled="(it.stock ?? 0) === 0 || it.stock < -1" @click="changeQty(it, it.quantity - 1)">−</button>
            <input class="cart-qty-input" type="number" min="1" :max="it.max_quantity||99" v-model.number="it.quantity"
                   :disabled="(it.stock ?? 0) === 0 || it.stock < -1" @change="changeQty(it, it.quantity)" />
            <button class="cart-qty-btn" :disabled="(it.stock ?? 0) === 0 || it.stock < -1 || it.quantity >= (it.max_quantity||99)" @click="changeQty(it, it.quantity + 1)">＋</button>
          </div>
          <div class="cart-item-subtotal">
            <div class="cart-subtotal">{{ formatMoney(itemPrice(it) * it.quantity) }}</div>
            <div class="muted">{{ $t('小計') }}</div>
          </div>
          <button class="cart-item-del" @click="remove(it.id)" :title="$t('刪除')">✕</button>
        </div>

        <!-- 結算欄（sticky） -->
        <div class="cart-checkout">
          <div class="cart-total">
            <div class="muted">{{ $t('已選') }} <b>{{ selectedItems.length }}</b> {{ $t('項') }}</div>
            <div class="cart-total-row">
              <span class="muted">{{ $t('合計：') }}</span>
              <span class="cart-total-price">{{ formatMoney(rawTotal) }}</span>
            </div>
            <div class="muted">{{ $t('優惠與實體運費將在結算時核算') }}</div>
          </div>
          <div class="cart-checkout-fields">
            <input v-if="!physicalOnly" v-model="queryPwd" type="text" class="input" :placeholder="trade.queryPasswordRequired ? $t('查詢密碼 *（取貨用，≥4 位）') : $t('查詢密碼（取貨用，≥4 位）')" style="max-width: 170px;" />
            <input v-if="physicalOnly && isGuestCart" v-model="contact" type="email" autocomplete="email" class="input" :placeholder="$t('郵箱（訂單通知與找回）')" />
            <input v-if="!physicalOnly && (isGuestCart || trade.contactScope === 'all') && trade.contactRequired !== 'none'" v-model="contact" type="text" class="input" :placeholder="$t('聯繫方式 *（{0}）', [contactRequiredLabel(trade.contactRequired)])" style="max-width: 170px;" />
            <input v-model="couponCode" type="text" class="input" :placeholder="$t('優惠券碼（選填）')" style="max-width: 150px;" />
            <template v-if="isGuestCart && captchaCfg.order">
              <CaptchaInput ref="captchaRef" @update:code="captchaCode = $event" @update:captcha-id="captchaId = $event" />
            </template>
            <button class="btn btn-primary cart-checkout-btn" :disabled="!selectedItems.length || checkingOut" @click="checkout">
              {{ checkingOut ? $t('結算中…') : $t('去結算（{0}）', [selectedItems.length]) }}
            </button>
          </div>
        </div>

        <!-- 必填控件收集 -->
        <div v-if="controlsNeeded.length && showControls" class="cart-controls">
          <h3 class="cart-controls-title">{{ $t('核對下單資料') }}</h3>
          <div v-for="g in controlsNeeded" :key="g.key" class="cart-controls-group">
            <div class="cart-controls-name">{{ g.name }}</div>
            <OrderFields :controls="g.controls" v-model="controlAnswers[g.key]" :prefix="g.key" />
          </div>
          <button class="btn btn-primary" style="margin-top: 8px;" :disabled="!controlsComplete || checkingOut" @click="doCheckout">
            {{ checkingOut ? $t('提交中…') : $t('確認並下單') }}
          </button>
        </div>
      </template>
    </template>
  </div>
 <ShippingCheckout ref="shippingCheckout" />
</template>

<script setup lang="ts">
import { t as $t } from '@/i18n';

import ShippingCheckout from "@/components/ShippingCheckout.vue";
const shippingCheckout=ref<InstanceType<typeof ShippingCheckout>>();
import OrderFields from '@/components/OrderFields.vue';
import { useFlashOffers } from '@/composables/flash-offers';
const flash=useFlashOffers();
import ThemeIcon from '@/components/ThemeIcon.vue';
import { ref, computed, onMounted, watch } from 'vue';
import { useRouter } from 'vue-router';
import { createOrder, getProduct, updateCart, rememberOrderPassword, fetchTradeConfig, contactRequiredLabel, contactValid, type CartItem, type ProductControl, type TradeConfig } from '@/api';
import { formatMoney } from '@/api/client';
import { loadCart, updateGuestQty, removeCartItem, clearPurchased, cartEnabled, cartSettingLoaded, cartSettingError, refreshCartSetting } from '@/cart';
import { NO_IMAGE, onImgError } from '@/no-image';
import { getRefCode } from '@/ref';
import { fetchCaptchaConfig, type CaptchaConfig } from '@/api';
import CaptchaInput from '@/components/CaptchaInput.vue';
import { cartPriceCents } from '@/utils/commerce';

const router = useRouter();
const items = ref<CartItem[]>([]);
const skuNames = ref<Record<number, string>>({});
const selected = ref<number[]>([]);
const loaded = ref(false);
const retrying = ref(false);
async function retrySetting() {
  retrying.value = true;
  try { if (await refreshCartSetting(true)) await load(); } finally { retrying.value = false; }
}
watch(cartEnabled, enabled => { if (enabled) load(); });
const isGuestCart = ref(false);
const couponCode = ref('');
const contact = ref('');
const trade = ref<TradeConfig>({ queryPasswordRequired: true, contactRequired: 'any' });
const captchaCfg = ref<CaptchaConfig>({ login: false, register: true, order: false, reset: true });
const captchaId = ref('');
const captchaCode = ref('');
const captchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null);
const checkingOut = ref(false);
const showControls = ref(false);
const controlAnswers = ref<Record<string, Record<string,string>>>({});
const controlsNeeded = ref<{ productId: number; key: string; name: string; controls: ProductControl[] }[]>([]);

// 自營/對接都按服務端各自庫存判斷；遊客快照仍由後端下單複核。
const validItems = computed(() => items.value.filter((i) => i.valid && !(flash.active(i.flash_sale) && (i.flash_sale?.remaining || 0) <= 0) && (i.stock ?? 0) !== 0 && i.stock >= -1));
const allSelected = computed(() => validItems.value.length > 0 && selected.value.length === validItems.value.length);
const selectedItems = computed(() => validItems.value.filter((i) => selected.value.includes(i.id)));
const productProperties = ref<Record<number,string>>({});
const physicalOnly = computed(() => selectedItems.value.length > 0 && selectedItems.value.every(i => productProperties.value[i.product_id] === 'physical'));
function itemPrice(item: CartItem) { return flash.price(cartPriceCents(item.price_cents), item.flash_sale); }
const rawTotal = computed(() => selectedItems.value.reduce((s, i) => s + itemPrice(i) * i.quantity, 0));
const controlsComplete = computed(() =>
  controlsNeeded.value.every((g) => g.controls.every((c) => !c.required || (controlAnswers.value[g.key]?.[String(c.id)] || '').trim() !== ''))
);

onMounted(async () => {
  await load();
  const [tc, cc] = await Promise.all([fetchTradeConfig(), fetchCaptchaConfig()]);
  trade.value = tc;
  captchaCfg.value = cc;
});

async function load() {
  const { items: list, error, isGuest } = await loadCart();
  items.value = list || [];
  isGuestCart.value = isGuest;
  selected.value = validItems.value.map((i) => i.id);
  const productIds = [...new Set(items.value.map(i => i.product_id))];
  const details = new Map(await Promise.all(productIds.map(async id => [id, (await getProduct(id)).data] as const)));
  productProperties.value=Object.fromEntries([...details].map(([id,p])=>[id,p?.product_property || p?.goods_type || 'virtual']));
  skuNames.value = Object.fromEntries(items.value.filter(i => i.sku_id).map(i => [
    i.id, details.get(i.product_id)?.skus?.find(s => Number(s.id) === Number(i.sku_id))?.name || '',
  ]));
  loaded.value = true;
}

function toggleAll() {
  selected.value = allSelected.value ? [] : validItems.value.map((i) => i.id);
}

async function changeQty(it: CartItem, qty: number) {
  if (!(await refreshCartSetting(true))) return;
  const q = Math.max(1, Math.min(it.max_quantity||99, qty || 1));
  if (isGuestCart.value) {
    // 遊客本地購物車
    updateGuestQty(it.id, q);
    it.quantity = q;
    return;
  }
  const { error } = await updateCart(it.id, q);
  if (!error) it.quantity = q;
}

async function remove(id: number) {
  const { error } = await removeCartItem(id);
  if (!error) {
    items.value = items.value.filter((i) => i.id !== id);
    selected.value = selected.value.filter((s) => s !== id);
  }
}

// 結算：先收必填控件（有則展開表單），後下單
async function checkout() {
  if (!(await refreshCartSetting(true))) return;
  // 交易設置校驗（與後端同口徑：查詢密碼強制 + 遊客聯繫方式）
  if (!physicalOnly.value && trade.value.queryPasswordRequired && queryPwd.value.trim().length < 4) {
    alert($t("請設置查詢密碼（取貨用，至少 4 位）"));
    return;
  }
  if (physicalOnly.value && isGuestCart.value && !contactValid(contact.value.trim(), 'email')) { alert($t('請填寫有效郵箱，用於訂單通知與找回')); return; }
  if (!physicalOnly.value && (isGuestCart.value || trade.value.contactScope === 'all') && trade.value.contactRequired !== 'none') {
    if (!contact.value.trim()) {
      alert($t("請填寫聯繫方式（{0}），用於訂單查詢與售後", [contactRequiredLabel(trade.value.contactRequired)]));
      return;
    }
    if (!contactValid(contact.value, trade.value.contactRequired)) {
      alert($t("聯繫方式格式不符（需要{0}）", [contactRequiredLabel(trade.value.contactRequired)]));
      return;
    }
  }
  showControls.value = false;
  controlsNeeded.value = [];
  controlAnswers.value = {};
  const needed: typeof controlsNeeded.value = [];
  checkoutCountries.value=null;
  for (const it of selectedItems.value) {
    const { data: p } = await getProduct(it.product_id);
    if (!p) { alert($t("讀取商品填寫信息失敗，請重試")); return; }
 if((p.product_property || p.goods_type)==='physical'){const current:string[]|null=checkoutCountries.value;checkoutCountries.value=current===null?[...(p.shipping_countries || [])]:(current as string[]).filter((c:string)=>(p.shipping_countries || []).includes(c))}
 const req = p.controls || [];
 const key = `${it.product_id}:${it.sku_id || 0}`; controlAnswers.value[key] = {};
    if (req.length) needed.push({ productId: it.product_id, key, name: `${p.name} ${p.skus?.find(s => Number(s.id) === Number(it.sku_id))?.name || ""}`, controls: req });
  }
  if (needed.length) {
    controlsNeeded.value = needed;
    showControls.value = true;
    return;
  }
  await doCheckout();
}

const checkoutCountries=ref<string[]|null>(null);
async function doCheckout() {
  if (checkingOut.value) return;
  checkingOut.value = true;
  try {
    if (!(await refreshCartSetting(true))) return;
    if (!physicalOnly.value && checkoutCountries.value !== null && isGuestCart.value && queryPwd.value.trim().length < 4) {
      alert($t("遊客購買實體商品須設置至少4位查詢密碼"));
      return;
    }
    const purchasedIds = selectedItems.value.map(i => i.id);
    const input = {
      items: selectedItems.value.map((i) => ({ product_id: i.product_id, sku_id: i.sku_id || undefined, quantity: i.quantity, control_answers:controlAnswers.value[`${i.product_id}:${i.sku_id || 0}`] })),
      coupon_code: couponCode.value || undefined,
      query_password: physicalOnly.value ? undefined : queryPwd.value,
      ref_code: getRefCode() || undefined,
      captcha_id: (isGuestCart.value && captchaCfg.value.order) ? captchaId.value : undefined,
      captcha_code: (isGuestCart.value && captchaCfg.value.order) ? captchaCode.value : undefined,
      contact: contact.value.trim() || undefined,
    };
    const { data, error } = checkoutCountries.value !== null
      ? { data: await shippingCheckout.value!.open(input, checkoutCountries.value), error: null }
      : await createOrder(input);
    if (!data && !error) return;
    if (error || !data) {
      alert(error || $t("下單失敗"));
      await load();
      return;
    }
    // 下單成功後移除本次提交的商品，避免等待期間選擇變化刪掉其他商品。
    await clearPurchased(purchasedIds);
    rememberOrderPassword(data.order_no, queryPwd.value); // 支付成功自動取貨用
    router.push(`/payment/${data.order_no}`);
  } catch (cause) {
    alert(cause instanceof Error ? cause.message : $t("下單失敗，請重試"));
  } finally {
    checkingOut.value = false;
  }
}

// 查詢密碼（多商品合併單共用一個取貨密碼；結算欄輸入）
const queryPwd = ref('');
</script>

<style scoped>
.cart-page { max-width: 860px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }

/* ── 空態 ── */
.cart-empty {
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 60px 20px; text-align: center;
}
.cart-empty-icon { font-size: 48px; color: var(--zc-primary); opacity: 0.65; margin-bottom: 12px; }
.cart-empty-text { color: #6b7280; font-size: 15px; margin-bottom: 16px; }

/* ── 工具欄 ── */
.cart-toolbar {
  display: flex; align-items: center; justify-content: space-between;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 12px 16px;
}
.cart-check { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; }

/* ── 商品行 ── */
.cart-item {
  display: flex; align-items: center; gap: 14px;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 14px 16px;
}
.cart-item.invalid { opacity: 0.55; }
.cart-item-check { width: 16px; height: 16px; flex-shrink: 0; }
.cart-item-cover {
  width: 56px; height: 56px; border-radius: 10px; overflow: hidden; flex-shrink: 0;
  background: linear-gradient(135deg, var(--zc-primary-soft), var(--zc-primary-tint));
  display: flex; align-items: center; justify-content: center;
  text-decoration: none;
}
.cart-item-cover img { width: 100%; height: 100%; object-fit: cover; display: block; }
.cart-item-ph { font-size: 20px; font-weight: 700; color: var(--zc-primary-muted); }
.cart-item-info { flex: 1; min-width: 0; }
.cart-item-name {
  font-size: 14px; font-weight: 600; color: #111827; text-decoration: none;
  display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.cart-item-name:hover { color: var(--zc-primary); }
.cart-item-badges { display: flex; gap: 6px; margin-top: 4px; }
.cart-item-sku { margin-top: 2px; overflow-wrap: anywhere; }
.cart-item-price, .cart-item-subtotal { text-align: center; width: 76px; flex-shrink: 0; }
.cart-price { color: #ff5722; font-weight: 700; font-size: 14px; }
.cart-subtotal { font-weight: 700; font-size: 14px; color: #111827; }

.cart-item-qty { display: inline-flex; border: 1px solid #d1d5db; border-radius: 8px; overflow: hidden; flex-shrink: 0; }
.cart-qty-btn {
  width: 30px; height: 32px; border: none; background: #f8fafc; cursor: pointer;
  font-size: 14px; color: #374151;
}
.cart-qty-btn:hover:not(:disabled) { background: var(--zc-primary-soft); color: var(--zc-primary); }
.cart-qty-input {
  width: 48px; height: 32px; border: none; border-left: 1px solid #e5e7eb; border-right: 1px solid #e5e7eb;
  text-align: center; font-size: 13px; outline: none;
}

.cart-item-del {
  width: 28px; height: 28px; border: none; background: none; cursor: pointer; flex-shrink: 0;
  border-radius: 999px; color: #9ca3af; font-size: 13px; transition: all 0.15s;
}
.cart-item-del:hover { background: #fee2e2; color: #ef4444; }

/* ── 結算欄 ── */
.cart-checkout {
  display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 14px 18px;
  position: sticky; bottom: 12px;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.08);
}
.cart-total-row { display: flex; align-items: baseline; gap: 6px; margin-top: 4px; }
.cart-total-price { color: #ff5722; font-size: 22px; font-weight: 800; }
.cart-checkout-fields { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cart-checkout-btn { padding: 10px 22px; font-weight: 700; }

/* ── 控件收集 ── */
.cart-controls {
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px; padding: 18px;
}
.cart-controls-title { font-size: 15px; font-weight: 700; margin-bottom: 12px; }
.cart-controls-group { border-bottom: 1px solid #f3f4f6; padding: 10px 0; }
.cart-controls-group:last-child { border-bottom: none; }
.cart-controls-name { font-weight: 600; font-size: 13px; margin-bottom: 6px; }

/* ── 移動端：商品行兩行化（第一行 勾選+封面+名稱，第二行 單價+數量+小計+刪除）── */
@media (max-width: 768px) {
  .cart-item { flex-wrap: wrap; row-gap: 10px; gap: 10px; padding: 12px; }
  /* 第一行佔滿剩餘寬，把價格組擠到第二行 */
  .cart-item-info { flex: 1 1 calc(100% - 16px - 56px - 40px); }
  .cart-item-price { order: 1; width: auto; text-align: left; }
  .cart-item-qty { order: 2; margin-left: auto; }
  .cart-item-subtotal { order: 3; width: auto; text-align: right; }
  .cart-item-del { order: 4; }
  /* 結算欄：合計一行、輸入與按鈕整行鋪滿（sticky 條不再擠壓） */
  .cart-checkout { padding: 12px; }
  .cart-checkout-fields { width: 100%; }
  .cart-checkout-fields .input { max-width: none !important; flex: 1 1 150px; min-width: 0; }
  .cart-checkout-btn { flex: 1 1 100%; }
  .cart-controls { padding: 14px; }
}
</style>
