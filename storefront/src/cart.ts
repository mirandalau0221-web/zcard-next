import { t as $t } from '@/i18n';
// 購物車抽象：登錄用戶走後端 API（服務端存儲）；遊客走 localStorage 本地購物車
// （老項目同款方案——遊客可加購/結算/下單，僅支付時無餘額渠道）。
// 登錄後自動把本地購物車合併到後端並清空本地。

import { ref } from 'vue';
import type { FlashOffer } from './api';
import { listCart, addCart, updateCart, removeCart } from './api';
import { api, getToken } from './api/client';
import { cartPriceCents } from './utils/commerce';

const GUEST_KEY = 'zcard_guest_cart';

// 所有購物車入口共享同一開關。配置未確認/讀取失敗時不展示入口。
export const cartEnabled = ref(false);
export const cartSettingLoaded = ref(false);
export const cartSettingError = ref('');
let settingRequest: Promise<boolean> | null = null;
export async function refreshCartSetting(force = false): Promise<boolean> {
  if (settingRequest) return settingRequest;
  if (cartSettingLoaded.value && !force) return cartEnabled.value;
  settingRequest = (async () => {
    try {
      const { data, error } = await api.get<{ entries: { key: string; value_json: string }[] }>('/config');
      if (error || !Array.isArray(data?.entries)) throw new Error('config unavailable');
      const entry = data.entries.find(e => e.key === 'trade.cart_enabled');
      const enabled = entry ? JSON.parse(entry.value_json) : true;
      if (typeof enabled !== 'boolean') throw new Error('invalid cart setting');
      cartEnabled.value = enabled;
      cartSettingError.value = '';
    } catch {
      cartEnabled.value = false;
      cartSettingError.value = $t("購物車配置暫時無法讀取，請重試");
    } finally {
      cartSettingLoaded.value = true;
      if (!cartEnabled.value) syncCartState([]);
    }
    return cartEnabled.value;
  })();
  try { return await settingRequest; } finally { settingRequest = null; }
}
function unavailableMessage() {
  return cartSettingError.value || $t("購物車已關閉，請在商品詳情頁直接購買");
}


// ── 共享響應式狀態（單一數據源）──
// 導航購物車角標 + 商品頁「已在購物車 → 移除購物車」判定都讀這裏；
// 任何 loadCart/加購/刪改操作後自動同步。
export interface CartSnapshotItem {
  id: number;
  product_id: number;
  sku_id: number;
  quantity: number;
}
export const cartState = ref<{ count: number; items: CartSnapshotItem[] }>({ count: 0, items: [] });

function syncCartState(items: CartSnapshotItem[]) {
  cartState.value = {
    count: items.reduce((s, i) => s + i.quantity, 0),
    items: items.map((i) => ({ id: i.id, product_id: i.product_id, sku_id: i.sku_id || 0, quantity: i.quantity })),
  };
}

/** 刷新共享狀態（不返回給調用方數據——純狀態同步；加購/刪除後調用） */
export async function refreshCartState() {
  if (!(await refreshCartSetting())) return;
  if (getToken()) {
    const { data, error } = await listCart().catch(() => ({ data: null, error: 'network' }));
    if (!error && data) {
      syncCartState(data.items || []);
      return;
    }
  }
  syncCartState(guestItems());
}

/** 按商品 + SKU 查購物車項（無則 undefined）——商品頁「移除購物車」用 */
export function cartItemOf(productId: number, skuId = 0) {
  return cartState.value.items.find((i) => i.product_id === productId && i.sku_id === (skuId || 0));
}

/** 商品（+SKU）是否已在購物車 */
export function inCart(productId: number, skuId = 0) {
  return !!cartItemOf(productId, skuId);
}

interface GuestCartItem {
  max_quantity?: number;
  flash_sale?: FlashOffer;
  id: number;
  product_id: number;
  sku_id: number;
  quantity: number;
  product_name: string;
  price_cents: number;
  stock: number;
  points_only: boolean;
  points_required: number;
  valid: boolean;
  added_at: number;
}

function guestItems(): GuestCartItem[] {
  try {
    const raw = JSON.parse(localStorage.getItem(GUEST_KEY) || '[]');
    return Array.isArray(raw) ? raw : [];
  } catch {
    return [];
  }
}

function saveGuest(items: GuestCartItem[]) {
  try {
    localStorage.setItem(GUEST_KEY, JSON.stringify(items));
  } catch { /* 存儲不可用：本地購物車降級為空 */ }
}

/** 401 判定（錯誤串兼容 reason/message 兩種格式） */
function isAuthError(error: string | null, status?: number): boolean {
  return status === 401 || !!error && (error.includes('401') || error.includes('UNAUTHORIZED') || error.includes('未登錄'));
}

