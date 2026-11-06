import { request } from "../request";

// ── 支付渠道 ──

export function fetchChannels() {
  return request({ url: "/api/v1/admin/payment/channels" });
}

export function createChannel(data: {
  sort?: number;
  name: string;
  code: string;
  driver: string;
  config_json: string;
  fee?: number;
  fee_type?: string;
  icon?: string;
  methods_json?: string;
  enabled?: boolean;
}) {
  return request({
    url: "/api/v1/admin/payment/channels",
    method: "post",
    data,
  });
}

export function updateChannel(id: number, data: Record<string, any>) {
  return request({
    url: `/api/v1/admin/payment/channels/${id}`,
    method: "put",
    data,
  });
}

export function deleteChannel(id: number) {
  return request({
    url: `/api/v1/admin/payment/channels/${id}`,
    method: "delete",
  });
}

// ── 員工管理 ──

export function fetchAdmins() {
  return request({ url: "/api/v1/admin/admins" });
}

export function createAdmin(data: {
  username: string;
  password: string;
  nickname?: string;
  role_id: number;
}) {
  return request({
    url: "/api/v1/admin/admins",
    method: "post",
    data,
  });
}

export function updateAdmin(id: number, data: Record<string, any>) {
  return request({
    url: `/api/v1/admin/admins/${id}`,
    method: "put",
    data,
  });
}

export function toggleAdmin(id: number, enabled: boolean) {
  return request({
    url: `/api/v1/admin/admins/${id}/toggle`,
    method: "put",
    data: { enabled },
  });
}

export function resetAdminPassword(id: number, password: string) {
  return request({
    url: `/api/v1/admin/admins/${id}/reset-password`,
    method: "put",
    data: { password },
  });
}

export function resetAdminTOTP(id: number, proof: { password: string; code?: string; reason: string }) {
  return request({
    url: "/api/v1/admin/auth/totp/reset",
    method: "post",
    data: { admin_id: id, ...proof },
  });
}

export function deleteAdmin(id: number) {
  return request({
    url: `/api/v1/admin/admins/${id}`,
    method: "delete",
  });
}

// ── 角色管理 ──

export function fetchRoles() {
  return request({ url: "/api/v1/admin/authz/roles" });
}

export function fetchRole(id: number) {
  return request({ url: `/api/v1/admin/authz/roles/${id}` });
}

export function createRole(data: {
  name: string;
  code: string;
  description?: string;
  permissions?: string[];
}) {
  return request({
    url: "/api/v1/admin/authz/roles",
    method: "post",
    data,
  });
}

export function updateRole(id: number, data: Record<string, any>) {
  return request({
    url: `/api/v1/admin/authz/roles/${id}`,
    method: "put",
    data,
  });
}

export function deleteRole(id: number) {
  return request({
    url: `/api/v1/admin/authz/roles/${id}`,
    method: "delete",
  });
}

export function updateRolePermissions(id: number, permissions: string[]) {
  return request({
    url: `/api/v1/admin/authz/roles/${id}/permissions`,
    method: "put",
    data: { permissions },
  });
}

export function fetchPermissionTree() {
  return request({ url: "/api/v1/admin/authz/permissions" });
}

// ── 系統設置 ──

export function fetchSettings(group?: string) {
  return request({
    url: "/api/v1/admin/settings",
    method: "get",
    params: group ? { group } : undefined,
  });
}

export function updateSetting(group: string, key: string, valueJson: string) {
  return request({
    url: `/api/v1/admin/settings/${group}/${key}`,
    method: "put",
    data: { value_json: valueJson },
  });
}

// 批量更新（表單級保存；後端單事務原子寫入）
export function updateSettings(items: { group: string; key: string; value_json: string }[]) {
  return request<{ updated: number; admin_base_path?: string }>({
    url: "/api/v1/admin/settings",
    method: "put",
    data: { items },
  });
}

export function testEmail(data: { recipient: string; subject: string; content: string }) {
  return request({
    url: "/api/v1/admin/notify/email/test",
    method: "post",
    data,
    timeout: 30000,
    silentError: true,
  });
}

// 可用模板清單（WP 主題式選擇；settings.template.pc_template 等取值）
export interface TemplateItem {
  key: string;
  name: string;
  desc: string;
  preview: string;
  author: string;
  version: string;
}

export function fetchTemplates() {
  return request<{ templates: TemplateItem[] }>({
    url: "/api/v1/admin/settings/templates",
    method: "get",
  });
}

// 安裝主題（zip base64；服務端解壓校驗後原子落盤）
export function installTemplate(dataBase64: string) {
  return request<TemplateItem>({
    url: "/api/v1/admin/settings/templates/install",
    method: "post",
    data: { data_base64: dataBase64 },
  });
}

// ── 貨幣（；符號/位置/小數位——前端金額格式化統一取默認貨幣）──

export function fetchCurrencies() {
  return request({ url: "/api/v1/admin/currencies", method: "get" });
}

// ── 支付驅動元數據（：配置面動態表單渲染）──

// ── 支付單（payment:read_detail / payment:capture）──

export function fetchPayments(params: { status?: string; order_no?: string; cursor?: number; limit?: number; review_only?: boolean }) {
  return request({ url: "/api/v1/admin/payments", params });
}

export function capturePayment(id: number) {
  return request({ url: `/api/v1/admin/payments/${id}/capture`, method: "post", data: {} });
}

export function fetchDrivers() {
  return request({ url: "/api/v1/admin/payment/drivers" });
}

export function fetchFieldOptions(code: string, field: string, configJson: string) {
  return request({
    url: `/api/v1/admin/payment/drivers/${code}/field-options`,
    method: "get",
    params: { field, config_json: configJson },
  });
}
