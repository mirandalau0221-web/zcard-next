// Package supplier 對外供貨模塊（ 完整落地，本站作上游）：
// - Supply API v2 全端點：ping/categories/products/stock/orders 查創退（HMAC 四頭）
// - HMAC 鑑權中間件：±300s 時間窗 + nonce 防重放（DB UNIQUE）+ 雙口徑驗籤（常數時間）
// - 下單鏈路：downstream_order_no 冪等 → 供貨價（覆蓋價>基礎價）→ 賬本扣款
// （冪等鍵 supply_order:<downID>，餘額不足零流水）→ inventory.Reserve 鎖卡
// （與前台同一庫存池防超賣）→ MarkUsed 交付 → 卡密內存態返回 → 回調登記
// - 供貨賬本：append-only 流水 + balance_cache（對賬由流水重算）
// - 回調轉發：HTTPS 強制 + SSRF 防護（httpx）+ 四頭簽名 + 退避重試（死信可手動重發）
// - 下游管理：申請→審核→發 key（secret 只顯一次）→啓停/重置/定價/充值/流水
//
// 表：supplier_accounts / supplier_ledger_entries / supply_orders / supply_nonces /
// supplier_product_prices / downstream_callbacks。
// 協議對偶： zcard 適配器即本協議的客户端（eat your own dog food）。
package supplier
