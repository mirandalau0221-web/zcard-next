<template>
  <div class="home">
    <!-- Hero 輪播（公告設置 image/carousel 優先；回落橫幅；無圖回退漸變品牌區） -->
    <div v-if="!heroReady" class="hero-placeholder" :role="heroError ? 'alert' : 'status'">
      <span>{{ heroError ? $t('店鋪展示內容暫時無法加載') : $t('正在加載店鋪內容…') }}</span>
      <button v-if="heroError" class="feed-retry" @click="loadDecorations">{{ $t('重試') }}</button>
    </div>
    <div v-else-if="heroItems.length" class="hero-slider" @mouseenter="stopHero" @mouseleave="startHero">
      <div class="hero-track" :style="{ transform: `translateX(-${heroIndex * 100}%)` }">
        <div v-for="item in heroItems" :key="item.key" class="hero-slide" @click="item.onClick">
          <img :src="item.img" :alt="item.title" />
          <div v-if="item.title" class="hero-title">{{ item.title }}</div>
        </div>
      </div>
      <div v-if="heroItems.length > 1" class="hero-dots">
        <span
          v-for="(item, i) in heroItems"
          :key="item.key"
          class="hero-dot"
          :class="{ active: i === heroIndex }"
          @click="heroIndex = i"
        ></span>
      </div>
    </div>
    <div v-else class="hero-banner">
      <div>
        <h1 :class="{ 'brand-name-placeholder': !brandReady }" :aria-busy="!brandReady">{{ siteName }}</h1>
        <p>{{ $t('在線下單 · 正品保障 · 售後無憂') }}</p>
        <div class="hero-points">
          <span>{{ $t('📦 按商品交付') }}</span>
          <span>{{ $t('🛡️ 正品保障') }}</span>
          <!-- 在線客服連結到店鋪 WhatsApp 客服 https://wa.me/85268111744 -->
          <a href="https://wa.me/85268111744" target="_blank" rel="noopener noreferrer">{{ $t('💬 在線客服') }}</a>
        </div>
      </div>
      <span class="hero-icon">🎁</span>
    </div>

    <!-- 公告條（設置文本優先；回落最新公告文章；點擊彈公告彈窗，與導航📢同源） -->
    <div v-if="noticeBarText" class="notice-bar" @click="openNoticeModal()">
      <span class="tag">{{ $t('公告') }}</span>
      <span class="notice-title">{{ noticeBarText }}</span>
      <span v-if="latestNotice && !announcementText" class="muted">{{ formatDate(latestNotice.published_at) }}</span>
    </div>

    <!-- 左右佈局：PC 左側多級分類樹 + 右側內容；移動端橫向膠囊兜底 -->
    <div id="catalog" class="home-layout">
      <CategoryTree
        v-if="navStyle !== 'grid'"
        :categories="categories" :loading="categoriesLoading" :error="!!categoriesError" @retry="loadCategories" :show-recommended="showRecommendedFilter"
        :model-value="activeCategory"
        @update:model-value="pickCategory"
      />
      <div class="home-content">
        <!-- 搜索 -->
        <form class="search-bar" role="search" @submit.prevent="goSearch">
          <input
            v-model="keyword"
            class="input"
            :placeholder="$t('搜索商品名')"
            :aria-label="$t('搜索商品名')"
          />
          <button class="btn btn-primary" type="submit">{{ $t('搜索') }}</button>
        </form>

        <!-- 分類導航：grid=頂部膠囊全斷點；list 時 PC 左樹，移動端「全部分類」摺疊樹（含全部層級，替代膠囊） -->
        <div v-if="navStyle === 'grid' && (categories.length || showRecommendedFilter)" class="card cat-chips">
          <button class="mobile-cat-head" type="button" :aria-expanded="chipsExpanded" @click="chipsExpanded = !chipsExpanded">
            <span class="mcb-bar"></span><span class="mcb-title">{{ $t('全部分類') }}</span>
            <span class="mcb-count">{{ categories.length }} {{ $t('類') }}</span>
            <span class="mcb-toggle-label">{{ chipsExpanded ? $t('摺疊') : $t('展開') }}</span>
            <span class="mcb-arrow" :class="{ open: chipsExpanded }"></span>
          </button>
          <div class="cat-chips-row" :class="{ expanded: chipsExpanded }">
            <button class="chip" :class="{ active: !activeCategory }" @click="pickCategory(0)"><ThemeIcon name="grid" class="chip-icon" />{{ $t('全部') }}</button>
            <button v-if="showRecommendedFilter" class="chip" :class="{ active: activeCategory === -1 }" @click="pickCategory(-1)">{{ $t('推薦商品') }}</button>
            <button v-for="c in categories.filter((x) => !x.parent_id)" :key="c.id" class="chip" :class="{ active: activeCategory === c.id }" @click="pickCategory(c.id)">
              <CategoryIcon :icon="c.icon" class="chip-icon" />{{ c.name }}
            </button>
          </div>
        </div>
        <div v-else-if="categories.length || showRecommendedFilter || categoriesLoading || categoriesError" class="card mobile-cat mobile-only">
          <CategoryTree variant="panel" :categories="categories" :loading="categoriesLoading" :error="!!categoriesError" @retry="loadCategories" :show-recommended="showRecommendedFilter" :model-value="activeCategory" @update:model-value="pickCategory" />
        </div>

        <div v-if="navStyle === 'grid' && categoriesError" class="error" role="alert">{{ $t('分類加載失敗') }} <button class="feed-retry" :disabled="categoriesLoading" @click="loadCategories">{{ $t('重試') }}</button></div>

        <section v-if="showRecommendations && (recommendedProducts.length || recommendationsLoading || recommendationsError)" class="home-recommendations" aria-labelledby="home-recommendations-title" :aria-busy="recommendationsLoading">
          <div class="recommendations-heading">
            <h2 id="home-recommendations-title">{{ $t('推薦商品') }}</h2>
            <router-link v-if="hasRecommended" class="recommendations-more" :to="{ path: '/', query: { recommend_only: '1' }, hash: '#catalog' }">{{ $t('查看全部') }}</router-link>
          </div>
          <div v-if="recommendationsError" class="error" role="alert">
            {{ $t('推薦商品加載失敗') }} <button class="feed-retry" :disabled="recommendationsLoading" @click="refreshRecommendations">{{ $t('重試') }}</button>
          </div>
          <div v-if="recommendationsLoading && !recommendedProducts.length" class="feed-status" role="status">{{ $t('正在加載商品…') }}</div>
          <div v-if="recommendedProducts.length" class="product-grid" :class="{ 'catalog-big': bigGrid }" :style="gridStyle">
            <ProductCard v-for="p in recommendedProducts" :key="p.id" :p="p" mode="grid" :show-sales="showSales" :show-stock="showStock" />
          </div>
        </section>

        <div v-if="error" role="alert" class="error" style="margin-bottom: 12px;">{{ uiText(error) }} <button :disabled="loading" class="feed-retry" @click="load(products.length > 0)">{{ $t('重新加載') }}</button></div>

        <CatalogToolbar :title="sectionTitle" :sort="!showSales && sort === 'sales' ? 'default' : sort" :show-sales="showSales" :view="viewMode" :loading="loading" @sort="changeSort" @view="changeView" />

        <!-- 商品列表（網格/列表雙視圖） -->
        <div ref="catalogItems" :class="[viewMode === 'grid' ? 'product-grid' : 'product-list', { 'catalog-big': viewMode === 'grid' && bigGrid }]"
          :style="viewMode === 'grid' ? gridStyle : undefined">
          <template v-for="(p, index) in products" :key="p.id">
            <ProductCard :p="p" :mode="viewMode" :show-sales="showSales" :show-stock="showStock" />
            <BannerStrip v-if="index + 1 === middleBannerIndex" :banners="middleBanners" catalog
              :label="$t('商品列表中部橫幅')" @open="openBanner" />
          </template>
        </div>
        <div v-if="products.length === 0 && !loading && !error" class="empty-state">
          <div class="empty-icon">📦</div>
          <div class="muted">{{ $t('暫無商品') }}</div>
        </div>

        <!-- 分頁器（首頁/頁碼/末頁 + 每頁條數） -->
        <div v-if="loading && !products.length" class="feed-status" role="status">{{ $t('正在加載商品…') }}</div>
        <div v-if="mobileCatalog && products.length" ref="loadMoreTarget" class="feed-status" :class="{ 'is-complete': !hasMore && !loading }" :aria-busy="loading || loadingMore">
          <span v-if="loadingMore" role="status">{{ $t('正在加載更多商品…') }}</span>
          <button v-else-if="error" class="feed-retry" @click="load(true)">{{ $t('刷新失敗，點擊重試') }}</button>
          <button v-else-if="loadMoreError" class="feed-retry" @click="loadMore()">{{ $t('加載失敗，點擊重試') }}</button>
          <button v-else-if="hasMore" class="feed-more" :disabled="loading" @click="loadMore()">{{ $t('繼續下滑加載更多 · 已加載') }} {{ products.length }} {{ $t('件') }}</button>
          <span v-else role="status">{{ $t('已顯示全部') }} {{ products.length }} {{ $t('件商品') }}</span>
        </div>
        <div v-if="!mobileCatalog && total > defaultPageSize" class="pager">
          <span class="pager-total muted">{{ $t('共') }} {{ total }} {{ $t('件') }}</span>
          <div class="pager-btns">
            <button class="pager-btn pager-jump" :disabled="page <= 1" :title="$t('首頁')" @click="goPage(1)">«</button>
            <button class="pager-btn" :disabled="page <= 1" @click="goPage(page - 1)">{{ $t('上一頁') }}</button>
            <!-- 手機端頁碼磚收起後的當前位置指示（桌面隱藏） -->
            <span class="pager-now">{{ page }} / {{ totalPage }}</span>
            <template v-for="p in pageList" :key="p">
              <span v-if="p === 0" class="pager-ellipsis">…</span>
              <button v-else class="pager-btn num" :class="{ active: p === page }" @click="goPage(p)">{{ p }}</button>
            </template>
            <button class="pager-btn" :disabled="page >= totalPage" @click="goPage(page + 1)">{{ $t('下一頁') }}</button>
            <button class="pager-btn pager-jump" :disabled="page >= totalPage" :title="$t('末頁')" @click="goPage(totalPage)">»</button>
          </div>
          <div class="pager-size">
            <span class="muted">{{ $t('每頁') }}</span>
            <select v-model.number="pageSize" class="pager-select" @change="changePageSize">
              <option v-for="s in pageSizeOptions" :key="s" :value="s">{{ s }}</option>
            </select>
            <span class="muted">{{ $t('條') }}</span>
          </div>
        </div>
      </div>
    </div>
    <BannerStrip class="home-bottom-banners" :banners="bottomBanners" :label="$t('首頁底部橫幅')" @open="openBanner" />
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag, locale } from '@/i18n';

