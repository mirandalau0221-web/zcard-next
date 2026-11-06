<script setup lang="ts">
// 賬單流水（全站錢包流水：user_id=0 拉全部用戶；歸屬用戶列 + 類型篩選 + 金額彙總）。
import { onMounted, ref, h } from "vue";
import { NDataTable, NInput, NSelect, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchWalletTransactions } from "@/service/api";
import TablePager from "@/components/common/table-pager.vue";
import { TRANSACTION_TYPES, transactionType, transactionAmount, transactionReference, transactionRemark, formatMoney, formatSignedMoney } from "@/utils/money";

defineOptions({ name: "WalletBillsTab" });

const loading = ref(false);
const bills = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
// 類型篩選（客戶端過濾當前頁；服務端 type 篩選參數暫未開放）
const typeFilter = ref<string | null>(null);

function directionText(d?: string) {
  return d === "in" ? "入賬" : d === "out" ? "出賬" : d || "-";
}

const typeOptions = Object.entries(TRANSACTION_TYPES).map(([value, label]) => ({ label, value }));

const filtered = () =>
  typeFilter.value ? bills.value.filter((b) => b.type === typeFilter.value) : bills.value;

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchWalletTransactions(0, {
      page: page.value,
      page_size: pageSize.value,
    });
    if (!error && data) {
      bills.value = (data as any).transactions || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    loading.value = false;
  }
}

// 當頁收支彙總（隨篩選聯動，給運營一眼對賬）
function sums() {
  let inCents = 0;
  let outCents = 0;
  for (const b of filtered()) {
    if (b.direction === "in") inCents += b.amount_cents || 0;
    else if (b.direction === "out") outCents += b.amount_cents || 0;
  }
  return { inCents, outCents };
}

function formatTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

const columns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 64 },
  {
    title: "用戶",
    key: "username",
    width: 140,
    render: (row) =>
      h("span", { title: `用戶 ID ${row.user_id}` }, row.username ? `${row.username}` : `#${row.user_id}`),
  },
  {
    title: "方向",
    key: "direction",
    width: 76,
    render: (row) =>
      h(
        NTag,
        { type: row.direction === "in" ? "success" : "error", size: "small", bordered: false },
        { default: () => directionText(row.direction) },
      ),
  },
  { title: "類型", key: "type", width: 132, render: (row) => transactionType(row.type) },
  {
    title: "金額",
    key: "amount_cents",
    width: 110,
    render: (row) => formatSignedMoney(transactionAmount(row)),
  },
  {
    title: "變動後餘額",
    key: "balance_after_cents",
    width: 110,
    render: (row) => formatMoney(row.balance_after_cents),
  },
  { title: "關聯單號", key: "reference", render: transactionReference, minWidth: 150, ellipsis: { tooltip: true } },
  { title: "備註", key: "remark", render: (row) => transactionRemark(row), minWidth: 120, ellipsis: { tooltip: true } },
  { title: "時間", key: "created_at", width: 160, render: (row) => formatTime(row.created_at) },
];

onMounted(load);
</script>

<template>
  <div>
    <div class="mb-12px flex flex-wrap items-center gap-12px">
      <NSelect
        v-model:value="typeFilter"
        :options="typeOptions"
        clearable
        placeholder="全部類型"
        class="w-140px"
      />
      <NInput readonly disabled class="w-0px hidden" />
      <span class="text-12px text-gray-400">
        當頁收入 <b class="text-success">{{ formatMoney(sums().inCents) }}</b> · 支出
        <b class="text-error">{{ formatMoney(sums().outCents) }}</b>
        · 共 {{ total }} 筆（充值/消費/退款/佣金/提現/調賬全量流水）
      </span>
    </div>
    <NDataTable :columns="columns" :data="filtered()" :loading="loading" :scroll-x="1200" :max-height="540" />
    <TablePager v-model:page="page" v-model:page-size="pageSize" :total="total" @change="load" />
  </div>
</template>
