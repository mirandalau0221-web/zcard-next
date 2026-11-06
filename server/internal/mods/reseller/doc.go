// Package reseller 分站模塊（M3）：申請/審核、域名 DNS 驗證、白標配置、
// 分站定價（SKU>商品>分站默認）、分賬賬本、提現、分站等級權限位、
// 分站自營商品上架（出單段旅程首環）+ order.paid 分賬訂閲（reseller.settle）。
//
// 表：reseller_profiles / reseller_sites / reseller_pricing / reseller_ledger_entries /
// reseller_balance_accounts / reseller_related_accounts。
// 開源版隔離 = subsite_id 行級（Row 模式）；品牌隔離 fail-closed（絕不暴露主站品牌）。
package reseller