import { loadPublicConfig } from '@/config';
import { usePageRecovery } from '@/composables/page-recovery';
import BannerStrip from '@/components/BannerStrip.vue';
import CatalogToolbar from '@/components/CatalogToolbar.vue';
import ThemeIcon from '@/components/ThemeIcon.vue';
import CategoryIcon from '@/components/CategoryIcon.vue';
import { useStockRefresh, needsStockRefresh } from '@/composables/stock-refresh';
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import { listProducts, listBanners, listPosts, listCategories, fetchAnnouncement, type Product, type Banner, type StorePost, type CategoryItem, type AnnouncementConfig } from '@/api';
import { fetchSiteSeo, applyDefaultSeo, applyVerification } from '@/seo';
import { useBranding } from '@/branding';
import { useSalesVisibility } from '@/composables/sales-visibility';
import { formatMoney } from '@/api/client';
import ProductCard from '@/components/ProductCard.vue';
import CategoryTree from '@/components/CategoryTree.vue';

const router = useRouter();
const route = useRoute();
const products = ref<Product[]>([]);
const keyword = ref('');
const searchTerm = ref('');
const defaultSort = ref('default');
let appliedDefaultView = '';
let initialized = false;
const loading = ref(true);
const error = ref('');
// 公告彈窗開啓動詞由 App.vue 提供（與導航📢公告同一彈窗；缺失時回退文章列表頁）
const openNoticeModal: () => void = inject('openNotice', () => {
  window.location.href = '/posts?type=notice';
});
const banners = ref<Banner[]>([]);
const middleBanners = ref<Banner[]>([]);
const bottomBanners = ref<Banner[]>([]);
const latestNotice = ref<StorePost | null>(null);
const categories = ref<CategoryItem[]>([]);
const categoriesLoading = ref(true);
const categoriesError = ref('');
const heroReady = ref(false);
const heroError = ref(false);
const hasRecommended = ref(false); // 推薦分類僅在存在可見推薦商品時顯示
const recommendedProducts = ref<Product[]>([]);
const recommendationsLoading = ref(true);
const recommendationsError = ref(false);
const activeCategory = ref(0);
const showRecommendedFilter = computed(() => hasRecommended.value || activeCategory.value === -1);
const viewMode = ref<'grid' | 'list'>('grid');
const page = ref(1);
const defaultPageSize = ref(20);
const pageSize = ref(20);
const showRecommendations = computed(() => activeCategory.value === 0 && !searchTerm.value && page.value === 1);
const sort = ref('default');
const total = ref(0);
const mobileCatalog = ref(typeof window !== 'undefined' && window.matchMedia('(max-width: 767px)').matches);
const loadedPage = ref(1);
const feedPageSize = ref(20);
const firstBatchCount = ref(0);
const feedEnded = ref(false);
const loadingMore = ref(false);
const loadMoreError = ref('');
const loadMoreTarget = ref<HTMLElement | null>(null);
const catalogActive = ref(true);
const hasMore = computed(() => !feedEnded.value && loadedPage.value * feedPageSize.value < total.value);
const announcement = ref<AnnouncementConfig>({ type: 'text', text: '', images: [] });
// 每頁選項跟隨後台 template.per_page（默認 20 → 20/40/60），前台不再寫死檔位
const pageSizeOptions = computed(() => [defaultPageSize.value, defaultPageSize.value * 2, defaultPageSize.value * 3]);

