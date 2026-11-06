/**
 * 路由數據源（本地派生——後端無 /route/* 端點）
 *
 * soybean 模板原指向 mock 接口 `/route/getUserRoutes` 等，後端 Go 側從未實現，
 * 切到 dynamic 模式會直接失敗登出。此處改為**本地派生**：靜態路由表 + 登錄用戶
 * 的權限點（profile.permissions）在客戶端過濾——static/dynamic 兩模式均可用，
 * 且菜單口徑一致（ROUTE_PERMISSIONS 單一真理源）。
 *
 * ⚠️ 依賴必須走函數內動態 import：service/api 與 @/router/routes、store 之間存在
 * 靜態環（store → service/api → router/routes → builtin → elegant/imports），
 * 頂層靜態 import 會打亂模塊求值順序（"Cannot access 'layouts' before
 * initialization" 白屏事故，2026-08-18），動態 import 在調用期才加載、徹底斷環。
 */
import type { ElegantConstRoute, LastLevelRouteKey } from "@elegant-router/types";

/** get constant routes */
export async function fetchGetConstantRoutes() {
  const { createStaticRoutes } = await import("@/router/routes");
  const { constantRoutes } = createStaticRoutes();
  return { data: constantRoutes as Api.Route.MenuRoute[], error: null };
}

/** get user routes（按當前登錄用戶的角色與權限點過濾；超管全量） */
export async function fetchGetUserRoutes() {
  const [{ createStaticRoutes }, { useAuthStore }, shared] = await Promise.all([
    import("@/router/routes"),
    import("@/store/modules/auth"),
    import("@/store/modules/route/shared"),
  ]);

  const { authRoutes } = createStaticRoutes();
  const authStore = useAuthStore();

  const filtered = authStore.isStaticSuper
    ? authRoutes
    : shared.filterAuthRoutesByPermissions(
        shared.filterAuthRoutesByRoles(authRoutes, authStore.userInfo.roles),
        authStore.userInfo.buttons,
      );

  const routes = shared.sortRoutesByOrder(shared.filterRoutesByDev(filtered));
  const firstVisible = routes.find((route) => !route.meta?.hideInMenu);

  return {
    data: {
      routes: routes as Api.Route.MenuRoute[],
      home: (firstVisible?.name ?? "home") as LastLevelRouteKey,
    },
    error: null,
  };
}

/**
 * whether the route is exist
 *
 * @param routeName route name
 */
export async function fetchIsRouteExist(routeName: string) {
  const { createStaticRoutes } = await import("@/router/routes");
  const { authRoutes } = createStaticRoutes();
  const names = new Set<string>();
  const walk = (routes: ElegantConstRoute[]) => {
    routes.forEach((route) => {
      names.add(route.name as string);
      if (route.children?.length) walk(route.children);
    });
  };
  walk(authRoutes);
  return { data: names.has(routeName), error: null };
}
