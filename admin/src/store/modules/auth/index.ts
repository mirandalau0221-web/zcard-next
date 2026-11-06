import { recoveryTicket } from "@/components/security/state";
import { computed, reactive, ref } from "vue";
import { useRoute } from "vue-router";
import { defineStore } from "pinia";
import { useLoading } from "@sa/hooks";
import { fetchGetUserInfo, fetchLogin, fetchLogout } from "@/service/api";
import { useRouterPush } from "@/hooks/common/router";
import { localStg } from "@/utils/storage";
import { SetupStoreId } from "@/enum";
import { $t } from "@/locales";
import { useRouteStore } from "../route";
import { useTabStore } from "../tab";
import { clearAuthStorage, getToken } from "./shared";

export const useAuthStore = defineStore(SetupStoreId.Auth, () => {
  const route = useRoute();
  const authStore = useAuthStore();
  const routeStore = useRouteStore();
  const tabStore = useTabStore();
  const { toLogin, redirectFromLogin } = useRouterPush(false);
  const { loading: loginLoading, startLoading, endLoading } = useLoading();

  const token = ref("");

  const userInfo: Api.Auth.UserInfo = reactive({
    userId: "",
    userName: "",
    roles: [],
    buttons: [],
  });

  /** is super role in static route */
  const isStaticSuper = computed(() => {
    const { VITE_AUTH_ROUTE_MODE, VITE_STATIC_SUPER_ROLE } = import.meta.env;

    return VITE_AUTH_ROUTE_MODE === "static" && userInfo.roles.includes(VITE_STATIC_SUPER_ROLE);
  });

  /** Is login */
  const isLogin = computed(() => Boolean(token.value));

  /** Reset auth store */
  let resetPromise: Promise<void> | null = null;
  function resetStore(): Promise<void> {
    resetPromise ??= clearSession().finally(() => { resetPromise = null; });
    return resetPromise;
  }

  async function clearSession() {
    recordUserId();

    clearAuthStorage();
    recoveryTicket.value = "";

    authStore.$reset();

    if (!route.meta.constant) {
      await toLogin();
    }

    tabStore.cacheTabs();
    await routeStore.resetStore();
  }

  /** Record the user ID of the previous login session Used to compare with the current user ID on next login */
  function recordUserId() {
    if (!userInfo.userId) {
      return;
    }

    // Store current user ID locally for next login comparison
    localStg.set("lastLoginUserId", userInfo.userId);
  }

  /**
   * Check if current login user is different from previous login user If different, clear all tabs
   *
   * @returns {boolean} Whether to clear all tabs
   */
  function checkTabClear(): boolean {
    if (!userInfo.userId) {
      return false;
    }

    const lastLoginUserId = localStg.get("lastLoginUserId");

    // Clear all tabs if current user is different from previous user
    if (!lastLoginUserId || lastLoginUserId !== userInfo.userId) {
      localStg.remove("globalTabs");
      tabStore.clearTabs();

      localStg.remove("lastLoginUserId");
      return true;
    }

    localStg.remove("lastLoginUserId");
    return false;
  }

  /**
   * Login
   *
   * @param userName User name
   * @param password Password
   * @param [captcha] 圖形驗證碼（captcha_admin_login 開啓時必填）
   * @param [redirect=true] Whether to redirect after login. Default is `true`
   */
  async function login(
    userName: string,
    password: string,
    captcha?: {
      captcha_id?: string;
      captcha_code?: string;
      challenge?: string;
      totp_code?: string;
    },
    redirect = true,
  ) {
    startLoading();

    const { data: loginToken, error } = await fetchLogin(userName, password, captcha);

    if (!error && loginToken?.requires_totp && loginToken.challenge) {
      endLoading();
      return { challenge: loginToken.challenge, reason: "" };
    }
    if (!error) {
      recoveryTicket.value = loginToken.recovery_ticket || "";
      const pass = await loginByToken(loginToken);

      if (pass) {
        // Check if the tab needs to be cleared
        const isClear = checkTabClear();
        let needRedirect = redirect;

        if (isClear) {
          // If the tab needs to be cleared,it means we don't need to redirect.
          needRedirect = false;
        }
        await redirectFromLogin(needRedirect);

        window.$notification?.success({
          title: $t("page.login.common.loginSuccess"),
          content: $t("page.login.common.welcomeBack", { userName: userInfo.userName }),
          duration: 4500,
        });
      }
    } else {
      resetStore();
    }

    endLoading();
    return { challenge: "", reason: (error as any)?.response?.data?.reason || "" };
  }

  async function loginByToken(loginToken: Api.Auth.LoginToken) {
    // Kratos 返回 snake_case：access_token / refresh_token
    const accessToken = loginToken.access_token;
    const refreshToken = loginToken.refresh_token;

    // 1. stored in the localStorage, the later requests need it in headers
    localStg.set("token", accessToken);
    localStg.set("refreshToken", refreshToken);

    // 2. get user info
    const pass = await getUserInfo();

    if (pass) {
      token.value = getToken();
      return true;
    }

    return false;
  }

  async function getUserInfo() {
    const { data, error } = await fetchGetUserInfo();

    if (!error) {
      // Kratos 返回 { admin: {...}, permissions: ["catalog:read", ...] }（snake_case）
      const admin = (data as any)?.admin || data;
      userInfo.userId = String(admin?.id || admin?.user_id || "");
      userInfo.userName = admin?.username || admin?.userName || "";
      // 真實權限點（後端 GetProfile 下發）：buttons 供 hasAuth/v-auth 按鈕級控制；
      // roles 僅映射超管（R_super 驅動 isStaticSuper 全量放行），非超管為空走權限碼過濾。
      // 兼容回退：舊後端無 permissions 字段（undefined≠空數組）→ 按 R_super 全量放行
      // （後端中間件每請求仍獨立鑑權，此處僅菜單/按鈕表現層回退，不構成越權）
      const rawPerms = (data as any)?.permissions;
      const perms: string[] = Array.isArray(rawPerms) ? rawPerms : ["*"];
      userInfo.buttons = perms;
      userInfo.roles = perms.includes("*") ? ["R_super"] : [];
      // 登錄頁跳過的貨幣初始化在此補載（符號/小數位從後台拉取）
      import("@/utils/money").then((m) => m.initCurrency(true)).catch(() => {});
      return true;
    }

    return false;
  }

  /** Logout（先請求後端吊銷 refresh 會話，再清本地——只清本地會留下 14 天有效會話） */
  async function logout() {
    const rToken = localStg.get("refreshToken");
    if (rToken) {
      try {
        await fetchLogout(rToken);
      } catch {
        // 吊銷失敗也繼續登出（token 可能已過期）
      }
    }
    await resetStore();
  }

  async function initUserInfo() {
    const maybeToken = getToken();

    if (maybeToken) {
      token.value = maybeToken;
      const pass = await getUserInfo();

      if (!pass && getToken()) {
        resetStore();
      }
    }
  }

  return {
    token,
    userInfo,
    isStaticSuper,
    isLogin,
    loginLoading,
    resetStore,
    logout,
    login,
    initUserInfo,
  };
});
