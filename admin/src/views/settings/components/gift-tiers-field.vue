<script setup lang="ts">
// 充值贈送檔位可視化編輯器（recharge.gift_tiers / supplier_recharge.gift_tiers）：
// 結構 [{amount, gift_balance, gift_points?}]（金額一律分存儲，界面以元編輯）。
// 供貨檔位（supplier）無積分列；買家端檔位按鈕與支付到賬均按此展示/入賬。
import { ref, watch } from "vue";
import { NButton, NInputNumber } from "naive-ui";
import { currencyUnit } from "@/utils/money";

interface Tier {
  amountYuan: number | null;
  giftYuan: number | null;
  giftPoints: number | null;
}

const props = defineProps<{ value: string | null; supplier?: boolean }>();
const emit = defineEmits<{ (e: "update", v: Record<string, number>[] | null): void }>();

const tiers = ref<Tier[]>([]);

function parse(v: string | null) {
  const out: Tier[] = [];
  try {
    const arr = JSON.parse(v || "null");
    if (Array.isArray(arr)) {
      for (const t of arr) {
        out.push({
          amountYuan: Number(t.amount) > 0 ? Number(t.amount) / 100 : null,
          giftYuan: Number(t.gift_balance) > 0 ? Number(t.gift_balance) / 100 : null,
          giftPoints: Number(t.gift_points) > 0 ? Number(t.gift_points) : null,
        });
      }
    }
  } catch {
    /* 非法 JSON 按空處理 */
  }
  tiers.value = out;
}
parse(props.value);
watch(() => props.value, parse);

function push() {
  const arr = tiers.value
    .filter((t) => (t.amountYuan || 0) > 0)
    .sort((a, b) => (a.amountYuan || 0) - (b.amountYuan || 0))
    .map((t) => {
      const row: Record<string, number> = {
        amount: Math.round((t.amountYuan || 0) * 100),
        gift_balance: Math.round((t.giftYuan || 0) * 100),
      };
      if (!props.supplier) row.gift_points = Math.round(t.giftPoints || 0);
      return row;
    });
  emit("update", arr.length ? arr : null);
}

function addRow() {
  tiers.value.push({ amountYuan: null, giftYuan: null, giftPoints: null });
}

function removeRow(i: number) {
  tiers.value.splice(i, 1);
  push();
}
</script>

<template>
  <div class="gift-tiers-field w-full">
    <div v-if="tiers.length" class="flex flex-col gap-6px">
      <div class="gift-tier-grid text-12px text-gray-400" :class="{ supplier }">
        <span class="tier-input text-center">單筆充值滿</span>
        <span class="tier-input text-center">贈送餘額</span>
        <span v-if="!supplier" class="tier-input text-center">贈送積分</span>
        <span class="w-40px" />
      </div>
      <div v-for="(t, i) in tiers" :key="i" class="gift-tier-grid" :class="{ supplier }">
        <NInputNumber v-model:value="t.amountYuan" :show-button="false" size="medium" :min="0.01" :precision="2" class="tier-input" :placeholder="currencyUnit()" @update:value="push">
          <template #suffix>{{ currencyUnit() }}</template>
        </NInputNumber>
        <NInputNumber v-model:value="t.giftYuan" :show-button="false" size="medium" :min="0" :precision="2" class="tier-input" placeholder="0.00" @update:value="push">
          <template #suffix>{{ currencyUnit() }}</template>
        </NInputNumber>
        <NInputNumber v-if="!supplier" v-model:value="t.giftPoints" :show-button="false" size="medium" :min="0" class="tier-input" placeholder="0" @update:value="push">
          <template #suffix>分</template>
        </NInputNumber>
        <NButton size="tiny" quaternary type="error" class="w-40px" @click="removeRow(i)">刪</NButton>
      </div>
    </div>
    <div v-else class="mb-8px text-12px text-gray-400">
      未配置贈送——買家按實付金額到賬；添加檔位後買家充值頁會展示「充 X 送 Y」引導
    </div>
    <div class="mt-8px flex items-center gap-8px">
      <NButton size="tiny" dashed @click="addRow">+ 添加檔位</NButton>
      <span v-if="!supplier" class="text-11px text-gray-400">
        想按「累計充值」自動打折/升級？到 營銷管理 → 會員等級 設置升級閾值與折扣（與本檔位贈送可疊加）
      </span>
      <span v-else class="text-11px text-gray-400">供貨賬戶充值滿額贈送餘額，到賬 = 本金 + 贈送（採購提貨通用）</span>
    </div>
  </div>
</template>

<style scoped>
.gift-tiers-field { overflow-x: auto; padding-bottom: 4px; }
.gift-tier-grid { display: grid; grid-template-columns: repeat(3, minmax(170px, 1fr)) 40px; gap: 12px; align-items: center; min-width: 586px; margin-bottom: 8px; }
.gift-tier-grid.supplier { grid-template-columns: repeat(2, minmax(170px, 1fr)) 40px; min-width: 404px; }
.tier-input { width: 100%; min-width: 0; }
</style>