const { siteName, brandReady } = useBranding();

// ── 模板設置（後台 系統設置 → 模板；與商品列表頁同源消費，保證全站一致）──
const navStyle = ref('list'); // template.category_nav_style：list=左側樹 | grid=頂部膠囊
const bigGrid = ref(false); // template.default_view=big：大圖卡片（更寬的列）
const perRow = ref(0); // template.per_row：每行商品數（2-8 固定列數；0=按容器寬度自適應）
const gridMinPx = computed(() => (bigGrid.value ? 300 : 200));
const gridStyle = computed(() =>
  !bigGrid.value && perRow.value
    ? { gridTemplateColumns: `repeat(${perRow.value}, 1fr)` }
    : { gridTemplateColumns: `repeat(auto-fill, minmax(${gridMinPx.value}px, 1fr))` },
);
// 以實際渲染列數對齊完整商品行，避免橫幅前留出半行空白。
const catalogItems = ref<HTMLElement | null>(null);
const catalogColumns = ref(1);
watch([catalogItems, gridStyle, viewMode], ([element], _previous, onCleanup) => {
  if (!element || typeof window === 'undefined') return;
  const measure = () => {
    const tracks = window.getComputedStyle(element).gridTemplateColumns;
    catalogColumns.value = viewMode.value === 'grid' && tracks !== 'none' ? Math.max(1, tracks.split(' ').filter(Boolean).length) : 1;
  };
  measure();
  const observer = new ResizeObserver(measure);
  observer.observe(element);
  onCleanup(() => observer.disconnect());
}, { flush: 'post' });
const middleBannerIndex = computed(() => {
  const count = mobileCatalog.value ? Math.min(firstBatchCount.value, products.value.length) : products.value.length;
  if (!count || !middleBanners.value.length) return 0;
  const columns = viewMode.value === 'grid' ? catalogColumns.value : 1;
  const lastBreak = Math.floor((count - 1) / columns) * columns;
  // 只有一行時放在該行之後，既不擠開商品，也不製造空白行。
  if (!lastBreak) return count;
  return Math.min(lastBreak, Math.max(columns, Math.round(count / 2 / columns) * columns));
});

const { showSales, applySalesConfig, normalizeSalesSort } = useSalesVisibility();
const showStock = ref(false); // template.show_stock：卡片「庫存」顯示開關
const topBannerEnabled = ref(true); // promo.top_banner_enabled：頂部橫幅（首頁 Hero 輪播）開關
const chipsExpanded = ref(false); // 移動端 grid 膠囊：單行橫滑 → 展開多行

const categoryTitle = computed(() =>
  activeCategory.value === -1 ? $t("推薦商品") : activeCategory.value
    ? categories.value.find((c) => c.id === activeCategory.value)?.name || $t("全部商品")
    : $t("全部商品"),
);
const sectionTitle = computed(() => searchTerm.value ? $t("{0} · 搜索結果", [categoryTitle.value]) : categoryTitle.value);
const totalPage = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)));
// 頁碼列表：當前頁 ±2 + 首末頁；越界段用 0 佔位渲染省略號
const pageList = computed(() => {
  const cur = page.value, last = totalPage.value;
  const set = new Set<number>([1, last, cur - 2, cur - 1, cur, cur + 1, cur + 2]);
  const nums = [...set].filter((p) => p >= 1 && p <= last).sort((a, b) => a - b);
  const out: number[] = [];
  let prev = 0;
  for (const p of nums) {
    if (prev && p - prev > 1) out.push(0); // 省略號
    out.push(p);
    prev = p;
  }
  return out;
});
function goPage(p: number) {
  const target = Math.min(Math.max(1, p), totalPage.value);
  if (target === page.value) return;
  page.value = target;
  void navigateCatalog();
}

