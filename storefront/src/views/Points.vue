<template>
  <div>
    <!-- 個人中心共用導航（積分商城高亮；頂部導航已收斂，此頁從個人中心進入） -->
    <MemberTabs active="points" />
    <div class="card" style="margin-bottom: 16px;" v-if="level">
      <div style="display: flex; justify-content: space-between; flex-wrap: wrap; gap: 8px;">
        <div>
          <span class="muted">{{ $t('當前積分') }}</span>
          <span style="font-size: 22px; font-weight: 700; margin-left: 8px; color: #4338ca;">{{ level.points }}</span>
        </div>
        <div class="muted">
          {{ level.current?.name || $t('普通會員') }}
          <template v-if="level.next"> {{ $t('· 距') }} {{ level.next.name }} {{ level.progress?.percent ?? 0 }}%</template>
        </div>
      </div>
      <div class="progress" style="margin-top: 10px;"><div :style="{ width: `${level.progress?.percent ?? 100}%` }"></div></div>
      <div class="muted" style="margin-top: 8px;">{{ $t('積分來源：充值贈送 / 消費累積（') }}{{ level.current?.points_rule_json || $t('規則待配置') }}{{ $t('）；登錄後可用積分兌換下方商品') }}</div>
    </div>

    <div class="grid">
      <div v-for="p in products" :key="p.id" class="card">
        <router-link :to="`/product/${p.id}`">
          <img
            :src="p.cover || NO_IMAGE"
            :style="{ width: '100%', borderRadius: '6px', marginBottom: '8px', objectFit: p.cover ? 'cover' : 'contain', background: '#f1f5f9' }"
            :alt="p.name"
            @error="onImgError"
          />
          <div style="font-weight: 600;">{{ p.name }}</div>
        </router-link>
        <div style="margin: 8px 0; color: #4338ca; font-weight: 700;">
          {{ p.points_required }} {{ $t('積分') }} </div>
        <div class="muted" style="margin-bottom: 8px;">{{ stockLabel(p) }}</div>
        <button class="btn" :disabled="exchanging === p.id || soldOut(p)" @click="exchange(p)">
          {{ exchanging === p.id ? $t('兌換中…') : p.stock < -1 ? $t('庫存待確認') : soldOut(p) ? $t('已兌完') : $t('積分兌換') }}
        </button>
      </div>
    </div>
    <div v-if="!products.length && loaded" class="card muted" style="text-align: center;">{{ $t('暫無積分商城商品') }}</div>
    <div v-if="error" class="card error">{{ uiText(error) }}</div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, locale } from '@/i18n';

import { ref, watch, onMounted, onUnmounted } from 'vue';
import { useRouter } from 'vue-router';
import { listProducts, getMyLevel, getProduct, createOrder, type Product, type MyLevelReply } from '@/api';
import { getToken } from '@/api/client';
import MemberTabs from '@/components/MemberTabs.vue';
import { NO_IMAGE, onImgError } from '@/no-image';
import { fetchSiteSeo, applySeo } from '@/seo';

const router = useRouter();
const products = ref<Product[]>([]);
const level = ref<MyLevelReply | null>(null);
const loaded = ref(false);
const error = ref('');
const exchanging = ref<number>(0);
let exchangeRequest = 0;
onUnmounted(() => { exchangeRequest++; });

// 庫存口徑（與後端 DTO 對齊）：-1=不限（上游代發/直發），>=0=有限庫存；
// proto3 省略零值仍為缺貨；-2 待確認也暫停兌換。
function soldOut(p: Product): boolean {
  return (p.stock ?? 0) === 0 || (p.stock ?? 0) < -1;
}
function stockLabel(p: Product): string {
  const s = p.stock ?? 0;
  return s < -1 ? $t("庫存待確認") : s === -1 ? $t("不限庫存") : $t("庫存 {0} 件", [s]);
}

watch(locale, () => { void applyPointsSeo(); });
async function applyPointsSeo() {
  const site = await fetchSiteSeo();
  const origin = typeof window !== 'undefined' ? window.location.origin : site.url;
  applySeo({ title: $t("積分商城 - {0}", [site.name]), canonical: `${origin}/points`, ogType: 'website' }, site);
}

// 數據預取（setup 頂層：SSG 靜態化積分商城內容 + 輸出 SEO head）
{
  const { data, error: err } = await listProducts({ points_only: true, page: 1, page_size: 48 });
  products.value = data?.items || [];
  error.value = err || '';
  loaded.value = true;
  const site = await fetchSiteSeo();
  const origin = typeof window !== 'undefined' ? window.location.origin : site.url;
  applySeo({ title: $t("積分商城 - {0}", [site.name]), canonical: `${origin}/points`, ogType: 'website' }, site);
}

onMounted(async () => {
  // 會員等級（登錄態專屬；SSG 不渲染）
  if (getToken()) {
    const l = await getMyLevel();
    level.value = l.data;
  }
});

// 積分兌換（：use_points 下單 → 服務端同事務扣積分 → 直落 paid → 取貨頁交付）
async function exchange(p: Product) {
  if (!getToken()) {
    router.push({ path: '/login', query: { redirect: '/points' } });
    return;
  }
  if (exchanging.value) return;
  const request = ++exchangeRequest;
  exchanging.value = p.id;
  const detail = await getProduct(p.id);
  if (request !== exchangeRequest) return;
  exchanging.value = 0;
  if (detail.error || !detail.data) {
    alert(detail.error || $t("加載失敗，請檢查網絡後重試"));
    return;
  }
  // 列表沒有規格明細；有規格時進入詳情選擇，沿用現有兌換與訂單校驗。
  if (detail.data.skus?.length) {
    router.push(`/product/${p.id}`);
    return;
  }
  if (!confirm($t("確認用 {0} 積分兌換「{1}」？", [p.points_required, p.name]))) return;
  // 與詳情頁對齊：積分兌換同樣需要查詢密碼（取貨驗證用，至少 4 位）
  const qp = prompt($t("請設置查詢密碼（至少 4 位，取貨時使用）"), '');
  if (!qp || qp.length < 4) {
    alert($t("查詢密碼至少 4 位"));
    return;
  }
  exchanging.value = p.id;
  const { data, error: err } = await createOrder({
    items: [{ product_id: p.id, quantity: 1 }],
    use_points: true,
    query_password: qp,
  });
  exchanging.value = 0;
  if (err || !data) {
    alert(err || $t("兌換失敗（積分不足？）"));
    return;
  }
  alert($t("兌換成功，訂單已支付，請前往取貨頁領取卡密"));
  router.push({ path: '/fetch', query: { order_no: data.order_no } });
  // 刷新積分
  if (getToken()) {
    const l = await getMyLevel();
    level.value = l.data;
  }
}
</script>

<style scoped>
/* 移動端：兌換商品網格雙列（與首頁/列表頁一致） */
@media (max-width: 768px) {
  .grid { grid-template-columns: repeat(2, 1fr) !important; gap: 10px; }
}
</style>
