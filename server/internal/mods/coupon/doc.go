// Package coupon 營銷模塊（M1 券 / M3 秒殺）。
//
// 表：coupons / flash_sales / promotions。使用校驗在價格管線中位置固定
// （會員折扣之後，§5.2）；訂單取消/退款返還券（可配）；秒殺與卡密鎖定同一把鎖防超賣。
package coupon