function changePageSize() {
  // 即使當前已在第一頁，也必須按新的每頁條數重新請求。
  page.value = 1;
  void navigateCatalog();
}

// Hero 輪播：公告設置 image/carousel 優先；頂部橫幅開啓時用生效橫幅；點擊行為跟隨來源
const heroItems = computed(() => {
  const items: { key: string; img: string; title: string; onClick?: () => void }[] = [];
  const ann = announcement.value;
  if ((ann.type === 'image' || ann.type === 'carousel') && ann.images.length) {
    ann.images.forEach((img, i) => items.push({ key: `ann-${i}`, img, title: '', onClick: openAnnouncement }));
  } else if (topBannerEnabled.value) {
    banners.value.forEach((b) => items.push({ key: `bn-${b.id}`, img: bannerImg(b), title: b.title, onClick: () => openBanner(b) }));
  }
  return items;
});
// 公告條：設置文本優先，回落最新公告文章標題
const announcementText = computed(() => (announcement.value.type === 'text' ? announcement.value.text : ''));
const noticeBarText = computed(() => (announcementText.value ? (announcement.value.summary ?? announcementText.value) : latestNotice.value?.title) || '');

// 點擊公告輪播 → 打開全局公告彈窗（App.vue 監聽）
function openAnnouncement() {
  window.dispatchEvent(new CustomEvent('zcard-open-notice'));
}

const heroIndex = ref(0);
let heroTimer: ReturnType<typeof setInterval> | null = null;
function startHero() {
  stopHero();
  if (heroItems.value.length > 1) {
    heroTimer = setInterval(() => {
      heroIndex.value = (heroIndex.value + 1) % heroItems.value.length;
    }, 4500);
  }
}
function stopHero() {
  if (heroTimer) { clearInterval(heroTimer); heroTimer = null; }
}

function bannerImg(b: Banner): string {
  // SSR 構建期無 window：PC 圖（移動端圖由客戶端水合後按視口切換）
  const isMobile = typeof window !== 'undefined' && window.innerWidth < 640;
  return (isMobile && b.mobile_image) ? b.mobile_image : b.image;
}

// Banner 點擊：product → 詳情；category → 分類列表；post → 文章詳情；notice → 公告彈窗；url → 外鏈
function openBanner(b: Banner) {
  if (b.link_type === 'product' && b.link_value) {
    router.push(`/product/${b.link_value}`);
  } else if (b.link_type === 'category' && b.link_value) {
    router.push({ path: '/', query: { category_id: b.link_value }, hash: '#catalog' });
  } else if (b.link_type === 'post' && b.link_value) {
    router.push(`/posts/${b.link_value}`);
  } else if (b.link_type === 'notice') {
    openAnnouncement();
  } else if (b.link_type === 'url' && b.link_value) {
    window.open(b.link_value, '_blank', 'noopener');
  }
}

const sorts = ['default', 'sales', 'newest', 'price_asc', 'price_desc'];
function readRouteFilters() {
  const q = route.query;
  const id = Number(q.category_id);
  activeCategory.value = Number.isSafeInteger(id) && id > 0 ? id : 0;
  if (q.recommend_only === '1' || q.recommend_only === 'true') activeCategory.value = -1;
  keyword.value = searchTerm.value = typeof q.keyword === 'string' ? q.keyword : '';
  sort.value = normalizeSalesSort(typeof q.sort === 'string' && sorts.includes(q.sort) ? q.sort : defaultSort.value);
  const size = Number(q.page_size);
  pageSize.value = !mobileCatalog.value && pageSizeOptions.value.includes(size) ? size : defaultPageSize.value;
  const p = Number(q.page);
  page.value = !mobileCatalog.value && Number.isSafeInteger(p) && p > 0 ? p : 1;
}
function scrollToCatalog() {
  const toolbar = document.querySelector('.catalog-toolbar');
  if (!toolbar) return;
  const headerHeight = document.querySelector('.topbar')?.getBoundingClientRect().height || 0;
  window.scrollTo({ top: Math.max(0, window.scrollY + toolbar.getBoundingClientRect().top - headerHeight - 12) });
}
async function navigateCatalog() {
  const query = { ...route.query };
  for (const k of ['keyword', 'category_id', 'recommend_only', 'sort', 'page', 'page_size']) delete query[k];
  if (searchTerm.value) query.keyword = searchTerm.value;
  if (activeCategory.value > 0) query.category_id = String(activeCategory.value);
  if (activeCategory.value === -1) query.recommend_only = '1';
  query.sort = sort.value;
  if (page.value > 1) query.page = String(page.value);
  if (pageSize.value !== defaultPageSize.value) query.page_size = String(pageSize.value);
  const target = { path: '/', query, hash: '#catalog' };
  if (router.resolve(target).fullPath === route.fullPath) { await load(); scrollToCatalog(); }
  else await router.push(target);
}
function goSearch() {
  searchTerm.value = keyword.value.trim();
  page.value = 1;
  void navigateCatalog();
}
function pickCategory(id: number) {
  activeCategory.value = id;
  page.value = 1;
  chipsExpanded.value = false;
  void navigateCatalog();
}
function changeSort(value: string) {
  if (!sorts.includes(value)) return;
  sort.value = normalizeSalesSort(value);
  page.value = 1;
  void navigateCatalog();
}
function changeView(value: 'grid' | 'list') {
  viewMode.value = value;
  if (value === 'grid') bigGrid.value = false;
}
let routeQueryKey = JSON.stringify(route.query);
watch(() => route.fullPath, async (_path, previous) => {
  if (!initialized || route.path !== '/') return;
  const key = JSON.stringify(route.query);
  if (key === routeQueryKey) {
    if (route.hash === '#catalog' && (previous === '/' || previous.startsWith('/?'))) { await nextTick(); scrollToCatalog(); }
    return;
  }
  routeQueryKey = key;
  readRouteFilters();
  await load();
  if (route.path === '/') { await nextTick(); scrollToCatalog(); }
});