/** 加載購物車（登錄 → 後端；遊客/令牌失效 → 本地；同時同步共享角標狀態） */
export async function loadCart() {
  if (!(await refreshCartSetting())) return { items: [], error: unavailableMessage(), isGuest: !getToken() };
  if (getToken()) {
    const { data, error, status } = await listCart();
    if (isAuthError(error, status)) {
      // token 過期/失效：降級遊客本地購物車（真實遊客場景）
      syncCartState(guestItems());
      return { items: guestItems(), error: '', isGuest: true };
    }
    syncCartState(data?.items || []);
    return { items: data?.items || [], error, isGuest: false };
  }
  syncCartState(guestItems());
  return { items: guestItems(), error: '', isGuest: true };
}

/** 加購（登錄 → 後端；遊客/令牌失效 → 本地，同商品同 SKU 合併數量）；成功後同步角標 */
export async function addToCart(product: { id: number; name: string; price_cents: number; points_required?: number; stock?: number; max_quantity?:number; flash_sale?: FlashOffer }, quantity: number, skuId = 0) {
  if (!(await refreshCartSetting(true))) return { data: null, error: unavailableMessage() };
  if ((product.stock ?? 0) === 0) return { data: null, get error() { return $t("暫時缺貨"); } };
  if ((product.stock ?? 0) < -1) return { data: null, get error() { return $t("庫存待確認，請稍後重試"); } };
  let result;
  if (getToken()) {
    const { data, error, status } = await addCart(product.id, quantity, skuId);
    if (isAuthError(error, status)) {
      // token 過期：降級遊客本地購物車，不阻斷加購
      result = addGuestLocal(product, quantity, skuId);
    } else {
      result = { data, error };
    }
  } else {
    result = addGuestLocal(product, quantity, skuId);
  }
  if (!result.error) await refreshCartState(); // 拉權威列表（拿到購物車項 id，供「移除」用）
  return result;
}

/** 遊客本地加購（同商品同 SKU 合併數量） */
function addGuestLocal(product: { id: number; name: string; price_cents: number; points_required?: number; stock?: number; max_quantity?:number; flash_sale?: FlashOffer }, quantity: number, skuId: number) {
  const items = guestItems();
  const existing = items.find((i) => i.product_id === product.id && (i.sku_id || 0) === skuId);
  if (existing) {
    existing.quantity = Math.min(product.max_quantity||99, existing.quantity + quantity);
    existing.max_quantity=product.max_quantity;
    existing.price_cents=cartPriceCents(product.price_cents);
    existing.flash_sale=product.flash_sale;
  } else {
    items.push({
      id: Date.now(),
      product_id: product.id,
      sku_id: skuId,
      quantity:Math.min(quantity,product.max_quantity||99),
      max_quantity:product.max_quantity,
      product_name: product.name,
      price_cents: cartPriceCents(product.price_cents),
      flash_sale: product.flash_sale,
      stock: product.stock ?? 0, // proto3 省略零庫存，不能轉換成不限。
      points_only: !!product.points_required,
      points_required: product.points_required || 0,
      valid: true,
      added_at: Date.now(),
    });
  }
  saveGuest(items);
  return { data: null, error: '' };
}

/** 改數量（遊客本地；同步角標計數） */
export function updateGuestQty(id: number, quantity: number) {
  if (!cartEnabled.value) return;
  const items = guestItems();
  const it = items.find((i) => i.id === id);
  if (it) it.quantity = Math.max(1, Math.min(it.max_quantity||99, quantity || 1));
  saveGuest(items);
  syncCartState(items);
}

/** 刪除（登錄 → 後端；遊客 → 本地）；成功後同步角標 */
export async function removeCartItem(id: number) {
  if (!(await refreshCartSetting(true))) return { data: null, error: unavailableMessage() };
  let result;
  if (getToken()) {
    result = await removeCart(id);
  } else {
    saveGuest(guestItems().filter((i) => i.id !== id));
    result = { data: null, error: '' };
  }
  if (!result.error) await refreshCartState();
  return result;
}

/** 下單成功後移除已購項（登錄 → 後端批量刪；遊客 → 本地按 id 刪）；同步角標 */
export async function clearPurchased(ids: number[]) {
  if (getToken()) {
    for (const id of ids) await removeCart(id).catch(() => {});
  } else {
    const idSet = new Set(ids);
    saveGuest(guestItems().filter((i) => !idSet.has(i.id)));
  }
  await refreshCartState();
}

/** 登錄後合併本地購物車到後端並清空（App 掛載時調用）；同步角標 */
export async function mergeGuestCart() {
  if (!getToken() || !(await refreshCartSetting())) return;
  const items = guestItems();
  if (items.length) {
    const merged = new Set<number>();
    for (const it of items) {
      const result = await addCart(it.product_id, it.quantity, it.sku_id || 0);
      if (result.error) break; // 開關在合併途中關閉時，保留尚未合併的商品。
      merged.add(it.id);
    }
    saveGuest(guestItems().filter(it => !merged.has(it.id)));
    await refreshCartState();
  }
}

/** 購物車總件數（角標；登錄 → 後端計數需調用方拉取，遊客 → 本地直接算） */
export function guestCartCount(): number {
  return guestItems().reduce((s, i) => s + i.quantity, 0);
}
