// 與後端訂單狀態機對應；訂單列表、會員詳情和採購單使用同一套業務名稱。
const labels: Record<string, string> = {
  pending_payment: "待支付",
  paid: "已支付",
  fulfilling: "履約中",
  partially_delivered: "部分發貨",
  manual_pending: "待人工發貨",
  delivered: "已發貨",
  completed: "已完成",
  canceled: "已取消",
  expired: "已過期",
  refund_pending: "退款中",
  refunded: "已退款",
};

export function orderStatusText(status?: string): string {
  return status ? Object.prototype.hasOwnProperty.call(labels, status) ? labels[status] : "未知狀態" : "—";
}

export function orderStatusType(status?: string): "success" | "error" | "warning" | "info" | "default" {
  if (["paid", "delivered", "completed"].includes(status || "")) return "success";
  if (["canceled", "expired", "refunded"].includes(status || "")) return "error";
  if (["pending_payment", "manual_pending"].includes(status || "")) return "warning";
  return status && Object.prototype.hasOwnProperty.call(labels, status) ? "info" : "default";
}