function formatDate(unix?: number): string {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleDateString(localeTag.value);
}

const recommendationRequest = () => listProducts({ recommend_only: true, sort: 'default', page: 1, page_size: 8 });
let recommendationsSequence = 0;
async function refreshRecommendations() {
  if (!catalogActive.value || route.path !== '/') return;
  const sequence = ++recommendationsSequence;
  recommendationsLoading.value = true;
  recommendationsError.value = false;
  try {
    const { data, error: err } = await recommendationRequest();
    if (sequence !== recommendationsSequence) return;
    if (err) { recommendationsError.value = true; return; }
    recommendedProducts.value = uniqueProducts(data?.items || []);
    hasRecommended.value = recommendedProducts.value.length > 0;
  } finally {
    if (sequence === recommendationsSequence) recommendationsLoading.value = false;
  }
}

let loadSequence = 0;
function productRequest(requestPage: number, size: number) {
  return listProducts({
    keyword: searchTerm.value || undefined,
    category_id: activeCategory.value > 0 ? activeCategory.value : undefined,
    recommend_only: activeCategory.value === -1 || undefined,
    sort: normalizeSalesSort(sort.value), page: requestPage, page_size: size,
  });
}
function uniqueProducts(items: Product[]) {
  const seen = new Set<number>();
  return items.filter(item => { if (seen.has(item.id)) return false; seen.add(item.id); return true; });
}
async function load(preserveFeed = false) {
  const sequence = ++loadSequence;
  const mobile = mobileCatalog.value;
  const size = mobile ? defaultPageSize.value : pageSize.value;
  // 返回詳情頁時刷新已加載的所有批次，替換前保留原列表高度和位置。
  const pages = mobile && preserveFeed && feedPageSize.value === size ? loadedPage.value : 1;
  loading.value = true;
  loadingMore.value = false;
  loadMoreError.value = '';
  error.value = '';
  if (mobile && !preserveFeed) { products.value = []; firstBatchCount.value = 0; }
  const items: Product[] = [];
  let nextTotal = 0, lastPage = 1, lastCount = 0, firstCount = 0;
  try {
    for (let batch = 1; batch <= pages; batch++) {
      const requestPage = mobile ? batch : page.value;
      const { data, error: err } = await productRequest(requestPage, size);
      if (sequence !== loadSequence) return;
      if (err) { error.value = err; return; }
      const rows = data?.items || [];
      if (batch === 1) firstCount = rows.length;
      items.push(...rows);
      nextTotal = Number(data?.total || 0);
      lastPage = requestPage;
      lastCount = rows.length;
      if (rows.length < size || requestPage * size >= nextTotal) break;
    }
    products.value = uniqueProducts(items);
    total.value = nextTotal;
    firstBatchCount.value = firstCount;
    loadedPage.value = lastPage;
    feedPageSize.value = size;
    feedEnded.value = lastCount < size || lastPage * size >= nextTotal;
  } finally {
    if (sequence === loadSequence) loading.value = false;
  }
}
async function loadMore() {
  if (!mobileCatalog.value || !catalogActive.value || route.path !== '/' || loading.value || loadingMore.value || error.value || !hasMore.value) return;
  const sequence = loadSequence;
  const nextPage = loadedPage.value + 1;
  loadingMore.value = true;
  loadMoreError.value = '';
  try {
    const { data, error: err } = await productRequest(nextPage, feedPageSize.value);
    if (sequence !== loadSequence) return;
    if (err) { loadMoreError.value = err; return; }
    const rows = data?.items || [];
    products.value = uniqueProducts([...products.value, ...rows]);
    total.value = Number(data?.total || 0);
    loadedPage.value = nextPage;
    feedEnded.value = rows.length < feedPageSize.value || nextPage * feedPageSize.value >= total.value;
  } finally {
    if (sequence === loadSequence) loadingMore.value = false;
  }
}
// 只保留一個觀察器；請求失敗後停止自動重試，由用戶點擊重試繼續。
watch([loadMoreTarget, mobileCatalog, hasMore, loading, loadingMore, loadMoreError, error, catalogActive], ([target], _old, onCleanup) => {
  if (!target || !mobileCatalog.value || !hasMore.value || loading.value || loadingMore.value || loadMoreError.value || error.value || !catalogActive.value || typeof IntersectionObserver === 'undefined') return;
  const observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting)) void loadMore();
  }, { rootMargin: '0px 0px 160px 0px' });
  observer.observe(target);
  onCleanup(() => observer.disconnect());
}, { flush: 'post' });
let stopViewportWatch: (() => void) | undefined;
onMounted(() => {
  const media = window.matchMedia('(max-width: 767px)');
  const update = () => {
    if (mobileCatalog.value === media.matches) return;
    mobileCatalog.value = media.matches;
    if (initialized && route.path === '/') { readRouteFilters(); void load(); }
  };
  media.addEventListener('change', update);
  stopViewportWatch = () => media.removeEventListener('change', update);
  update();
});
onUnmounted(() => { stopViewportWatch?.(); ++loadSequence; ++recommendationsSequence; });

useStockRefresh(recommendedProducts, () => catalogActive.value && route.path === '/' && showRecommendations.value && !recommendationsLoading.value, async () => {
  const response = await recommendationRequest();
  return response.data?.items || [];
});

