<template>
  <div class="app" :style="appBgStyle">
    <!-- 安裝頁：無商城佈局（頭部/尾部/客服/公告全隱藏，僅渲染嚮導自身） -->
    <template v-if="!isInstall && shellReady">
    <!-- 頂部品牌條：主題自定義可獨立關閉，不影響首頁輪播。 -->
    <div v-if="brandBarEnabled" class="brand-bar">
      <span class="brand-slogan">🎁 {{ siteName }} {{ $t('· 在線下單 按商品交付') }}</span>
      <span class="brand-trust">
        <span>{{ $t('✓ 安全支付') }}</span>
        <span>{{ $t('✓ 隱私保障') }}</span>
        <span>{{ $t('✓ 售後無憂') }}</span>
      </span>
    </div>

    <!-- 維護模式：橫幅樣式（ops.maintenance_style=banner） -->
    <div v-if="maintenance && maintenanceStyle === 'banner'" class="maint-banner"> {{ $t('🔧 站點維護中，下單與支付可能間歇性不可用，請稍後再試') }} </div>

    <!-- 維護模式：彈窗樣式（ops.maintenance_style=modal；ops.maintenance_modal_freq 控制頻率
         every=每次進入都彈 / daily=24 小時內只彈一次，關閉後本次會話不再打擾） -->
    <div v-if="maintenance && maintenanceStyle === 'modal' && maintModalVisible" class="maint-overlay">
      <div class="maint-card">
        <div class="maint-icon">🔧</div>
        <h2>{{ $t('站點維護中') }}</h2>
        <p>{{ $t('我們正在升級系統，下單與支付可能間歇性不可用。') }}<br />{{ $t('給您帶來不便敬請諒解，請稍後再試。') }}</p>
        <button class="maint-btn" @click="closeMaintModal">{{ $t('我知道了，繼續瀏覽') }}</button>
      </div>
    </div>

    <!-- 主導航（sticky） -->
    <header class="topbar">
      <router-link to="/" class="logo" :aria-busy="!brandReady" :aria-label="$t('店鋪首頁')">
        <img v-if="siteLogo" :src="siteLogo" alt="logo" class="logo-mark logo-img" />
        <span v-else-if="brandReady" class="logo-mark">ZC</span>
        <span v-else class="logo-mark brand-placeholder" aria-hidden="true"></span>
        <span class="logo-name" :class="{ 'brand-name-placeholder': !brandReady }" :title="siteName">{{ siteName }}</span>
      </router-link>
      <nav class="nav-links">
        <router-link v-if="lotteryVisible" to="/lottery">{{ $t('幸運抽獎') }}</router-link>
        <router-link to="/" exact>{{ $t('首頁') }}</router-link>
        <button class="nav-horn" @click="openNotice" :title="$t('系統公告')"><ThemeIcon name="announcement" />{{ $t('公告') }}</button>
        <router-link to="/fetch">{{ $t('取貨查詢') }}</router-link>
        <!-- 頂部自定義按鈕：外部鏈接新窗口；文章/公告站內路由（site.top_button {text,type,url|slug}） -->
        <router-link
          v-if="topButton && topButton.type !== 'link' && topButton.slug"
          :to="`/posts/${topButton.slug}`"
          class="nav-custom-btn"
        ><ThemeIcon v-if="isGuide(topButton.text)" name="book" />{{ navText(topButton.text) }}</router-link>
        <a
          v-else-if="topButton"
          :href="topButton.url || '#'"
          :target="topButton.url ? '_blank' : undefined"
          rel="noopener noreferrer"
          class="nav-custom-btn"
        ><ThemeIcon v-if="isGuide(topButton.text)" name="book" />{{ navText(topButton.text) }}</a>
        <!-- 導航推薦位（promo.nav_recommend [{text,url}]）：站內路徑走路由，外鏈新窗口 -->
        <template v-for="r in navRecommend" :key="r.text + r.url">
          <router-link v-if="r.url.startsWith('/')" :to="r.url" class="nav-recommend-btn">{{ r.text }}</router-link>
          <a v-else :href="r.url" target="_blank" rel="noopener noreferrer" class="nav-recommend-btn">{{ r.text }}</a>
        </template>
      </nav>
      <div class="nav-right">
        <LanguageSwitcher />
        <CurrencySwitcher />
        <router-link v-if="cartEnabled" to="/cart" class="cart-link" :title="$t('查看購物車')" :aria-label="$t('查看購物車')">
          <ThemeIcon name="cart" class="cart-icon" />
          <span class="cart-label">{{ $t('購物車') }}</span>
          <span v-if="cartCount > 0" :key="cartCount" class="cart-badge">{{ cartCount > 99 ? '99+' : cartCount }}</span>
        </router-link>
        <template v-if="authState.loggedIn">
          <router-link to="/member" class="member-link" :title="$t('個人中心')">{{ $t('👤 個人中心') }}</router-link>
          <router-link to="/member" class="user-link">{{ authState.username }}</router-link>
          <button class="logout-btn" @click="onLogout">{{ $t('退出') }}</button>
        </template>
        <template v-else>
          <router-link to="/login" class="login-link">{{ $t('登錄') }}</router-link>
          <router-link to="/register" class="btn btn-primary">{{ $t('註冊') }}</router-link>
        </template>
      </div>
    </header>
    </template>

    <main class="main" :class="{ 'install-main': isInstall, 'home-main': route.path === '/' }">
      <!-- Suspense：路由組件 async setup（SSG 預取）在客戶端水合時也能正常等待 -->
      <router-view v-if="shellReady || isInstall" v-slot="{ Component, route: viewRoute }">
        <!-- 保留目錄瀏覽狀態；會員頁內切換複用表單，其餘詳情和支付頁按導航重新創建。 -->
        <KeepAlive include="Home" :max="8">
          <Suspense>
            <template #fallback><div class="store-loading" role="status">{{ $t('正在加載頁面…') }}</div></template>
            <component :is="Component" :key="['/', '/member', '/posts'].includes(viewRoute.path) ? viewRoute.path : viewRoute.fullPath" />
          </Suspense>
        </KeepAlive>
      </router-view>
      <div v-if="!shellReady && !isInstall" class="store-loading" :role="shellError ? 'alert' : 'status'" :aria-busy="shellLoading">
        <p>{{ shellError || $t('正在加載店鋪…') }}</p>
        <a v-if="!shellError" href="" class="muted">{{ $t('加載時間較長？重新加載') }}</a>
        <noscript>{{ $t('請啓用 JavaScript 後重新加載店鋪。') }}</noscript>
        <button v-if="shellError" class="btn btn-primary" :disabled="shellLoading" @click="refreshShell">{{ $t('重新加載') }}</button>
      </div>
    </main>

    <!-- 頁腳（安裝頁隱藏；頁腳配置 footer.* 可覆蓋關於/導航/聯繫/社交/備案） -->
    <template v-if="!isInstall && shellReady">
    <footer class="footer">
      <div class="footer-trust">
        <div v-for="(item, index) in footerTrust" :key="index" class="trust-item">
          <span class="trust-icon" aria-hidden="true">
            <img v-if="item.image" :src="item.image" alt="" @error="item.image = ''" />
            <template v-else>{{ item.icon }}</template>
          </span>
          <div class="trust-copy"><b>{{ item.title }}</b><span class="muted">{{ item.description }}</span></div>
        </div>
      </div>
      <nav class="footer-mobile-actions" :aria-label="$t('常用服務')">
        <router-link to="/fetch">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3 7 9-4 9 4v10l-9 4-9-4Z"/><path d="m3 7 9 4 9-4M12 11v10M7.5 5l9 4"/></svg>
          <span>{{ $t('訂單取貨') }}</span>
        </router-link>
        <router-link to="/posts?type=blog">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 4h6a3 3 0 0 1 3 3v14a4 4 0 0 0-4-2H4ZM20 4h-4a3 3 0 0 0-3 3v14a4 4 0 0 1 4-2h3Z"/></svg>
          <span>{{ $t('使用幫助') }}</span>
        </router-link>
        <router-link to="/tickets">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 13v-2a8 8 0 0 1 16 0v2M4 12H3v6h4v-6ZM20 12h1v6h-4v-6ZM19 18v1a2 2 0 0 1-2 2h-5"/></svg>
          <span>{{ $t('售後工單') }}</span>
        </router-link>
      </nav>
      <button class="footer-mobile-toggle" type="button" :aria-expanded="footerExpanded" aria-controls="footer-information" @click="footerExpanded = !footerExpanded">
        <span>{{ footerExpanded ? $t('收起服務與店鋪信息') : $t('更多服務與店鋪信息') }}</span>
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5" :class="{ expanded: footerExpanded }" aria-hidden="true"><path d="m5 7.5 5 5 5-5"/></svg>
      </button>
      <div id="footer-information" class="footer-cols" :class="{ 'is-expanded': footerExpanded }">
        <div class="footer-col">
          <div class="footer-brand">
            <img v-if="siteLogo" :src="siteLogo" alt="logo" class="logo-mark logo-img" />
            <span v-else-if="brandReady" class="logo-mark">ZC</span>
            <span v-else class="logo-mark brand-placeholder" aria-hidden="true"></span>
          </div>
          <p class="muted">{{ footerAbout || $t('在線選購商品，查看虛擬交付結果或實體配送進度。') }}</p>
          <!-- 社交鏈接（footer.social = [{icon,url}]，配置後顯示） -->
          <div v-if="footerSocial.length" class="footer-social">
            <a v-for="(s, i) in footerSocial" :key="i" :href="s.url || '#'" target="_blank" rel="noopener noreferrer" class="footer-social-link" :title="s.url">{{ s.icon }}</a>
          </div>
        </div>
        <div class="footer-col">
          <h4>{{ $t('快速導航') }}</h4>
          <template v-if="footerNav.length">
            <a v-for="(n, i) in footerNav" :key="i" :href="n.url || '#'">{{ n.text }}</a>
          </template>
          <template v-else>
            <router-link to="/#catalog">{{ $t('選購商品') }}</router-link>
            <router-link to="/points">{{ $t('積分商城') }}</router-link>
            <router-link to="/coupons">{{ $t('優惠券') }}</router-link>
            <router-link to="/affiliate">{{ $t('推廣中心') }}</router-link>
          </template>
        </div>
        <div class="footer-col">
          <h4>{{ $t('幫助中心') }}</h4>
          <router-link to="/posts?type=notice">{{ $t('系統公告') }}</router-link>
          <router-link to="/posts?type=blog">{{ $t('使用幫助') }}</router-link>
          <router-link to="/tickets">{{ $t('提交工單') }}</router-link>
          <router-link to="/fetch">{{ $t('訂單取貨') }}</router-link>
        </div>
        <div class="footer-col">
          <h4>{{ $t('會員服務') }}</h4>
          <router-link to="/member">{{ $t('會員中心') }}</router-link>
          <router-link to="/member?tab=orders">{{ $t('我的訂單') }}</router-link>
          <router-link to="/member?tab=recharge">{{ $t('餘額充值') }}</router-link>
          <router-link to="/member?tab=giftcard">{{ $t('禮品卡兌換') }}</router-link>
        </div>
        <div v-if="footerContact" class="footer-col">
          <h4>{{ $t('聯繫我們') }}</h4>
          <p class="muted" style="white-space: pre-line;">{{ footerContact }}</p>
        </div>
      </div>
      <div class="footer-copy">
        <span class="footer-copyright">© {{ year }} {{ siteName }}<span class="footer-rights"> {{ $t('· 保留所有權利') }}</span></span>
        <a v-if="agreementHref" :href="agreementHref" :target="agreementHref.startsWith('http') ? '_blank' : undefined" rel="noopener noreferrer" class="footer-copy-link">{{ $t('用戶協議') }}</a>
        <a v-if="footerIcp" href="https://beian.miit.gov.cn/" target="_blank" rel="noopener noreferrer" class="footer-copy-link">{{ footerIcp }}</a>
      </div>
    </footer>

    <!-- 回到頂部懸浮球 -->
    <button v-if="showTop" class="back-top" @click="scrollTop" :title="$t('回到頂部')">↑</button>

    <!-- 右下角客服（第三方腳本原生氣泡 / 本站鏈接浮窗） -->
    <ServiceWidget />

    <!-- 公告彈窗（首次訪問自動彈出 + 導航喇叭/首頁公告輪播入口） -->
    <NoticeModal :show="noticeShow" :post="noticePost" :content="noticeContent" :announcement="noticeAnnouncement" @update:show="(v: boolean) => (noticeShow = v)" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { t as $t } from '@/i18n';

