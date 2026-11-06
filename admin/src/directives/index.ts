import type { App, Directive } from "vue";
import { useAuthStore } from "@/store/modules/auth";

/**
 * 權限點判定（與後端 authz.Allowed 同語義：任一命中或 "*" 通配即通過）
 *
 * 供 v-auth 指令與腳本側調用；store 依賴 pinia 已安裝（main.ts 中 setupStore 先於 mount）
 */
export function checkAuth(codes: string | string[]): boolean {
  const authStore = useAuthStore();

  if (!authStore.isLogin) {
    return false;
  }

  const buttons = authStore.userInfo.buttons;

  if (buttons.includes("*")) {
    return true;
  }

  return typeof codes === "string" ? buttons.includes(codes) : codes.some((c) => buttons.includes(c));
}

/**
 * v-auth="'catalog:write'" / v-auth="['order:cancel', 'order:refund']"
 *
 * 無權限時移除元素（DOM 級隱藏——比 disabled 更強的表達：無權者不知道該功能存在）
 */
export const authDirective: Directive<HTMLElement, string | string[]> = {
  mounted(el, binding) {
    if (!binding.value) {
      return;
    }

    if (!checkAuth(binding.value)) {
      el.parentNode?.removeChild(el);
    }
  },
};

/** 註冊全局指令 */
export function setupDirectives(app: App) {
  app.directive("auth", authDirective);
}
