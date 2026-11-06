<template>
  <!-- 大廠個人中心分段式導航：白卡容器 + 膠囊選中態（與首頁卡片視覺同語言） -->
  <nav ref="navEl" class="member-tabs" :aria-label="$t('會員中心導航')">
    <button
      v-for="t in tabs"
      :key="t.key"
      :class="{ active: active === t.key }"
      :aria-current="active === t.key ? 'page' : undefined"
      type="button"
      @click="go(t.key)"
    >{{ t.label }}</button>
  </nav>
</template>

<script setup lang="ts">
import { t as $t } from '@/i18n';

// 個人中心共用導航（Member 與 Withdraw 兩頁共享）：
// 提現是獨立路由頁（/withdraw），其餘為 /member 頁內 tab（?tab= 查詢參數驅動）。

import { ref, watch, nextTick, onMounted, onBeforeUnmount } from 'vue';
import { useRouter } from 'vue-router';

const props = defineProps<{ active?: string }>();
const navEl = ref<HTMLElement | null>(null);
const scrollKey = "zcard-member-nav-scroll";
function remember() { if (navEl.value) { try { sessionStorage.setItem(scrollKey, String(navEl.value.scrollLeft)); } catch { /* optional storage */ } } }
async function revealActive(smooth = true) {
  await nextTick();
  const nav = navEl.value, item = nav?.querySelector<HTMLElement>('[aria-current="page"]');
  if (!nav || !item) return;
  const box = item.getBoundingClientRect(), bounds = nav.getBoundingClientRect();
  if (box.left < bounds.left + 6 || box.right > bounds.right - 6) {
    nav.scrollTo({ left: nav.scrollLeft + box.left - bounds.left - (bounds.width - box.width) / 2, behavior: smooth && !matchMedia("(prefers-reduced-motion: reduce)").matches ? "smooth" : "auto" });
  }
}
watch(() => props.active, () => revealActive());
onMounted(() => {
  try { if (navEl.value) navEl.value.scrollLeft = Number(sessionStorage.getItem(scrollKey)) || 0; } catch { /* optional storage */ }
  void revealActive(false);
  navEl.value?.addEventListener("scroll", remember, { passive: true });
  window.addEventListener("resize", onResize);
});
function onResize() { void revealActive(false); }
onBeforeUnmount(() => { remember(); navEl.value?.removeEventListener("scroll", remember); window.removeEventListener("resize", onResize); });

const router = useRouter();

const tabs = [
  { key: 'overview', get label() { return $t("總覽"); } },
  { key: 'orders', get label() { return $t("我的訂單"); } },
  { key: 'transactions', get label() { return $t("餘額流水"); } },
  { key: 'recharge', get label() { return $t("充值"); } },
  { key: 'giftcard', get label() { return $t("禮品卡"); } },
  { key: 'points', get label() { return $t("積分商城"); } },
  { key: 'promo', get label() { return $t("推廣營銷"); } },
  { key: 'supplier', get label() { return $t("對接申請"); } },
  { key: 'withdraw', get label() { return $t("提現"); } },
  { key: 'security', get label() { return $t("賬戶安全"); } },
  { key: 'tickets', get label() { return $t("提交工單"); } },
  { key: 'prizes', get label() { return $t("我的獎品"); } },
];

function go(key: string) {
  remember();
  if (key === props.active) return;
  if (key === "prizes") { router.push("/lottery?tab=prizes"); return; }
  if (key === "tickets") { router.push("/tickets"); return; }
  if (key === 'withdraw') {
    router.push('/withdraw');
    return;
  }
  // 積分商城同為獨立路由頁（/points），不佔 /member 頁內 tab
  if (key === 'points') {
    router.push('/points');
    return;
  }
  router.push(key === 'overview' ? '/member' : { path: '/member', query: { tab: key } });
}
</script>

<style scoped>
.member-tabs {
  display: flex; align-items: center; gap: 2px;
  background: #fff; border: 1px solid #e5e6e8; border-radius: 12px;
  padding: 6px; margin-bottom: 16px;
  overflow-x: auto; -webkit-overflow-scrolling: touch; scrollbar-width: none;
}
.member-tabs::-webkit-scrollbar { display: none; }
.member-tabs button {
  flex-shrink: 0; min-height: 44px; padding: 8px 16px; border: none; background: none; cursor: pointer;
  border-radius: 8px; font-size: 14px; color: #4b5563; font-family: inherit;
  transition: background 0.15s, color 0.15s; white-space: nowrap;
}
.member-tabs button:hover:not(.active) { color: var(--zc-primary); background: var(--zc-primary-soft); }
.member-tabs button.active { background: var(--zc-primary); color: #fff; font-weight: 600; }
</style>