import ThemeIcon from '@/components/ThemeIcon.vue';
import { loadPublicConfig, publicConfig, type PublicConfig } from './config';
import { ref, computed, watch, onMounted, onUnmounted, provide } from 'vue';
import { useBranding } from './branding';
import { useRoute, useRouter } from 'vue-router';
import { initCurrency } from '@/api/client';
import { authState, refreshAuth, logout } from '@/auth';
import { listPosts, getPost, fetchAnnouncement, type StorePost, type AnnouncementConfig } from '@/api';
import { mergeGuestCart, refreshCartState, cartState, cartEnabled, refreshCartSetting } from '@/cart';
import { captureRefCode } from '@/ref';
import { listLotteryActivities } from '@/api/lottery';
import { themeValue } from '../../packages/theme-sdk/src/index';
import NoticeModal from '@/components/NoticeModal.vue';
import ServiceWidget from '@/components/ServiceWidget.vue';
import LanguageSwitcher from '@/components/LanguageSwitcher.vue';
import { locale } from '@/i18n';
import CurrencySwitcher from '@/components/CurrencySwitcher.vue';

const lotteryVisible = ref(false);
onMounted(async () => { const { data } = await listLotteryActivities(); lotteryVisible.value = !!data?.items?.length; });
const router = useRouter();
const route = useRoute();

