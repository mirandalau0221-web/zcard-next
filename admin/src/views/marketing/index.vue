<script setup lang="ts">
// 營銷管理：會員等級 / 優惠券 / 秒殺促銷（內容管理已獨立為一級菜單 /content）。
import { NTabs, NTabPane, NCard } from "naive-ui";
import LevelsTab from "./components/levels-tab.vue";
import CouponsTab from "./components/coupons-tab.vue";
import LotteryTab from "./components/lottery-tab.vue";
import FlashTab from "./components/flash-tab.vue";
import { checkAuth } from "@/directives";

defineOptions({ name: "MarketingManagement", route: "marketing" });
</script>

<template>
  <div class="min-h-500px">
    <NCard title="營銷管理">
      <NTabs type="line">
        <NTabPane v-if="checkAuth('memberlevel:read')" name="levels" tab="會員等級">
          <LevelsTab />
        </NTabPane>
        <NTabPane v-if="checkAuth('coupon:read')" name="coupons" tab="優惠券">
          <CouponsTab />
        </NTabPane>
        <NTabPane v-if="checkAuth('coupon:read')" name="flash" tab="秒殺/促銷">
          <FlashTab />
        </NTabPane>
        <NTabPane v-if="checkAuth('lottery:read')" name="lottery" tab="抽獎活動"><LotteryTab /></NTabPane>
      </NTabs>
    </NCard>
  </div>
</template>
