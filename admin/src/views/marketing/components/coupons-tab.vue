<script setup lang="ts">
// 優惠券管理：批次生成 / 列表（分頁+批次篩選）/ 勾選批量刪除 / 導出 CSV / 作廢 / 贈送（coupon:read / write）。
import { computed, onMounted, ref, h } from "vue";
import { NButton, NDataTable, NInput, NInputNumber, NModal, NForm, NFormItem, NPopconfirm, NSelect, NTag } from "naive-ui";
import type { DataTableColumns, DataTableRowKey } from "naive-ui";
import { fetchCoupons, createCouponBatch, disableCoupon, grantCoupon, deleteCoupons, exportCoupons } from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, formatMoney } from "@/utils/money";
import FilterTabs from "@/components/common/filter-tabs.vue";

defineOptions({ name: "CouponsTab" });

const loading = ref(false);
const coupons = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const statusFilter = ref<string>("");
const batchFilter = ref<string | null>(null);
const batches = ref<string[]>([]);
const checkedKeys = ref<DataTableRowKey[]>([]);

const showBatch = ref(false);
const saving = ref(false);
const batchForm = ref({ name: "", type: "fixed", valueYuan: 10, count: 10 });

const showGrant = ref(false);
const granting = ref(false);
const grantForm = ref({ batch_id: "", user_id: null as number | null, count: 1 });

const canWrite = () => checkAuth("coupon:write");

// 快捷篩選卡片（與狀態列 NTag 同色系）
const statusTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "未使用", value: "unused", type: "success" as const },
  { label: "已使用", value: "used", type: "default" as const },
  { label: "已作廢", value: "disabled", type: "error" as const },
];

const batchOptions = computed(() => batches.value.map((b) => ({ label: b, value: b })));

const columns: DataTableColumns<any> = [
  {
    type: "selection",
    disabled: (row) => row.status !== "unused",
  },
  { title: "批次", key: "batch_id", width: 190, ellipsis: true },
  { title: "名稱", key: "name", width: 120, ellipsis: true },
  {
    title: "類型",
    key: "type",
    width: 80,
    render: (row) => h(NTag, { size: "small", type: row.type === "fixed" ? "info" : "warning" }, { default: () => (row.type === "fixed" ? "滿減" : "折扣") }),
  },
  {
    title: "面值",
    key: "value",
    width: 96,
    render: (row) => (row.type === "fixed" ? formatMoney(row.value) : `${(row.value / 1000).toFixed(1)}折`),
  },
  { title: "券碼", key: "code", minWidth: 170, ellipsis: true },
  {
    title: "狀態",
    key: "status",
    width: 84,
    render: (row) => h(NTag, { size: "small", type: row.status === "unused" ? "success" : row.status === "used" ? "default" : "error" }, { default: () => ({ unused: "未使用", used: "已使用", disabled: "已作廢" } as any)[row.status] || row.status }),
  },
  {
    title: "操作",
    key: "actions",
    width: 200,
    render: (row) =>
      h("div", { class: "flex gap-4px" }, [
        checkAuth("coupon:write")
          ? h(NButton, { size: "tiny", onClick: () => ((grantForm.value = { batch_id: row.batch_id, user_id: null, count: 1 }), (showGrant.value = true)) }, { default: () => "贈送" })
          : null,
        row.status === "unused" && canWrite()
          ? h(NPopconfirm, { onPositiveClick: () => handleDelete([row.id], false) }, { trigger: () => h(NButton, { size: "tiny", type: "error", quaternary: true }, { default: () => "刪除" }), default: () => "刪除該券？僅未使用券可刪。" })
          : null,
        row.status === "unused" && canWrite()
          ? h(NPopconfirm, { onPositiveClick: () => handleDisable(row.batch_id) }, { trigger: () => h(NButton, { size: "tiny", quaternary: true }, { default: () => "作廢批次" }), default: () => "作廢該批次全部未使用券？" })
          : null,
      ]),
  },
];

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchCoupons(statusFilter.value || undefined, batchFilter.value || undefined, page.value, pageSize.value);
    if (!error && data) {
      coupons.value = (data as any).coupons || [];
      total.value = (data as any).total || 0;
      batches.value = (data as any).batches || [];
      checkedKeys.value = checkedKeys.value.filter((k) => coupons.value.some((c) => c.id === k));
    }
  } finally {
    loading.value = false;
  }
}

function resetPageAndLoad() {
  page.value = 1;
  load();
}

async function handleBatch() {
  if (!batchForm.value.name || !batchForm.value.count) return;
  saving.value = true;
  try {
    const { data, error } = await createCouponBatch({
      name: batchForm.value.name,
      type: batchForm.value.type,
      value: batchForm.value.type === "fixed" ? Math.round(batchForm.value.valueYuan * 100) : batchForm.value.valueYuan,
      count: batchForm.value.count,
    });
    if (!error) {
      window.$message?.success(`已生成 ${(data as any)?.count ?? batchForm.value.count} 張券`);
      showBatch.value = false;
      resetPageAndLoad();
    }
  } finally {
    saving.value = false;
  }
}