// 安裝頁：無商城佈局（頭部/尾部/客服/公告等全部不渲染，見模板 v-if）
const isInstall = computed(() => route.path === '/install');

const { siteName, siteLogo, brandReady } = useBranding();
watch([siteLogo, brandReady], () => {
  if (!brandReady.value || typeof document === 'undefined') return;
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (icon) { icon.removeAttribute('type'); icon.href = siteLogo.value || '/zcard-icon.png'; }
}, { immediate: true });

// SEO 默認 head 由 main.ts 處理（客戶端拉取後更新；SSR 渲染後輸出到靜態 HTML）

// 啓動加載默認貨幣（i18n.base_currency → 符號/小數位；失敗回退默認符號）
if (!import.meta.env.SSR) initCurrency();
// 登錄態恢復（token 存在則拉用戶名；失敗靜默——401 層統一處理）
if (!import.meta.env.SSR) refreshAuth();

const year = new Date().getFullYear();

// 購物車角標：共享響應式狀態（商品頁加購/刪除實時聯動；:key 變化觸發彈跳動畫）
async function refreshCartAvailability() {
  if (!isInstall.value && await refreshCartSetting(true)) await refreshCartState();
}
watch(() => route.fullPath, refreshCartAvailability);
const cartCount = computed(() => cartState.value.count);
// 登錄/登出後重拉（登錄合併本地車、登出回退本地車）
watch(() => authState.loggedIn, () => { refreshCartState(); });