useStockRefresh(products, () => catalogActive.value && route.path === '/' && !loading.value && !loadingMore.value, async () => {
  const mobile = mobileCatalog.value;
  const size = mobile ? feedPageSize.value : pageSize.value;
  const pages = new Set<number>();
  products.value.forEach((p, index) => { if (needsStockRefresh(p)) pages.add(mobile ? Math.floor(index / size) + 1 : page.value); });
  const rows: Product[] = [];
  for (const nextPage of pages) { const response = await productRequest(nextPage, size); if (response.data) rows.push(...response.data.items); }
  return rows;
});

let needsRefresh = false;
onActivated(async () => {
  catalogActive.value = true;
  startHero();
  if (needsRefresh) {
    needsRefresh = false;
    // 保留瀏覽狀態，同時重新獲取價格、庫存和首頁標題。
    const sequence = loadSequence;
    await loadTemplateSettings();
    if (sequence !== loadSequence || !catalogActive.value || route.path !== '/') return;
    readRouteFilters();
    await refreshRecommendations();
    if (sequence !== loadSequence || !catalogActive.value || route.path !== '/') return;
    void load(mobileCatalog.value);
    void fetchSiteSeo().then(applyDefaultSeo);
  }
});
onDeactivated(() => {
  catalogActive.value = false;
  ++loadSequence;
  ++recommendationsSequence;
  recommendationsLoading.value = false;
  loading.value = false;
  loadingMore.value = false;
  stopHero();
  needsRefresh = true;
});

onMounted(async () => { startHero(); if (route.hash === '#catalog') { await nextTick(); scrollToCatalog(); } });
onUnmounted(stopHero);

async function loadCategories() {
  if (categoriesRequest) return categoriesRequest;
  categoriesRequest = (async () => {
    categoriesLoading.value = true;
    try {
      const result = await listCategories();
      if (result.error) { categoriesError.value = result.error; return; }
      categories.value = result.data?.categories || [];
      categoriesError.value = '';
    } finally { categoriesLoading.value = false; }
  })();
  try { await categoriesRequest; } finally { categoriesRequest = undefined; }
}
let categoriesRequest: Promise<void> | undefined;
watch(locale, () => { if (route.path === '/') void loadDecorations(); });
onActivated(() => { if (route.path === '/') void loadDecorations(); });
async function loadDecorations() {
  heroError.value = false;
  const requestedLocale = locale.value;
  const current = () => requestedLocale === locale.value;
  await Promise.all([
    listBanners('middle').then(b => { if (current() && b.data) middleBanners.value = b.data.banners || []; }),
    listBanners('bottom').then(b => { if (current() && b.data) bottomBanners.value = b.data.banners || []; }),
    listBanners('top').then(b => { if (!current()) return; heroError.value = !!b.error; if (b.data) { banners.value = b.data.banners || []; heroReady.value = true; } }),
    listPosts('notice', 1, 1).then(n => { if (current() && n.data) latestNotice.value = n.data.posts?.[0] || null; }),
    fetchAnnouncement().then(a => { announcement.value = a; }),
    fetchSiteSeo().then(site => { if (route.path === '/') { applyDefaultSeo(site); applyVerification(site); } }),
  ]);
}
// Render loading/error controls immediately; optional banners must not block products.
onMounted(async () => {
  await loadTemplateSettings();
  if (!catalogActive.value || route.path !== '/') return;
  readRouteFilters();
  routeQueryKey = JSON.stringify(route.query);
  initialized = true;
  void loadCategories();
  void loadDecorations();
  if (activeCategory.value === -1) await refreshRecommendations();
  else void refreshRecommendations();
  void load();
});
usePageRecovery(async () => {
  await loadTemplateSettings();
  await refreshRecommendations();
  await Promise.all([loadCategories(), loadDecorations(), load(mobileCatalog.value)]);
}, () => initialized && catalogActive.value && route.path === '/' && !loading.value && !loadingMore.value);

// Backend defaults apply on first entry; cached user choices remain intact.
async function loadTemplateSettings() {
  try {
    const json = await loadPublicConfig();
    applySalesConfig(json);
    const val = (k: string) => {
      const raw = json?.entries?.find((e: any) => e.key === k)?.value_json;
      if (raw === undefined) return undefined;
      try { return JSON.parse(raw); } catch { return raw; }
    };
    // 分類導航樣式：grid=頂部膠囊（隱藏左側樹）
    const ns = val('template.category_nav_style');
    if (ns === 'grid' || ns === 'list') navStyle.value = ns;
    // 商品默認視圖：list=列表行 | grid=網格 | big=大圖（網格加寬）
    const dv = val('template.default_view');
    const nextView = ['list', 'big'].includes(dv) ? dv : 'grid';
    if (nextView !== appliedDefaultView) {
      bigGrid.value = nextView === 'big';
      viewMode.value = nextView === 'list' ? 'list' : 'grid';
      appliedDefaultView = nextView;
    }
    // 庫存顯示沿用原有開關。
    showStock.value = val('template.show_stock') !== false;
    // 每行商品數（2-8）：顯式固定列數，替代 auto-fill 的"裝幾個算幾個"
    const pr = val('template.per_row');
    if (typeof pr === 'number' && pr >= 2 && pr <= 8) perRow.value = Math.floor(pr);
    // 每頁商品數（防濫用夾在 6~60；與 /products 頁同源消費）
    const pp = Number(val('template.per_page'));

    if (Number.isInteger(pp) && pp >= 6 && pp <= 60 && pp !== defaultPageSize.value) {
      defaultPageSize.value = pp;
      if (!route.query.page_size) {
        pageSize.value = pp;
      }
    }
    const sb = val('template.sort_by');
    if (['default', 'newest', 'sales', 'price_asc', 'price_desc'].includes(sb)) defaultSort.value = normalizeSalesSort(sb);
    // 頂部橫幅開關：關閉時 Hero 回退品牌漸變區（公告圖片輪播不受影響）
    topBannerEnabled.value = val('promo.top_banner_enabled') !== false;
  } catch { /* 配置拉取失敗保持默認 */ }
}

