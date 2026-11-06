<script setup lang="ts">
// 審計日誌（audit:read）：操作審計（誰在何時動了什麼）+ 安全審計（取貨/登錄/敏感操作）。
import { computed, onMounted, ref } from "vue";
import { NDataTable, NInput, NInputNumber, NButton, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchOpLogs, fetchSecurityLogs } from "@/service/api";

defineOptions({ name: "AuditTab" });

const opLoading = ref(false);
const opLogs = ref<any[]>([]);
const opPage = ref(1);
const operatorFilter = ref<number | null>(null);

const secLoading = ref(false);
const secLogs = ref<any[]>([]);
const secPage = ref(1);
const actionFilter = ref("");

const opColumns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 70 },
  {
    title: "操作者",
    key: "operator_id",
    width: 130,
    ellipsis: true,
    render: (row) =>
      row.operator_name ||
      (row.operator_id ? `${row.operator_type === "admin" ? "員工" : "用戶"}#${row.operator_id}` : "未知"),
  },
  { title: "權限點", key: "permission_point", width: 170, ellipsis: true },
  { title: "動作", key: "action", width: 70 },
  { title: "路由", key: "route", width: 220, ellipsis: true },
  { title: "IP", key: "ip", width: 120, render: (row) => row.ip || "-" },
  {
    title: "時間",
    key: "created_at",
    width: 160,
    render: (row) => (row.created_at ? new Date(row.created_at * 1000).toLocaleString() : "-"),
  },
];

const secColumns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 70 },
  {
    title: "主體",
    key: "actor_id",
    width: 130,
    ellipsis: true,
    render: (row) => row.actor_name || (row.actor_id ? `${row.actor_type}#${row.actor_id}` : row.actor_type || "-"),
  },
  {
    title: "動作",
    key: "action",
    width: 170,
    render: (row) => row.action,
  },
  { title: "IP", key: "ip", width: 130 },
  { title: "元數據", key: "metadata_json", minWidth: 200, ellipsis: true },
  {
    title: "時間",
    key: "created_at",
    width: 160,
    render: (row) => (row.created_at ? new Date(row.created_at * 1000).toLocaleString() : "-"),
  },
];

async function loadOp() {
  opLoading.value = true;
  try {
    const { data, error } = await fetchOpLogs({ operator_id: operatorFilter.value || undefined, page: opPage.value, page_size: 20 });
    if (!error && data) opLogs.value = (data as any).logs || (data as any).op_logs || [];
  } finally {
    opLoading.value = false;
  }
}

// 操作員選項：從已加載日誌去重（ID/姓名/類型一目瞭然，替代手填數字 ID）
const operatorOptions = computed(() => {
  const map = new Map<number, { label: string; value: number }>();
  for (const r of opLogs.value as any[]) {
    const id = r.operator_id;
    if (!id || map.has(id)) continue;
    const name = r.operator_name || `${r.operator_type === "admin" ? "員工" : "用戶"}#${id}`;
    map.set(id, { label: `${name}（#${id}）`, value: id });
  }
  return [...map.values()];
});

async function loadSec() {
  secLoading.value = true;
  try {
    const { data, error } = await fetchSecurityLogs({ action: actionFilter.value || undefined, page: secPage.value, page_size: 20 });
    if (!error && data) secLogs.value = (data as any).logs || (data as any).security_logs || [];
  } finally {
    secLoading.value = false;
  }
}

onMounted(() => {
  loadOp();
  loadSec();
});
</script>

<template>
  <div class="flex flex-col gap-16px">
    <div>
      <div class="mb-8px flex items-center gap-8px">
        <span class="text-13px font-500">操作審計（變更類管理操作）</span>
        <NSelect
          v-model:value="operatorFilter"
          size="small"
          filterable
          clearable
          placeholder="按操作員篩選"
          class="w-200px"
          :options="operatorOptions"
          @update:value="(opPage = 1), loadOp()"
        />
        <NButton size="tiny" @click="loadOp">刷新</NButton>
      </div>
      <NDataTable :columns="opColumns" :data="opLogs" :loading="opLoading" size="small"  :max-height="540" />
      <div class="mt-8px flex justify-end gap-8px">
        <NButton size="small" :disabled="opPage <= 1" @click="opPage--, loadOp()">上一頁</NButton>
        <NButton size="small" :disabled="opLogs.length < 20" @click="opPage++, loadOp()">下一頁</NButton>
      </div>
    </div>
    <div>
      <div class="mb-8px flex items-center gap-8px">
        <span class="text-13px font-500">安全審計（登錄/取貨/敏感操作）</span>
        <NInput v-model:value="actionFilter" size="small" placeholder="動作模糊搜索" class="w-160px" clearable @keyup.enter="(secPage = 1), loadSec()" />
        <NButton size="tiny" @click="loadSec">刷新</NButton>
      </div>
      <NDataTable :columns="secColumns" :data="secLogs" :loading="secLoading" size="small"  :max-height="540" />
      <div class="mt-8px flex justify-end gap-8px">
        <NButton size="small" :disabled="secPage <= 1" @click="secPage--, loadSec()">上一頁</NButton>
        <NButton size="small" :disabled="secLogs.length < 20" @click="secPage++, loadSec()">下一頁</NButton>
      </div>
    </div>
  </div>
</template>
