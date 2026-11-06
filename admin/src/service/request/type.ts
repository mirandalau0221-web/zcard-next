import "axios";

declare module "axios" {
  interface AxiosRequestConfig {
    /** An expired request may be replayed only once. */
    authRetried?: boolean;
    /** 由更新頁實時提供等待狀態，僅供更新狀態 GET 的錯誤提示判定。 */
    expectedUpdateRestart?: () => boolean;
  }
}

export interface RequestInstanceState {
  /** the promise of refreshing token */
  refreshTokenPromise: Promise<boolean> | null;
  /** the request error message stack */
  errMsgStack: string[];
  [key: string]: unknown;
}