</script>

<style scoped>
.hero-placeholder { min-height: 160px; display: grid; place-items: center; color: #64748b; background: #f1f5f9; border-radius: 16px; }
.home { display: flex; flex-direction: column; gap: 16px; }
.home-recommendations { display: flex; flex-direction: column; gap: 12px; padding-bottom: 12px; }
.recommendations-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.recommendations-heading h2 { margin: 0; font-size: 18px; font-weight: 600; }
.recommendations-more { display: inline-flex; align-items: center; min-height: 44px; padding: 0 8px; color: var(--zc-primary); font-size: 14px; flex-shrink: 0; }
.recommendations-more:hover { text-decoration: underline; }
.recommendations-more:focus-visible { outline: 2px solid var(--zc-primary); outline-offset: 2px; border-radius: 8px; }
.feed-status { align-self: center; max-width: 100%; padding: 0 12px; border-radius: 999px; background: rgba(255, 255, 255, .94); min-height: 44px; display: flex; justify-content: center; align-items: center; color: #64748b; font-size: 13px; text-align: center; }
.feed-status.is-complete { min-height: 28px; }
.feed-more, .feed-retry { min-height: 44px; padding: 8px 12px; border: 0; border-radius: 8px; background: transparent; color: inherit; font: inherit; cursor: pointer; }
.feed-retry { color: var(--zc-primary); }
.feed-more:focus-visible, .feed-retry:focus-visible { outline: 2px solid var(--zc-primary); outline-offset: 2px; }
@media (max-width: 767px) {
  .home .home-bottom-banners { margin: 0; }
}

/* 左右佈局：PC 左側分類樹 + 右側內容 */
.home-layout { scroll-margin-top: 100px; display: flex; gap: 16px; align-items: flex-start; }
.home-content { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 16px; }
.mobile-only { display: flex; }
@media (min-width: 768px) {
  .mobile-only { display: none; }
}
/* 分類膠囊（category_nav_style=grid 頂部導航 / list 移動端兜底）——飽滿大尺寸 */
.cat-chips { padding: 0; overflow: hidden; }
.cat-chips-row.expanded { flex-wrap: wrap; overflow-x: visible; }
.cat-chips-row .chip { flex-shrink: 0; }
.cat-chips-row { display: flex; flex-wrap: nowrap; overflow-x: auto; gap: 10px; align-items: center; padding: 12px 16px; }
.chip {
  padding: 9px 22px; border-radius: 999px; font-size: 15px; font-weight: 500; color: #374151;
  background: #f3f4f6; border: 1px solid transparent; cursor: pointer; transition: all .15s;
}
.chip:hover { border-color: var(--zc-primary); color: var(--zc-primary); background: var(--zc-primary-soft); }
.chip.active { background: var(--zc-primary); color: #fff; font-weight: 600; box-shadow: 0 3px 10px color-mix(in srgb, var(--zc-primary) 30%, transparent); }
.chip-icon { margin-right: 5px; }
.chip-icon-img { width: 16px; height: 16px; object-fit: contain; border-radius: 3px; }

/* ── 移動端「全部分類」摺疊面板（PC 左樹的移動端等價物；含全部層級） ── */
/* mobile-only 會給容器 display:flex，這裏必須轉縱向，防止頭/體橫排擠壓樹體 */
.mobile-cat { padding: 0; overflow: hidden; flex-direction: column; }
.mobile-cat-head {
  width: 100%; display: flex; align-items: center; gap: 8px;
  padding: 13px 16px; background: #f8fafc; border: none;
  cursor: pointer; font-family: inherit; text-align: left;
}
.mcb-bar { width: 4px; height: 16px; border-radius: 999px; background: #ff5722; flex-shrink: 0; }
.mcb-title { font-size: 15px; font-weight: 700; color: #111827; letter-spacing: 0.5px; }
.mcb-toggle-label { font-size: 12px; color: #6b7280; }
.mcb-count {
  margin-left: auto; font-size: 12px; color: var(--zc-primary);
  background: color-mix(in srgb, var(--zc-primary) 8%, transparent); padding: 2px 9px; border-radius: 999px;
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.mcb-arrow {
  width: 7px; height: 7px; flex-shrink: 0; margin-left: 8px;
  border-right: 1.5px solid #6b7280; border-bottom: 1.5px solid #6b7280;
  transform: rotate(45deg); transition: transform 0.2s;
}
.mcb-arrow.open { transform: rotate(-135deg); }
.mobile-cat-body { border-top: 1px solid #e5e7eb; padding: 6px; }

/* ── Hero ── */
.hero-slider {
  position: relative;
  border-radius: 14px;
  overflow: hidden;
  box-shadow: 0 4px 16px rgba(15, 23, 42, 0.08);
}
.hero-track { display: flex; transition: transform 0.5s ease; }
.hero-slide { flex: 0 0 100%; position: relative; cursor: pointer; }
.hero-slide img { width: 100%; height: clamp(240px, 32vw, 360px); object-fit: cover; display: block; }
.hero-title {
  position: absolute; left: 0; right: 0; bottom: 0;
  padding: 30px 22px 16px;
  background: linear-gradient(180deg, transparent, rgba(0, 0, 0, 0.65));
  color: #fff; font-size: clamp(17px, 2.4vw, 26px); font-weight: 700;
  letter-spacing: 0.5px; text-shadow: 0 1px 8px rgba(0, 0, 0, 0.5);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.hero-dots {
  position: absolute; bottom: 10px; right: 16px; display: flex; gap: 6px;
}
.hero-dot {
  width: 8px; height: 8px; border-radius: 999px; background: rgba(255,255,255,0.5);
  cursor: pointer; transition: all 0.2s;
}
.hero-dot.active { background: #fff; width: 20px; }

.hero-banner {
  display: flex; align-items: center; justify-content: space-between;
  padding: 36px 32px; border-radius: 14px; color: #fff;
  background: linear-gradient(135deg, var(--zc-primary-hover), var(--zc-primary), var(--zc-primary-bright));
  box-shadow: 0 4px 16px color-mix(in srgb, var(--zc-primary) 25%, transparent);
}
.hero-banner > div { min-width: 0; }
.hero-banner h1 { font-size: 28px; margin-bottom: 8px; overflow-wrap: anywhere; }
.hero-banner p { opacity: 0.9; margin-bottom: 14px; }
.hero-points { display: flex; gap: 14px; font-size: 13px; }
.hero-points a { color: inherit; text-decoration: none; }
.hero-icon { font-size: 72px; opacity: 0.35; }

/* ── 公告 ── */
.notice-bar {
  display: flex; align-items: center; gap: 10px;
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 10px 16px; cursor: pointer; transition: border-color 0.2s;
}
.notice-bar:hover { border-color: color-mix(in srgb, var(--zc-primary) 45%, transparent); }
.notice-title {
  flex: 1; font-size: 14px; color: #1f2329;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

/* ── 搜索 ── */
.search-bar {
  display: flex; gap: 10px; background: #fff; border: 1px solid #e5e7eb;
  border-radius: 12px; padding: 10px 14px;
}
.search-bar .input { flex: 1; border: none; outline: none; padding: 8px 12px; font-size: 14px; }
.search-bar .input:focus { box-shadow: 0 0 0 2px color-mix(in srgb, var(--zc-primary) 20%, transparent); border-radius: 8px; }

/* ── 商品區 ── */
.product-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
}
.product-list { display: flex; flex-direction: column; gap: 10px; }
@media (max-width: 640px) {
  .product-list { gap: 0; background: #fff; border: 1px solid #e5e7eb; border-radius: 12px; overflow: hidden; }
  .product-list :deep(.product-card) { border: none; border-radius: 0; border-bottom: 1px solid #e5e7eb; }
  .product-list :deep(.banner-strip--catalog) { margin: 12px; }
  .product-list :deep(.product-card:last-child) { border-bottom: none; }
  .product-list :deep(.product-card:hover) { transform: none; box-shadow: none; }
}

.empty-state { text-align: center; padding: 48px 0; }
.empty-icon { font-size: 44px; margin-bottom: 8px; }

.pager {
  display: flex; align-items: center; justify-content: center; gap: 14px; flex-wrap: wrap;
  padding: 14px 0 6px;
}
.pager-btns { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.pager-btn {
  min-width: 34px; height: 34px; padding: 0 10px;
  border: 1px solid #e5e7eb; border-radius: 8px; background: #fff;
  font-size: 13px; color: #4b5563; cursor: pointer; transition: all 0.15s;
}
.pager-btn:hover:not(:disabled):not(.active) { border-color: var(--zc-primary); color: var(--zc-primary); }
.pager-btn.active { background: var(--zc-primary); border-color: var(--zc-primary); color: #fff; font-weight: 600; }
.pager-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.pager-ellipsis { color: #9ca3af; padding: 0 2px; }
/* 手機端當前位置指示（桌面隱藏，頁碼磚即位置） */
.pager-now { display: none; align-items: center; justify-content: center; min-width: 56px; height: 34px; font-size: 14px; font-weight: 600; color: #374151; font-variant-numeric: tabular-nums; }
.pager-size { display: flex; align-items: center; gap: 6px; font-size: 13px; white-space: nowrap; flex-shrink: 0; }
.pager-select {
  padding: 5px 8px; border: 1px solid #d1d5db; border-radius: 8px;
  font-size: 13px; outline: none; background: #fff;
}
.pager-total { font-size: 13px; }

/* ── 移動端（≤768px）：輪播限高、分類膠囊單行橫滑、商品雙列 ── */
@media (max-width: 768px) {
  .home { gap: 12px; }
  .home-layout { scroll-margin-top: 100px; gap: 12px; }
  /* 輪播高度按視口收窄（桌面 clamp 240-360px 在手機上過高） */
  .hero-slide img { height: clamp(110px, 30vw, 170px); }
  .hero-title { padding: 22px 14px 10px; font-size: 15px; }
  /* 回退品牌橫幅（無圖時）移動端收緊：省首屏空間——隱藏大圖標、壓行距 */
  .hero-banner { padding: 14px 16px; }
  .hero-banner h1 { font-size: 18px; margin-bottom: 4px; }
  .hero-banner p { margin-bottom: 8px; font-size: 12px; }
  .hero-points { font-size: 12px; gap: 10px; flex-wrap: wrap; }
  .hero-icon { display: none; }
  /* 分類展開後自動換行，長分類名不撐出屏幕。 */
  .cat-chips-row { gap: 8px; padding: 10px 12px; }
  .chip { max-width: 100%; white-space: normal; overflow-wrap: anywhere; }
  /* 商品網格：雙列瀑布（auto-fill minmax(200px) 在手機只能出 1 列） */
  .product-grid { grid-template-columns: repeat(2, 1fr) !important; gap: 10px; }
  /* 分頁器：手機緊湊單行（上一頁 · 當前/總頁 · 下一頁）——頁碼磚/省略號/首末跳轉收起，
     flex 不折行保證一行放得下；「每頁 N 條」獨佔一行居中。淡灰文字壓深一檔保可讀 */
  .pager { gap: 10px; }
  .pager-btns { flex-wrap: nowrap; justify-content: center; }
  .pager-jump, .pager-ellipsis { display: none; }
  .pager-btn.num { display: none; }
  .pager-now { display: inline-flex; }
  .pager-total, .pager-size .muted { color: #6b7280; }
  .pager-size { justify-content: center; white-space: nowrap; }
}

@media (max-width: 767px) { .catalog-big { grid-template-columns: minmax(0, 1fr) !important; } }
</style>
