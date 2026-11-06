/**
 * 路由 → 後端權限點碼映射（authz 聲明式目錄，server/internal/mods/authz/permissions.go）
 *
 * 設計：不寫進 elegant/routes.ts（生成物，重生成會覆蓋），而是在路由初始化與
 * 守衞兩處按 route.name 查表——缺省（無條目）= 登錄即可見。
 * 超管 "*" 通配在判定函數內處理。
 */

/** 路由名 → 所需權限點（任一命中即可見） */
export const ROUTE_PERMISSIONS: Record<string, string[]> = {
  home: ["dashboard:read"],
  product: ["catalog:read"],
  category: ["catalog:category_read"],
  content: ["content:read"],
  marketing: ["memberlevel:read", "coupon:read", "lottery:read"],
  inventory: ["inventory:read"],
  order: ["order:read"],
  "payment-channel": ["payment:read"],
  wallet: ["wallet:read"],
  user: ["identity:user_read"],
  staff: ["identity:admin_read"],
  settings: ["settings:read"],
  channel: ["supply:read", "supplier:read", "procurement:read"],
};

/**
 * 判定路由對當前權限點集是否可見
 *
 * @param routeName 路由名（elegant-router name）
 * @param buttons 當前用户權限點集（profile.permissions，超管含 "*"）
 */
export function hasRoutePermission(routeName: string, buttons: string[]): boolean {
  const required = ROUTE_PERMISSIONS[routeName];
  if (!required?.length) {
    return true;
  }

  if (buttons.includes("*")) {
    return true;
  }

  return required.some((code) => buttons.includes(code));
}
