package data

// Ent 租户攔截器（ 預留）：框架級 subsite_id 行級隔離。
// 落地：需要 ent feature intercept + runtime 註冊——當前 data 層的顯式條件
// （product.SubsiteID(tc.SubsiteID)）已保證隔離，攔截器作為 框架級強化項。
// 專項測試：兩租户數據互不可見（隨攔截器交付）。
//
// 接入計劃：
// 1. ent feature: intercept（生成 interceptors 包）
// 2. internal/data/ent/runtime.go 註冊 DomainInterceptor
// 3. 攔截器讀 tenancy.Context 自動追加過濾
// 4. 移除 data 層顯式條件（保留攔截器為唯一租户入口）