async function handleDisable(batchId: string) {
  const { error } = await disableCoupon(batchId);
  if (!error) {
    window.$message?.success("批次已作廢");
    load();
  }
}

/** 批量/單張刪除（僅未使用；byBatch=true 時 ids 為空走整批刪除） */
async function handleDelete(ids: number[], byBatch: boolean, batchId?: string) {
  const { data, error } = await deleteCoupons(ids, byBatch ? batchId : undefined);
  if (!error) {
    const deleted = (data as any)?.deleted ?? 0;
    if (deleted > 0) {
      window.$message?.success(`已刪除 ${deleted} 張券`);
    } else {
      window.$message?.warning("沒有可刪除的未使用券");
    }
    checkedKeys.value = [];
    load();
  }
}

/** 導出當前篩選（狀態 + 批次）全部券碼為 CSV 文件 */
async function handleExport() {
  const { data, error } = await exportCoupons(statusFilter.value || undefined, batchFilter.value || undefined);
  if (error || !data) return;
  const { filename, csv } = data as any;
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename || "coupons.csv";
  a.click();
  URL.revokeObjectURL(url);
}

async function handleGrant() {
  if (!grantForm.value.batch_id || !grantForm.value.user_id) return;
  granting.value = true;
  try {
    const { data, error } = await grantCoupon(grantForm.value.batch_id, grantForm.value.user_id, grantForm.value.count);
    if (!error) {
      window.$message?.success(`已贈送 ${(data as any)?.granted ?? 0} 張`);
      showGrant.value = false;
    }
  } finally {
    granting.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div>
    <div class="mb-8px flex flex-wrap items-center justify-between gap-8px">
      <div class="flex flex-wrap items-center gap-8px">
        <FilterTabs v-model:value="statusFilter" :options="statusTabs" size="small" @change="resetPageAndLoad" />
        <NSelect
          v-model:value="batchFilter"
          size="small"
          clearable
          filterable
          placeholder="按批次篩選"
          class="w-200px"
          :options="batchOptions"
          @update:value="resetPageAndLoad"
        />
        <span class="text-12px color-gray">共 {{ total }} 張</span>
      </div>
      <div class="flex items-center gap-8px">
        <NButton size="small" @click="handleExport">導出 CSV</NButton>
        <NPopconfirm v-if="canWrite() && checkedKeys.length" @positive-click="handleDelete(checkedKeys as number[], false)">
          <template #trigger>
            <NButton size="small" type="error">刪除選中（{{ checkedKeys.length }}）</NButton>
          </template>
          刪除選中的 {{ checkedKeys.length }} 張未使用券？已使用/已作廢的不可選也不會刪除。
        </NPopconfirm>
        <NButton v-if="canWrite()" size="small" type="primary" @click="showBatch = true">批量生成</NButton>
      </div>
    </div>
    <NDataTable
      v-model:checked-row-keys="checkedKeys"
      remote
      :columns="columns"
      :data="coupons"
      :loading="loading"
      size="small"
      :row-key="(row: any) => row.id"
      :max-height="480"
      :pagination="{
        page: page,
        pageSize: pageSize,
        itemCount: total,
        pageSizes: [20, 50, 100],
        showSizePicker: true,
        onChange: (p: number) => { page = p; load(); },
        onUpdatePageSize: (s: number) => { pageSize = s; page = 1; load(); },
      }"
    />

    <!-- 批量生成 -->
    <NModal v-model:show="showBatch" preset="dialog" title="批量生成優惠券" style="width: 460px">
      <NForm :model="batchForm" label-placement="left" label-width="88">
        <NFormItem label="名稱" required>
          <NInput v-model:value="batchForm.name" placeholder="如：新品滿減券" />
        </NFormItem>
        <NFormItem label="類型">
          <NSelect v-model:value="batchForm.type" :options="[{ label: '滿減（固定金額）', value: 'fixed' }, { label: '折扣（萬分比）', value: 'percent' }]" />
        </NFormItem>
        <NFormItem :label="batchForm.type === 'fixed' ? `面值(${currencyUnit()})` : '折扣(萬分比)'" required>
          <NInputNumber v-model:value="batchForm.valueYuan" :min="1" class="w-full" />
        </NFormItem>
        <NFormItem label="數量" required>
          <NInputNumber v-model:value="batchForm.count" :min="1" :max="10000" class="w-full" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showBatch = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="handleBatch">生成</NButton>
      </template>
    </NModal>

    <!-- 贈送指定用户 -->
    <NModal v-model:show="showGrant" preset="dialog" title="贈送優惠券" style="width: 420px">
      <NForm :model="grantForm" label-placement="left" label-width="72">
        <NFormItem label="批次">
          <NInput :value="grantForm.batch_id" disabled />
        </NFormItem>
        <NFormItem label="用户ID" required>
          <NInputNumber v-model:value="grantForm.user_id" :min="1" class="w-full" placeholder="users 表 ID" />
        </NFormItem>
        <NFormItem label="數量" required>
          <NInputNumber v-model:value="grantForm.count" :min="1" class="w-full" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showGrant = false">取消</NButton>
        <NButton type="primary" :loading="granting" @click="handleGrant">贈送</NButton>
      </template>
    </NModal>
  </div>
</template>
