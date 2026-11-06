import { useAuthStore } from "@/store/modules/auth";

export function useAuth() {
  const authStore = useAuthStore();

  function hasAuth(codes: string | string[]) {
    if (!authStore.isLogin) {
      return false;
    }

    // buttons = profile 下發的權限點碼；"*" 為超管通配（與後端 authz.Allowed 同語義）
    const buttons = authStore.userInfo.buttons;

    if (buttons.includes("*")) {
      return true;
    }

    if (typeof codes === "string") {
      return buttons.includes(codes);
    }

    return codes.some((code) => buttons.includes(code));
  }

  return {
    hasAuth,
  };
}
