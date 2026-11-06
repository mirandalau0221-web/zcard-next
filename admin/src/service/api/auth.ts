import { request } from "../request";

/** 管理員登錄（Kratos：POST /api/v1/admin/auth/login；captcha_admin_login 開啓時帶驗證碼） */
export function fetchLogin(username: string, password: string, captcha?: { captcha_id?: string; captcha_code?: string; challenge?: string; totp_code?: string }) {
  return request<Api.Auth.LoginToken>({
    url: "/api/v1/admin/auth/login",
    method: "post",
    data: { username, password, ...captcha },
  });
}

/** 登錄圖形驗證碼（免鑑權；GET /api/v1/admin/auth/captcha/image） */
export function fetchAdminCaptchaImage() {
  return request<{ captcha_id: string; image_base64: string; length?: number; charset?: 'digit' | 'letter' | 'mixed' }>({
    url: "/api/v1/admin/auth/captcha/image",
  });
}

/** 登錄驗證碼開關（免鑑權；GET /api/v1/admin/auth/captcha-config） */
export function fetchAdminCaptchaConfig() {
  return request<{ enabled: boolean }>({
    url: "/api/v1/admin/auth/captcha-config",
  });
}

/** 獲取當前用戶信息（Kratos：GET /api/v1/admin/auth/profile） */
export function fetchGetUserInfo() {
  return request<Api.Auth.UserInfo>({
    url: "/api/v1/admin/auth/profile",
  });
}

/** 刷新令牌（Kratos：POST /api/v1/admin/auth/refresh） */
export function fetchRefreshToken(refreshToken: string) {
  return request<Api.Auth.LoginToken>({
    url: "/api/v1/admin/auth/refresh",
    method: "post",
    data: { refresh_token: refreshToken },
  });
}

/** 登出 */
export function fetchLogout(refreshToken: string) {
  return request({
    url: "/api/v1/admin/auth/logout",
    method: "post",
    data: { refresh_token: refreshToken },
  });
}

// ── 前台用戶管理（identity:user_read / identity:user_status）──

/** 用戶列表（關鍵詞/狀態/供貨商篩選，分頁；含等級/錢包/訂單/供貨聚合） */
export function fetchUsers(params?: {
  keyword?: string;
  status?: string;
  is_supplier?: boolean;
  page?: number;
  page_size?: number;
}) {
  return request<{ users: any[]; total: number }>({
    url: "/api/v1/admin/users",
    method: "get",
    params,
  });
}

/** 用戶詳情（聚合：等級/錢包/優惠券/供貨賬戶/邀請關係/最近訂單） */
export function fetchUserDetail(id: number) {
  return request<any>({ url: `/api/v1/admin/users/${id}` });
}

/** 新增用戶（identity:user_create，超管） */
export function createUser(data: { username: string; password: string; email?: string }) {
  return request<any>({ url: "/api/v1/admin/users", method: "post", data });
}

/** 重置用戶密碼（identity:user_reset_pwd，超管） */
export function resetUserPassword(id: number, newPassword: string) {
  return request({ url: `/api/v1/admin/users/${id}/password`, method: "put", data: { new_password: newPassword } });
}

/** 封禁/解封用戶（identity:user_status，超管專屬） */
export function setUserStatus(id: number, status: "active" | "banned") {
  return request<any>({
    url: `/api/v1/admin/users/${id}/status`,
    method: "post",
    data: { status },
  });
}