// 統計代碼（service.stats_script）：注入 document.body 末尾——統計代碼置底，與客服懸浮球無關
let statsInjected = false;
function injectStatsScript(html: string) {
  if (statsInjected) return;
  statsInjected = true;
  try {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const scripts = Array.from(doc.querySelectorAll('script'));
    if (!scripts.length) return;
    for (const s of scripts) {
      const code = s.textContent || '';
      if (!code.trim()) continue;
      const el = document.createElement('script');
      el.textContent = code;
      el.dataset.zcardStatsScript = 'true';
      document.body.appendChild(el); // 頁面最底部
    }
  } catch { /* 統計腳本注入失敗忽略 */ }
}

// 回到頂部
const showTop = ref(false);
function onScroll() {
  showTop.value = window.scrollY > 300;
}
function scrollTop() {
  window.scrollTo({ top: 0, behavior: 'smooth' });
}

// ── 頂部自定義按鈕（site.top_button 公開下發）+ 維護模式（ops.maintenance[_style]）──
// top_button：{text, type: link|post|notice, url?|slug?}——link 外鏈新窗口；post/notice 站內文章路由
// 教程入口可能來自自定義按鈕或推薦位；只替換舊書本前綴，配置原文不變。
const isGuide = (text: string) => /教程|指南|幫助/.test(text);
const navText = (text: string) => isGuide(text) ? text.replace(/^(?:📚|📖|📘|📙|📗|📕|📔|📓)\s*/, '') : text;
const topButton = ref<{ text: string; type?: string; url?: string; slug?: string } | null>(null);
const navRecommend = ref<{ text: string; url: string }[]>([]);
const maintenance = ref(false);
const maintenanceStyle = ref('modal'); // modal=全屏遮罩 | banner=頂部橫幅
const maintenanceModalFreq = ref('every'); // every=每次進入都彈 | daily=24 小時一次
// 無主題配置時兼容舊 promo 開關；主題自定義優先，缺省顯示。
const brandBarEnabled = ref(true);
// template.bg_image：全站背景圖（空=默認純色背景）
const bgImage = ref(themeValue('template.bg_image', ''));
const bgImageMobile = ref(themeValue('template.bg_image_mobile', ''));
const mobileBackground = ref(typeof window !== 'undefined' && window.matchMedia('(max-width: 768px)').matches);
let backgroundMedia: MediaQueryList | null = null;
function updateBackgroundMedia() { mobileBackground.value = backgroundMedia?.matches ?? false; }
onMounted(() => { backgroundMedia = window.matchMedia('(max-width: 768px)'); updateBackgroundMedia(); backgroundMedia.addEventListener('change', updateBackgroundMedia); });
onUnmounted(() => backgroundMedia?.removeEventListener('change', updateBackgroundMedia));
// 背景屬於商城外殼，導航到商品詳情時保持不變。
const appBgStyle = computed(() => {
  const image = mobileBackground.value ? bgImageMobile.value || bgImage.value : bgImage.value;
  if (!image || isInstall.value) return undefined;
  return { backgroundImage: `url(${JSON.stringify(image)})`, backgroundSize: 'cover', backgroundPosition: 'center', backgroundAttachment: 'fixed' };
});

