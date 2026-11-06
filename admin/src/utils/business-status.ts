// 按業務域解釋狀態，避免相同英文在不同業務中被誤譯。
function label(labels: Record<string, string>, status?: string): string {
  if (!status) return "—";
  return Object.prototype.hasOwnProperty.call(labels, status) ? labels[status] : "未知狀態";
}
export const couponStatusText = (status?: string) => label({ unused: "未使用", used: "已使用", disabled: "已作廢" }, status);
export const supplierStatusText = (status?: string) => label({ applying: "待審核", approved: "已通過", rejected: "已駁回", disabled: "已禁用" }, status);
export const cardStatusText = (status?: string) => label({ available: "可用", reserved: "已鎖定", used: "已售出", disabled: "已禁用" }, status);
export const refundStatusText = (status?: string) => label({ created: "待處理", processing: "退款中", succeeded: "退款成功", failed: "退款失敗" }, status);
