// 按表格容器實際寬度分檔（full ≥1080 / mid ≥720 / compact），
// 渠道管理三個 tab 依檔裁剪列集：任意屏寬下操作列完整可見、不依賴橫向滾動。
import { computed, type Ref } from "vue";
import { useElementSize } from "@vueuse/core";

export type TableTier = "compact" | "mid" | "full";

export function useResponsiveTier(el: Ref<HTMLElement | null>) {
  // 首幀用視口寬估位（桌面優先），ResizeObserver 掛載後隨即校正，避免閃現錯誤檔位
  const { width } = useElementSize(el, { width: typeof window === "undefined" ? 1200 : window.innerWidth, height: 0 });
  const tier = computed<TableTier>(() => (width.value >= 1080 ? "full" : width.value >= 720 ? "mid" : "compact"));
  return { tier, containerWidth: width };
}
