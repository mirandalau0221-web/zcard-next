<script setup lang="ts">
import TablePager from "@/components/common/table-pager.vue";
import { NTabs, NTabPane } from "naive-ui";
import { checkAuth } from "@/directives";
import WithdrawTab from "./components/withdraw-tab.vue";
import GiftcardTab from "./components/giftcard-tab.vue";
import BillsTab from "./components/bills-tab.vue";
import { useRoute } from "vue-router";
import { ref, reactive, h, watch } from "vue";
import { NTag, NSelect, NRadioGroup, NRadioButton, NRadio } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchWalletBalance, adjustWalletBalance, adjustWalletPoints, fetchWalletTransactions, fetchUsers, fetchCoupons, grantCoupon } from "@/service/api";
import { currencyUnit, transactionType, transactionAmount, transactionReference, transactionRemark, formatMoney, formatSignedMoney, yuanToFen } from "@/utils/money";

defineOptions({ name: "WalletManagement" });
const route = useRoute();
const activeTab = ref(route.query.tab === "withdraw" && checkAuth("wallet:withdraw") ? "withdraw" : "bills");
watch(() => route.query.tab, tab => { activeTab.value = tab === "withdraw" && checkAuth("wallet:withdraw") ? "withdraw" : "bills"; });

const userId = ref<number | null>(null);
const selectedUser = ref<any>(null);
const userOptions = ref<{ label: string; value: number }[]>([]);
const userLoading = ref(false);
const balance = ref<any>(null);
const balanceLoading = ref(false);
const loading = ref(false);
const adjusting = ref(false);
const showAdjust = ref(false);

const transactions = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);

const adjustTab = ref<"balance" | "points" | "coupon">("balance");
const adjustDir = ref<"add" | "sub">("add"); // 增/扣 單選（免手輸正負號）
const couponBatches = ref<any[]>([]);
const couponBatchId = ref<string | null>(null);
const couponCount = ref(1);
const couponLoading = ref(false);
const grantingCoupon = ref(false);
const adjustForm = reactive<{ amount_yuan: number | null; reason: string }>({
  amount_yuan: null,
  reason: "",
});

function directionText(d?: string) {
  if (!d) return "-";
  const map: Record<string, string> = {
    in: "入賬",
    out: "出賬",
    credit: "入賬",
    debit: "出賬",
    income: "入賬",
    expense: "出賬",
    add: "入賬",
    sub: "扣減",
    adjust_in: "入賬",
    adjust_out: "扣減",
  };
  return map[d] || d;
}

function directionType(d?: string): "success" | "error" | "default" {
  if (["in", "credit", "income", "add", "adjust_in"].includes(d || "")) return "success";
  if (["out", "debit", "expense", "sub", "adjust_out"].includes(d || "")) return "error";
  return "default";
}

function formatTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

const columns: DataTableColumns<any> = [
  {
    title: "方向",
    key: "direction",
    width: 80,
    render: (row) =>
      h(
        NTag,
        { type: directionType(row.direction), size: "small" },
        { default: () => directionText(row.direction) },
      ),
  },
  { title: "類型", key: "type", width: 132, render: (row) => transactionType(row.type) },
  {
    title: "金額",
    key: "amount_cents",
    width: 120,
    render: (row) => formatSignedMoney(transactionAmount(row)),
  },
  {
    title: "餘額前",
    key: "balance_before_cents",
    width: 120,
    render: (row) => formatMoney(row.balance_before_cents),
  },
  {
    title: "餘額後",
    key: "balance_after_cents",
    width: 120,
    render: (row) => formatMoney(row.balance_after_cents),
  },
  { title: "關聯單號", key: "reference", render: transactionReference, minWidth: 160 },
  { title: "備註", key: "remark", render: (row) => transactionRemark(row), minWidth: 140, ellipsis: { tooltip: true } },
  { title: "時間", key: "created_at", width: 160, render: (row) => formatTime(row.created_at) },
];

/** 遠程搜索用戶（用戶名/註冊郵箱模糊匹配），選中後以用戶 ID 查餘額/流水/調賬 */
async function onUserSearch(keyword: string) {
  if (!keyword.trim()) {
    userOptions.value = [];
    return;
  }
  userLoading.value = true;
  try {
    const { data, error } = await fetchUsers({ keyword: keyword.trim(), page: 1, page_size: 10 });
    if (!error && data) {
      userOptions.value = (data.users || []).map((u: any) => ({
        label: u.email ? `${u.username}（${u.email}）` : u.username,
        value: u.id,
        user: u,
      }));
    }
  } finally {
    userLoading.value = false;
  }
}

function onUserChange(_value: number | null, option: any) {
  selectedUser.value = option?.user || null;
}

