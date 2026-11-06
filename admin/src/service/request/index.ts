import type { AxiosResponse } from "axios";
import { createFlatRequest } from "@sa/axios";
import { useAuthStore } from "@/store/modules/auth";
import { getServiceBaseURL } from "@/utils/service";
import { getAuthorization, handleExpiredRequest, showErrorMsg } from "./shared";
import type { RequestInstanceState } from "./type";
import { suppressUpdateRestartError } from "./update-restart";
import { authResponseStatus, createSessionRecovery } from "./session-recovery";

const isHttpProxy = import.meta.env.DEV && import.meta.env.VITE_HTTP_PROXY === "Y";
const { baseURL } = getServiceBaseURL(import.meta.env, isHttpProxy);
const recoverSession = createSessionRecovery({
  authorization: getAuthorization,
  refresh: () => handleExpiredRequest(request.state),
  logout: () => useAuthStore().resetStore(),
});

/**
 * Kratos 後端請求客戶端：
 * - 成功：HTTP 2xx，響應體為 proto JSON（snake_case）
 * - 失敗：HTTP 4xx/5xx，響應體 { code, reason, message }
 * - 401：嘗試 refresh，失敗則登出
 */
export const request = createFlatRequest<App.Service.Response<any>, any, RequestInstanceState>(
  {
    baseURL,
    validateStatus: authResponseStatus,
    headers: { "Content-Type": "application/json" },
  },
  {
    defaultState: {
      errMsgStack: [],
      refreshTokenPromise: null,
    } as RequestInstanceState,
    transform(response: AxiosResponse) {
      return response.data as any;
    },
    async onRequest(config) {
      const Authorization = getAuthorization();
      if (Authorization) {
        Object.assign(config.headers, { Authorization });
      }
      // Kratos 要求 POST/PUT/PATCH 必須帶 JSON body：axios 對無 data 請求會移除
      // Content-Type → 後端 400 CODEC（unregister Content-Type）。統一補空對象。
      const method = (config.method || "").toLowerCase();
      if (["post", "put", "patch"].includes(method) && config.data == null) {
        config.data = {};
      }
      return config;
    },
    isBackendSuccess(response) {
      return response.status >= 200 && response.status < 300;
    },
    async onBackendFail(response: any, instance: any) {
      return recoverSession(response, instance);
    },
    onError(err: any) {
      if (err.config?.silentError && err.response?.status !== 401) return;
      if (suppressUpdateRestartError(err)) return;
      // Closing an import preview intentionally aborts its in-flight quotes.
      if (err.code === "ERR_CANCELED" && err.config?.params?.quote_code
        && /^\/api\/v1\/admin\/supply\/connections\/\d+\/preview$/.test(err.config?.url || "")) return;
      const msg = err.response?.data?.message || err.message || "網絡異常";
      showErrorMsg(request.state as RequestInstanceState, msg);
    },
  },
);
