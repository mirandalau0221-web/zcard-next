// Package affiliate 三級分銷模塊（M3）：歸因（註冊/下單綁定上級，三級鏈快照進訂單）、
// 佣金計算/凍結/確認（pending_confirm → available → withdrawn）、返利、退款逆向扣回。
//
// 表：affiliate_commissions（UNIQUE(order_id, tier) 防重發傭）；賬務紀律與 wallet 一致。
package affiliate