async function loadBalance() {
  if (!userId.value) {
    window.$message?.warning("請搜索並選擇用戶（用戶名或郵箱）");
    return;
  }
  balanceLoading.value = true;
  try {
    const { data, error } = await fetchWalletBalance(userId.value);
    if (!error && data) {
      balance.value = data;
      page.value = 1;
      loadTransactions();
    } else {
      balance.value = null;
      transactions.value = [];
      total.value = 0;
    }
  } finally {
    balanceLoading.value = false;
  }
}

async function loadTransactions() {
  if (!userId.value) return;
  loading.value = true;
  try {
    const { data, error } = await fetchWalletTransactions(userId.value, {
      page: page.value,
      page_size: pageSize.value,
    });
    if (!error && data) {
      transactions.value = (data as any).transactions || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    loading.value = false;
  }
}

async function openAdjust() {
  adjustTab.value = "balance";
  adjustDir.value = "add";
  couponBatchId.value = null;
  couponCount.value = 1;
  showAdjust.value = true;
  if (!couponBatches.value.length) {
    couponLoading.value = true;
    try {
      const { data, error } = await fetchCoupons("unused", undefined, 1, 50);
      if (!error && data) {
        const seen = new Set<string>();
        const opts: any[] = [];
        for (const c of (data as any).coupons || []) {
          if (!seen.has(c.batch_id)) {
            seen.add(c.batch_id);
            opts.push({ label: `${c.name}（${c.batch_id}）`, value: c.batch_id });
          }
        }
        couponBatches.value = opts;
      }
    } finally {
      couponLoading.value = false;
    }
  }
}

async function handleAdjust() {
  if (!userId.value) return;
  if (adjustTab.value !== "coupon" && !adjustForm.reason.trim()) {
    window.$message?.warning("請填寫調整原因（用於審計）");
    return;
  }
  if (adjustTab.value === "balance") {
    const amountYuan = adjustForm.amount_yuan;
    if (amountYuan === null || amountYuan <= 0) {
      window.$message?.warning("請填寫金額");
      return;
    }
    // 增/扣單選 → 服務端正負語義（鐵律 15：元→分防浮點）
    const cents = yuanToFen(amountYuan) * (adjustDir.value === "sub" ? -1 : 1);
    adjusting.value = true;
    try {
      const { data, error } = await adjustWalletBalance(userId.value, {
        amount_cents: cents,
        reason: adjustForm.reason,
      });
      if (!error) {
        window.$message?.success(adjustDir.value === "add" ? "已增加餘額" : "已扣減餘額");
        showAdjust.value = false;
        Object.assign(adjustForm, { amount_yuan: null, reason: "" });
        balance.value = data || balance.value;
        page.value = 1;
        loadTransactions();
      }
    } finally {
      adjusting.value = false;
    }
    return;
  }
  if (adjustTab.value === "points") {
    const pts = adjustForm.amount_yuan;
    if (pts === null || pts <= 0 || !Number.isInteger(pts)) {
      window.$message?.warning("請填寫整數積分數量");
      return;
    }
    adjusting.value = true;
    try {
      const { error } = await adjustWalletPoints(userId.value, pts * (adjustDir.value === "sub" ? -1 : 1), adjustForm.reason);
      if (!error) {
        window.$message?.success(adjustDir.value === "add" ? "已增加積分" : "已扣減積分");
        showAdjust.value = false;
        Object.assign(adjustForm, { amount_yuan: null, reason: "" });
      }
    } finally {
      adjusting.value = false;
    }
    return;
  }
  // 優惠券贈送
  if (!couponBatchId.value) {
    window.$message?.warning("請選擇優惠券批次");
    return;
  }
  grantingCoupon.value = true;
  try {
    const { data, error } = await grantCoupon(couponBatchId.value, userId.value, couponCount.value || 1);
    if (!error) {
      window.$message?.success(`已贈送 ${(data as any)?.granted ?? 0} 張券`);
      showAdjust.value = false;
    }
  } finally {
    grantingCoupon.value = false;
  }
}
</script>

<template>
  <div class="min-h-500px flex-col gap-16px overflow-hidden">
    <NCard title="錢包管理" class="flex-1">
      <NTabs v-model:value="activeTab" type="line">
      <NTabPane name="bills" tab="賬單流水">
        <BillsTab />
      </NTabPane>
      <NTabPane name="balance" tab="餘額/流水">
      <!-- 查詢 -->
      <div class="mb-16px flex items-center gap-12px">
        <NSelect
          v-model:value="userId"
          :options="userOptions"
          :loading="userLoading"
          filterable
          remote
          clearable
          placeholder="搜索用戶名或註冊郵箱"
          class="w-300px"
          @search="onUserSearch"
          @update:value="onUserChange"
        />
        <NButton type="primary" :loading="balanceLoading" @click="loadBalance">查詢餘額</NButton>
        <NButton v-auth="'wallet:adjust'" :disabled="!balance" @click="openAdjust">調整（餘額/積分/券）</NButton>
        <span v-if="selectedUser" class="text-13px text-gray-500">
          當前用戶：#{{ selectedUser.id }} {{ selectedUser.username }}<template v-if="selectedUser.email">（{{ selectedUser.email }}）</template>
        </span>
      </div>

      <!-- 餘額卡片 -->
      <NGrid
        v-if="balance"
        :x-gap="16"
        :y-gap="16"
        cols="s:1 m:3"
        responsive="screen"
        class="mb-16px"
      >
        <NGi>
          <NCard size="small" :bordered="false">
            <div class="text-14px">可用餘額</div>
            <div class="mt-8px text-24px font-bold">{{ formatMoney(balance.available_cents) }}</div>
          </NCard>
        </NGi>
        <NGi>
          <NCard size="small" :bordered="false">
            <div class="text-14px">凍結餘額</div>
            <div class="mt-8px text-24px font-bold">{{ formatMoney(balance.locked_cents) }}</div>
          </NCard>
        </NGi>
        <NGi>
          <NCard size="small" :bordered="false">
            <div class="text-14px">總餘額</div>
            <div class="mt-8px text-24px font-bold">{{ formatMoney(balance.total_cents) }}</div>
          </NCard>
        </NGi>
      </NGrid>

      <!-- 流水 -->
      <NDataTable :columns="columns" :data="transactions" :loading="loading" :scroll-x="1100"  :max-height="540" />

      <TablePager
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="total"
        @change="loadTransactions"
      />
          </NTabPane>
      <NTabPane v-if="checkAuth('wallet:withdraw')" name="withdraw" tab="提現審核">
        <WithdrawTab />
      </NTabPane>
      <NTabPane v-if="checkAuth('giftcard:read')" name="giftcard" tab="禮品卡">
        <GiftcardTab />
      </NTabPane>
      </NTabs>
    </NCard>

    <!-- 調整彈窗：餘額 / 積分 / 優惠券（增扣單選，免手輸正負號） -->
    <NModal v-model:show="showAdjust" preset="dialog" title="賬戶調整" style="width: 520px">
      <NRadioGroup v-model:value="adjustTab" size="small" class="mb-16px">
        <NRadioButton value="balance">餘額</NRadioButton>
        <NRadioButton value="points">積分</NRadioButton>
        <NRadioButton value="coupon">贈送優惠券</NRadioButton>
      </NRadioGroup>
      <NForm label-placement="left" label-width="90">
        <template v-if="adjustTab !== 'coupon'">
          <NFormItem :label="adjustTab === 'balance' ? '方向' : '方向'">
            <NRadioGroup v-model:value="adjustDir">
              <NRadio value="add">增加</NRadio>
              <NRadio value="sub">扣減</NRadio>
            </NRadioGroup>
          </NFormItem>
          <NFormItem :label="adjustTab === 'balance' ? `金額（${currencyUnit()}）` : '積分數量'" required>
            <NInputNumber
              v-model:value="adjustForm.amount_yuan"
              class="w-full"
              :precision="adjustTab === 'balance' ? 2 : 0"
              :step="adjustTab === 'balance' ? 0.01 : 1"
              :min="0"
              :placeholder="adjustTab === 'balance' ? `要${adjustDir === 'add' ? '增加' : '扣減'}的金額` : `要${adjustDir === 'add' ? '增加' : '扣減'}的積分`"
            />
          </NFormItem>
          <NFormItem label="原因" required>
            <NInput v-model:value="adjustForm.reason" type="textarea" :rows="3" placeholder="必填，用於審計" />
          </NFormItem>
        </template>
        <template v-else>
          <NFormItem label="優惠券批次" required>
            <NSelect
              v-model:value="couponBatchId"
              :options="couponBatches"
              :loading="couponLoading"
              filterable
              placeholder="選擇批次（僅顯示未使用券的批次）"
            />
            <div v-if="!couponLoading && !couponBatches.length" class="mt-4px text-12px text-gray-400">
              暫無可贈送的批次——先到 營銷管理 → 優惠券 批量生成
            </div>
          </NFormItem>
          <NFormItem label="贈送數量" required>
            <NInputNumber v-model:value="couponCount" :min="1" :max="1000" class="w-full" />
          </NFormItem>
          <div class="text-12px text-gray-400">贈送後券立即出現在買家「我的優惠券」中</div>
        </template>
      </NForm>
      <template #action>
        <NButton @click="showAdjust = false">取消</NButton>
        <NButton
          v-auth="'wallet:adjust'"
          type="primary"
          :loading="adjustTab === 'coupon' ? grantingCoupon : adjusting"
          @click="handleAdjust"
        >
          {{ adjustTab === 'coupon' ? '贈送' : `確定${adjustDir === 'add' ? '增加' : '扣減'}` }}
        </NButton>
      </template>
    </NModal>
  </div>
</template>
