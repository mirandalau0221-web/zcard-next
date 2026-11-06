<template>
  <div
    class="product-card"
    :class="[mode === 'list' ? 'list-mode' : 'grid-mode']"
    @click="$router.push(`/product/${p.id}`)"
  >
    <div class="pc-cover">
      <img v-if="p.cover" :src="p.cover" :alt="p.name" loading="lazy" @error="onImgError" />
      <img v-else :src="NO_IMAGE" :alt="p.name" class="pc-noimg" loading="lazy" />
      <span v-if="p.points_required" class="pc-points-tag">{{ p.points_required }} {{ $t('積分') }}</span>
    </div>
    <div class="pc-body">
      <div class="pc-heading">
        <div class="pc-name" :title="p.name">{{ p.name }}</div>
        <span v-if="p.is_recommend || p.category_recommend" class="pc-recommend-tag">{{ $t('推薦') }}</span>
      </div>
      <div v-if="p.delivery_kind === 'social_service'" class="pc-price">{{ $t('按填寫內容報價') }}</div>
      <div v-else-if="p.product_kind === 'sms_channel'" class="pc-price">{{ $t('進入選價') }}</div>
      <div v-else class="pc-price">{{ formatMoney(flash.price(p.price_cents, p.flash_sale)) }}<span v-if="flash.active(p.flash_sale)" class="pc-flash-label">{{ (p.flash_sale?.remaining || 0) > 0 ? $t('秒殺') : $t('已搶完') }}</span></div>
      <div v-if="p.product_kind !== 'sms_channel' && p.delivery_kind !== 'social_service' && (salesShown || stockShown)" class="pc-meta">
        <span v-if="salesShown" class="pc-sales">{{ $t('已售') }} {{ p.sales_count || 0 }}</span>
        <span v-if="stockShown && p.stock_status === 'stale'" class="pc-stock-reference" :title="stockHint(p)">{{ p.stock_reference === -1 ? $t('上次庫存不限') : $t('參考庫存 {0}', [p.stock_reference ?? 0]) }}</span>
        <span v-else-if="stockShown && stockValue(p) >= 0">{{ stockValue(p) === 0 ? $t('暫時售罄') : $t('庫存 {0}', [stockValue(p)]) }}</span>
        <span v-else-if="stockShown && p.stock === -1" class="pc-stock-free">{{ $t('不限庫存') }}</span>
        <span v-else-if="stockShown" :title="$t('上游暫未提供庫存，進入商品詳情查詢或重試')">{{ $t('庫存待確認') }}</span>
      </div>
      <button type="button" class="btn btn-primary pc-buy" :aria-label="`${buyLabel(p, mode)}：${p.name}`" @click.stop="$router.push(`/product/${p.id}`)">{{ buyLabel(p, mode) }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { t as $t, localeTag } from '@/i18n';

import type { Product } from '@/api';
import { useFlashOffers } from '@/composables/flash-offers';
const flash = useFlashOffers();
import { formatMoney } from '@/api/client';
import { NO_IMAGE, onImgError } from '@/no-image';
function stockValue(p: Product) { return p.stock ?? (p.stock_status === 'unknown' || p.stock_status === 'stale' ? -2 : 0); }
function buyLabel(p: Product, mode?: string) { if(p.delivery_kind === 'social_service') return $t("填寫信息報價"); if(p.product_kind === 'sms_channel') return $t("選價接碼"); return mode !== 'list' ? $t("查看詳情") : stockValue(p) === 0 ? $t("查看") : stockValue(p) < -1 ? $t("查看詳情") : $t("購買"); }
function stockHint(p: Product) {
  const date = p.stock_checked_at ? new Date(p.stock_checked_at * 1000).toLocaleString(localeTag.value) : '';
  return $t("上次同步{0}，僅供參考；進入商品詳情重新核對庫存", [date ? '：' + date : '']);
}


const props = defineProps<{
  p: Product;
  mode?: 'grid' | 'list';
  /** 卡片「已售」顯示（template.show_sales；缺省顯示） */
  showSales?: boolean;
  /** 卡片「庫存」顯示（template.show_stock；疊加商品級 stock_visible；缺省顯示） */
  showStock?: boolean;
}>();
const salesShown=computed(()=>props.showSales !== false && props.p.sales_visible !== false);
const stockShown=computed(()=>props.showStock !== false && props.p.stock_visible !== false && ((props.p.product_property || props.p.goods_type) !== 'physical' || props.p.track_inventory !== false));
</script>

<style scoped>
.product-card {
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: var(--zc-card-radius, 12px);
  overflow: hidden;
  cursor: pointer;
  transition: box-shadow 0.2s, border-color 0.2s, transform 0.2s;
  display: flex;
  flex-direction: column;
  min-width: 0; /* 網格/flex 子項防長詞撐爆列寬（grid item 默認 min-width:auto） */
}
.product-card:hover {
  border-color: color-mix(in srgb, var(--zc-primary) 45%, transparent);
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.08);
  transform: translateY(-2px);
}
/* 封面 */
.pc-cover {
  position: relative;
  aspect-ratio: 1 / 1;
  background: #f1f5f9;
  overflow: hidden;
}
.pc-cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.3s;
  display: block;
}
.product-card:hover .pc-cover img { transform: scale(1.05); }
.pc-cover-placeholder {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 32px;
  font-weight: 700;
  color: var(--zc-primary-border);
  background: linear-gradient(135deg, var(--zc-primary-soft), var(--zc-primary-tint));
}
/* 無圖佔位（SVG data URI）：contain 完整顯示，不參與 hover 縮放 */
.pc-noimg { object-fit: contain !important; }
.product-card:hover .pc-cover img.pc-noimg { transform: none; }
.pc-points-tag {
  position: absolute;
  left: 8px;
  top: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  background: #4338ca;
  color: #fff;
}
/* 信息區 */
.pc-body { padding: 12px; display: flex; flex-direction: column; gap: 6px; flex: 1; min-width: 0; }
.pc-heading { display:flex; align-items:flex-start; gap:6px; min-width:0; }
.pc-heading .pc-name { flex:1; min-width:0; }
.pc-recommend-tag { flex-shrink:0; white-space:nowrap; color:var(--zc-primary-hover); background:var(--zc-primary-soft); border-radius:4px; padding:1px 5px; font-size:12px; line-height:18px; }
.pc-name {
  font-size: 14px;
  font-weight: 600;
  color: #111827;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 40px;
  word-break: break-word; /* 不可斷長詞（連續英文/URL）換行，防橫向溢出 */
  overflow-wrap: anywhere;
}
.pc-flash-label { display:inline-block; margin-left:5px; padding:1px 4px; font-size:12px; line-height:18px; font-weight:500; color:#b91c1c; background:#fef2f2; border-radius:4px; }
.pc-price { color: #ff5722; font-size: 18px; font-weight: 700; }
.pc-meta {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: #64748b;
  margin-top: auto;
}
.pc-stock-free { color: #16a34a; }
.pc-stock-reference { color: #475569; }
.pc-buy { align-items: center; line-height: 1.2; flex-shrink: 0; display: none; margin-top: 8px; width: 100%; justify-content: center; }

/* 列表視圖 */
.product-card.list-mode { flex-direction: row; align-items: center; gap: 14px; padding: 12px; }
.list-mode .pc-cover { width: 64px; height: 64px; aspect-ratio: 1 / 1; border-radius: 8px; flex-shrink: 0; }
.list-mode .pc-cover img { object-fit: contain; }
.product-card.list-mode:hover .pc-cover img { transform: none; }
.list-mode .pc-cover-placeholder { font-size: 20px; }
.list-mode .pc-body { padding: 0; flex-direction: row; align-items: center; gap: 14px; flex: 1; }
.list-mode .pc-heading { flex:1; align-items:center; }
.list-mode .pc-name { min-height: auto; flex: 1; -webkit-line-clamp: 1; min-width: 0; }
.list-mode .pc-price { font-size: 16px; }
.list-mode .pc-meta { margin-top: 0; gap: 12px; }
.list-mode .pc-buy { display: inline-flex; width: auto; margin-top: 0; padding: 6px 14px; font-size: 13px; }

@media (min-width: 640px) {
  .grid-mode:hover .pc-buy { display: flex; }
}

/* 移動端雙列窄卡：收緊留白，字號保持 ≥14px */
@media (max-width: 640px) {
  .grid-mode .pc-body { padding: 10px; gap: 5px; }
  .grid-mode .pc-price { font-size: 16px; }
  .grid-mode .pc-name { min-height: 38px; }
  .product-card.list-mode { align-items: center; gap: 10px; padding: 10px 12px; }
  .list-mode .pc-cover { width: 48px; height: 48px; }
  .list-mode .pc-body {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 3px 6px;
  }
  .list-mode .pc-heading { grid-column:1; grid-row:1; align-items:flex-start; }
  .list-mode .pc-name {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    white-space: normal;
    line-height: 20px;
  }
  .list-mode .pc-price { grid-column: 1; grid-row: 2; white-space: normal; line-height: 22px; }
  .list-mode .pc-buy {
    grid-column: 2;
    grid-row: 1 / 4;
    align-self: center;
    height: 44px;
    min-width: 60px;
    padding: 6px 8px;
    white-space: nowrap;
  }
  .list-mode .pc-meta {
    grid-column: 1; grid-row: 3; min-width: 0;
    justify-content: flex-start; gap: 8px;
    flex-wrap: wrap; white-space: normal; line-height: 18px;
  }
  .list-mode .pc-meta span { flex-shrink: 0; }
  .list-mode .pc-sales { order: 1; }
}
</style>
