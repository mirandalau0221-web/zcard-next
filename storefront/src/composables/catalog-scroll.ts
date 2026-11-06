import { nextTick, onActivated, onDeactivated } from 'vue';
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router';

/** 與緩存的目錄實例一起保留位置，等 Suspense/KeepAlive 恢復內容後再滾動。 */
export function useCatalogScroll() {
  let position = { left: 0, top: 0 };
  let active = false;
  const save = () => {
    position = { left: window.scrollX, top: window.scrollY };
  };
  onBeforeRouteLeave(save);
  onBeforeRouteUpdate(save);
  onActivated(async () => {
    active = true;
    await nextTick();
    if (active) window.scrollTo(position);
  });
  onDeactivated(() => { active = false; });
}