// ── 頁腳配置（footer.* 公開下發：about/nav/social/contact/agreement/icp，空值回落默認）──
const footerExpanded = ref(false);
watch(() => route.fullPath, () => { footerExpanded.value = false; });
const footerTrustDefaults = [
  { icon: '⚡', image: '', get title() { return $t("按商品履約"); }, get description() { return $t("虛擬交付或快遞配送"); } },
  { icon: '🛡️', image: '', get title() { return $t("正品保障"); }, get description() { return $t("渠道直供貨源"); } },
  { icon: '💬', image: '', get title() { return $t("在線客服"); }, get description() { return $t("7×24 小時響應"); } },
  { icon: '↩️', image: '', get title() { return $t("售後無憂"); }, get description() { return $t("問題訂單快速處理"); } },
];
const footerTrust = ref(footerTrustDefaults.map(item => ({ ...item })));
function applyFooterTrustSettings(read: (key: string, fallback: string) => string) {
  footerTrust.value = footerTrustDefaults.map((item, index) => {
    const prefix = `theme.footer_service_${index + 1}`;
    return {
      icon: read(`${prefix}_icon`, item.icon),
      image: read(`${prefix}_image`, item.image),
      title: (() => { const value = read(`${prefix}_title`, item.title); const source = ['按商品履約', '正品保障', '在線客服', '售後無憂'][index]; return value === source ? $t(source) : value; })(),
      description: (() => { const value = read(`${prefix}_description`, item.description); const source = ['虛擬交付或快遞配送', '渠道直供貨源', '7×24 小時響應', '問題訂單快速處理'][index]; return value === source ? $t(source) : value; })(),
    };
  });
}
const footerAbout = ref('');
const footerNav = ref<{ text: string; url: string }[]>([]);
const footerSocial = ref<{ icon: string; url: string }[]>([]);
const footerContact = ref('');
const footerAgreement = ref('');
const footerIcp = ref('');
// 協議鏈接：http(s) 外鏈原樣；非空文本視為文章 slug → /posts/<slug>
const agreementHref = computed(() => {
  const v = footerAgreement.value.trim();
  if (!v) return '';
  if (/^https?:\/\//.test(v)) return v;
  return `/posts/${v}`;
});

// 維護彈窗顯隱：daily 頻率下 24 小時窗口用 localStorage 標記；關閉即記時間戳
const maintModalVisible = ref(false);
const MAINT_SHOWN_KEY = 'zc_maint_shown_at';

function maintShownRecently(): boolean {
  try {
    const ts = Number(localStorage.getItem(MAINT_SHOWN_KEY) || 0);
    return ts > 0 && Date.now() - ts < 24 * 60 * 60 * 1000;
  } catch {
    return false;
  }
}

function closeMaintModal() {
  maintModalVisible.value = false;
  try {
    localStorage.setItem(MAINT_SHOWN_KEY, String(Date.now()));
  } catch { /* 隱私模式忽略 */ }
}

// ── 公告彈窗（設置公告優先；回落最新公告文章；首次訪問自動彈出 + 導航喇叭/首頁輪播隨時重開）──
const noticeShow = ref(false);
const noticePost = ref<StorePost | null>(null);
const noticeContent = ref('');
const noticeAnnouncement = ref<AnnouncementConfig | null>(null);

let noticeRequest = 0;
async function loadNotice() {
  const currentRequest = ++noticeRequest;
  const requestedLocale = locale.value;
  // 設置公告（ops.announcement）：text/image/carousel 任一配置生效即優先
  const ann = await fetchAnnouncement();
  if (currentRequest !== noticeRequest || requestedLocale !== locale.value) return;
  if ((ann.type === 'text' && ann.text) || ann.images.length) {
    noticeAnnouncement.value = ann;
    noticePost.value = null;
    noticeContent.value = '';
    return;
  }
  noticeAnnouncement.value = null;
  const { data } = await listPosts('notice', 1, 1);
  if (currentRequest !== noticeRequest || requestedLocale !== locale.value) return;
  const post = data?.posts?.[0];
  if (!post) return;
  noticePost.value = post;
  const { data: detail } = await getPost(post.slug).catch(() => ({ data: null }));
  if (currentRequest !== noticeRequest || requestedLocale !== locale.value) return;
  noticeContent.value = detail?.content || '';
}

function openNotice() {
  if (noticePost.value || noticeAnnouncement.value) noticeShow.value = true;
  else {
    // 尚未加載（或失敗）：靜默拉一次再彈
    loadNotice().then(() => { noticeShow.value = true; });
  }
}
// 供頁面內公告入口（首頁滾動公告條等）複用同一彈窗
provide('openNotice', openNotice);

const shellReady = ref(!import.meta.env.SSR && !!publicConfig.value);
const shellLoading = ref(false);
const shellError = ref('');
function applyShellConfig(json: PublicConfig) {
    const wasMaintenance = maintenance.value;
    const previousMaintenanceStyle = maintenanceStyle.value;
    const find = (k: string) => json?.entries?.find((e: any) => e.key === k)?.value_json;
    applyFooterTrustSettings((key, fallback) => {
      try {
        const value = JSON.parse(find(key) ?? 'null');
        return typeof value === 'string' ? value : fallback;
      } catch { return themeValue(key, fallback); }
    });
    topButton.value = null;
    navRecommend.value = [];
    // 頂部自定義按鈕 {text,type,url|slug}
    const tb = find('site.top_button');
    if (tb) {
      try {
        const v = JSON.parse(tb);
        if (v && typeof v.text === 'string' && v.text.trim()) {
          topButton.value = {
            text: v.text.trim(),
            type: v.type === 'post' || v.type === 'notice' ? v.type : 'link',
            url: typeof v.url === 'string' ? v.url.trim() : '',
            slug: typeof v.slug === 'string' ? v.slug.trim() : '',
          };
        }
      } catch { /* 非法配置忽略 */ }
    }
    // 導航推薦位（promo.nav_recommend [{text,url}]：最多取 3 條，非法結構忽略）
    const nr = find('promo.nav_recommend');
    if (nr) {
      try {
        const v = JSON.parse(nr);
        if (Array.isArray(v)) {
          navRecommend.value = v
            .filter((x: any) => x && typeof x.text === 'string' && x.text.trim() && typeof x.url === 'string' && x.url.trim())
            .slice(0, 3)
            .map((x: any) => ({ text: x.text.trim(), url: x.url.trim() }));
        }
      } catch { /* 非法配置忽略 */ }
    }
    // 頁腳配置（footer.*：空值回落模板默認）
    const fa = find('footer.about');
    if (fa) { try { footerAbout.value = String(JSON.parse(fa) || ''); } catch { /* ignore */ } }
    const fn = find('footer.nav');
    if (fn) {
      try {
        const v = JSON.parse(fn);
        footerNav.value = Array.isArray(v) ? v.filter((x: any) => x && typeof x.text === 'string' && typeof x.url === 'string') : [];
      } catch { /* ignore */ }
    }
    const fs = find('footer.social');
    if (fs) {
      try {
        const v = JSON.parse(fs);
        footerSocial.value = Array.isArray(v) ? v.filter((x: any) => x && typeof x.icon === 'string' && typeof x.url === 'string') : [];
      } catch { /* ignore */ }
    }
    const fc = find('footer.contact');
    if (fc) { try { footerContact.value = String(JSON.parse(fc) || ''); } catch { /* ignore */ } }
    const fag = find('footer.agreement');
    if (fag) { try { footerAgreement.value = String(JSON.parse(fag) || ''); } catch { /* ignore */ } }
    const ficp = find('footer.icp');
    if (ficp) { try { footerIcp.value = String(JSON.parse(ficp) || ''); } catch { /* ignore */ } }
    // 維護模式與樣式（modal=遮罩彈窗 / banner=頂部橫幅）；彈窗頻率 daily=24h 一次
    const mt = find('ops.maintenance');
    if (mt) { try { maintenance.value = JSON.parse(mt) === true; } catch { /* ignore */ } }
    // 主題品牌條開關獨立於首頁橫幅；未配置主題開關時沿用舊設置。
    const tbe = find('theme.brand_bar_enabled') || find('promo.top_banner_enabled');
    if (tbe) { try { brandBarEnabled.value = JSON.parse(tbe) !== false; } catch { /* ignore */ } }
    // 全站背景圖（template.bg_image；空=默認純色）
    const mobileBi = find('template.bg_image_mobile');
    if (mobileBi) { try { const v = JSON.parse(mobileBi); if (typeof v === 'string') bgImageMobile.value = v; } catch { /* ignore */ } }
    const bi = find('template.bg_image');
    if (bi) { try { const v = JSON.parse(bi); if (typeof v === 'string') bgImage.value = v; } catch { /* ignore */ } }
    const ms = find('ops.maintenance_style');
    if (ms) { try { const v = JSON.parse(ms); if (v === 'banner' || v === 'modal') maintenanceStyle.value = v; } catch { /* ignore */ } }
    const mf = find('ops.maintenance_modal_freq');
    if (mf) { try { const v = JSON.parse(mf); if (v === 'every' || v === 'daily') maintenanceModalFreq.value = v; } catch { /* ignore */ } }
    if (maintenance.value && maintenanceStyle.value === 'modal' && (!wasMaintenance || previousMaintenanceStyle !== 'modal')) {
      if (maintenanceModalFreq.value === 'daily' && maintShownRecently()) {
        maintModalVisible.value = false; // 24 小時內已彈過：本次不再打擾
      } else {
        maintModalVisible.value = true;
      }
    }
    if (!maintenance.value) maintModalVisible.value = false;
    const stats = find('service.stats_script');
    if (stats) {
      let script = '';
      try { script = JSON.parse(stats); } catch { script = stats; }
      if (typeof script === 'string' && script.trim()) injectStatsScript(script);
    }

}
watch(locale, () => {
  if (publicConfig.value) applyShellConfig(publicConfig.value);
  if (!isInstall.value) void loadNotice();
});
watch(publicConfig, (config) => {
  if (!config || import.meta.env.SSR) return;
  applyShellConfig(config);
  shellReady.value = true;
  shellError.value = '';
}, { immediate: true });
async function refreshShell() {
  if (shellLoading.value) return;
  shellLoading.value = true;
  try { await loadPublicConfig(); }
  catch { if (!shellReady.value) shellError.value = $t("店鋪暫時無法加載，請檢查網絡後重試"); }
  finally { shellLoading.value = false; }
}
function resumeShell() {
  if (document.visibilityState === 'visible' && !isInstall.value) void refreshShell();
}

onMounted(async () => {
  if (isInstall.value) return;
  // Store identity and footer must not wait for cart or member requests.
  void refreshShell();
  void refreshCartAvailability();
  window.addEventListener('focus', refreshCartAvailability);
  window.addEventListener('online', resumeShell);
  document.addEventListener('visibilitychange', resumeShell);
  captureRefCode();

  refreshCartState();
  window.addEventListener('scroll', onScroll);
  // 登錄態：合併本地遊客購物車到後端（遊客加購的商品登錄後自動同步；merge 內部會刷新角標）
  if (authState.loggedIn) {
    await mergeGuestCart();
  }
  // 公告：每會話首次訪問自動彈出（sessionStorage 標記）
  await loadNotice();
  try {
    if (themeValue('theme.notice_auto', true) && (noticePost.value || noticeAnnouncement.value) && !sessionStorage.getItem('zc_notice_shown')) {
      sessionStorage.setItem('zc_notice_shown', '1');
      noticeShow.value = true;
    }
  } catch { /* Storage restrictions must not break the storefront. */ }
  // 首頁公告輪播點擊 → 打開彈窗
  window.addEventListener('zcard-open-notice', openNotice);
});
onUnmounted(() => {
  window.removeEventListener('focus', refreshCartAvailability);
  window.removeEventListener('online', resumeShell);
  document.removeEventListener('visibilitychange', resumeShell);
  window.removeEventListener('scroll', onScroll);
  window.removeEventListener('zcard-open-notice', openNotice);
});

function onLogout() {
  logout();
  router.push('/');
}
</script>
