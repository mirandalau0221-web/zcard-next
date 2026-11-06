// 貨源渠道 API（supply:read / supply:write）：連接 CRUD、探活、映射、同步任務、健康度。
// ：settings 攜帶 schedule（定時計劃）與限流參數；連接響應含調度錨點與熔斷狀態。

import { request } from "../request";

// ── 連接 ──

export function fetchSupplyConnections(params?: { page?: number; page_size?: number }) {
  return request({ url: "/api/v1/admin/supply/connections", params });
}

export function createSupplyConnection(data: {
  name: string;
  driver: string; // zcard | dujiao_next | acg_faka
  base_url: string;
  credentials: string; // 驅動結構 JSON 明文（僅此接口）
  callback_url?: string;
  retry_max?: number;
  retry_intervals?: string;
  exchange_rate?: number;
  price_markup_percent?: number;
  price_markup_amount?: number; // 固定加價（分）
  price_rounding_mode?: string;
  auto_sync_price?: boolean;
  stock_mode?: string;
  settings?: string;
}) {
  return request({ url: "/api/v1/admin/supply/connections", method: "post", data });
}

export function updateSupplyConnection(id: number, data: Record<string, unknown>) {
  return request({ url: `/api/v1/admin/supply/connections/${id}`, method: "put", data });
}

export function deleteSupplyConnection(id: number) {
  return request({ url: `/api/v1/admin/supply/connections/${id}`, method: "delete" });
}

export function pingSupplyConnection(id: number) {
  return request({ url: `/api/v1/admin/supply/connections/${id}/ping`, method: "post" });
}

// ── 映射 ──

export function fetchSupplyMappings(params: { connection_id?: number; page?: number; page_size?: number }) {
  return request({ url: "/api/v1/admin/supply/mappings", params });
}

export function upsertSupplyMapping(data: Record<string, unknown>) {
  return request({ url: "/api/v1/admin/supply/mappings", method: "post", data });
}

export function deleteSupplyMapping(id: number) {
  return request({ url: `/api/v1/admin/supply/mappings/${id}`, method: "delete" });
}

// ── 同步任務 ──

export function createSupplySyncTask(data: {
  connection_id: number;
  mode?: string; // full | incremental（默認 full；驅動不支持增量自動回落）
  scope?: string; // collect=採集（默認）| price=僅價格 | status=僅上下架與庫存
  force_reprice?: boolean;
}) {
  return request({ url: "/api/v1/admin/supply/sync-tasks", method: "post", data });
}

export function fetchSupplySyncTasks(params: { connection_id?: number; page?: number; page_size?: number }, silentError = false) {
  return request({ url: "/api/v1/admin/supply/sync-tasks", params, silentError });
}

export function cancelSupplySyncTask(id: number) {
  return request({ url: `/api/v1/admin/supply/sync-tasks/${id}/cancel`, method: "post" });
}

// ── 健康度 ──

export function fetchSupplyHealth() {
  return request({ url: "/api/v1/admin/supply/health" });
}

// ── 交互式導入（ D）──

export function previewSupplyProducts(connectionId: number, quoteCode?: string, signal?: AbortSignal, catalog?: { async?: boolean; snapshot_id?: string; refresh?: boolean; page?: number; page_size?: number }) {
  return request({ url: `/api/v1/admin/supply/connections/${connectionId}/preview`,
    params: { ...catalog, ...(quoteCode ? { quote_code: quoteCode } : {}) }, signal, silentError: true, timeout: quoteCode ? 25000 : catalog?.page ? 130000 : 15000 });
}

export function importSupplyProducts(
  connectionId: number,
  data: {
    codes: string[];
    request_key?: string;
    snapshot_id?: string;
    pricing_mode?: string; // percent | fixed | equal | pending
    markup_percent?: number;
    markup_amount_cents?: number;
    save_default?: boolean;
    category_map?: Record<string, number>;
    category_drafts?: { upstream_code: string; name: string; parent_id: number }[];
  },
) {
  return request({ url: `/api/v1/admin/supply/connections/${connectionId}/import`, method: "post", data, silentError: true, timeout: 60000 });
}

export function fetchSupplySyncTask(id: number) {
  return request({ url: `/api/v1/admin/supply/sync-tasks/${id}`, silentError: true });
}
export function fetchSupplyImportItems(id: number, params: { page: number; page_size: number; problems_only: boolean }) {
  return request({ url: `/api/v1/admin/supply/sync-tasks/${id}/items`, params, silentError: true });
}
export function retrySupplyImportTask(id: number, scope: "failed" | "stock" | "remaining", confirmConfig = false) {
  return request({ url: `/api/v1/admin/supply/sync-tasks/${id}/retry`, method: "post", data: { scope, confirm_config: confirmConfig } });
}
