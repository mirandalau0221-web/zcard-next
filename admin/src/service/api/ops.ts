// 運營支撐 API：審計日誌（audit:read）+ 貨幣管理（settings:currency_*）。

import { request } from "../request";

// ── 審計日誌 ──

export function fetchOpLogs(params?: { operator_id?: number; page?: number; page_size?: number }) {
  return request({ url: "/api/v1/admin/audit/op-logs", params });
}

export function fetchSecurityLogs(params?: { action?: string; page?: number; page_size?: number }) {
  return request({ url: "/api/v1/admin/audit/security-logs", params });
}

// ── 貨幣 ──

export function listCurrencies() {
  return request({ url: "/api/v1/admin/currencies" });
}

export function createCurrency(data: { code: string; symbol: string; position?: string; precision?: number; rate_json: string }) {
  return request({ url: "/api/v1/admin/currencies", method: "post", data });
}

export function updateCurrency(code: string, data: { symbol?: string; position?: string; precision?: number; rate_json?: string; enabled?: boolean; sort?: number }) {
  return request({ url: `/api/v1/admin/currencies/${code}`, method: "put", data });
}

export function deleteCurrency(code: string) {
  return request({ url: `/api/v1/admin/currencies/${code}`, method: "delete" });
}
