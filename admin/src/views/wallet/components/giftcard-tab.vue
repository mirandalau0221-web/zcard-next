<script setup lang="ts">
// 禮品卡批次（giftcard:read / giftcard:write 超管專屬；明文碼僅創建時一次性返回）。
import { computed, h, onMounted, ref } from "vue";
import { NButton, NDataTable, NInput, NModal, NForm, NFormItem, NInputNumber } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchGiftcardBatches, createGiftcardBatch, deleteGiftcardBatch } from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, formatMoney } from "@/utils/money";

defineOptions({ name: "GiftcardTab" });

const loading = ref(false);
const batches = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const showCreate = ref(false);
const saving = ref(false);
const form = ref({ batch_no: "", name: "", amountYuan: 50, quantity: 10 });

// 明文碼一次性展示（鐵律 11：此後任何端點不可再取）
const showCodes = ref(false);
const codesBatch = ref("");
const plainCodes = ref<string[]>([]);

const deleteTarget = ref<any>(null);
const deleting = ref(false);

const columns = computed<DataTableColumns<any>>(() => [
  { title: "批次號", key: "batch_no", width: 170 },
  { title: "名稱", key: "name", width: 140, ellipsis: { tooltip: true } },
  { title: "面值", key: "amount_cents", width: 90, render: (row) => formatMoney(row.amount_cents) },
  { title: "數量", key: "quantity", width: 70 },
  { title: "已兌換", key: "redeemed", width: 70, render: (row) => row.redeemed ?? row.redeemed_count ?? "-" },
  {
    title: "創建時間",
    key: "created_at",
    width: 160,
    render: (row) => (row.created_at ? new Date(row.created_at * 1000).toLocaleString() : "-"),
  },
  ...(checkAuth("giftcard:write") ? [{
    title: "操作", key: "actions", width: 90, fixed: "right" as const,
    render: (row: any) => h(NButton, { size: "small", type: "error", secondary: true, onClick: () => { deleteTarget.value = row; } }, () => "刪除"),
  }] : []),
]);

async function handleDelete() {
  if (!deleteTarget.value || deleting.value) return;
  deleting.value = true;
  try {
    const { error } = await deleteGiftcardBatch(deleteTarget.value.id);
    if (error) return;
    deleteTarget.value = null;
    window.$message?.success("批次已刪除，未兌換禮品卡已作廢");
    if (batches.value.length === 1 && page.value > 1) page.value--;
    await load();
  } finally {
    deleting.value = false;
  }
}

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchGiftcardBatches(page.value);
    if (!error && data) {
      batches.value = (data as any).batches || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    loading.value = false;
  }
}

async function handleCreate() {
  if (!form.value.batch_no || !form.value.name || !form.value.quantity) return;
  saving.value = true;
  try {
    const { data, error } = await createGiftcardBatch({
      batch_no: form.value.batch_no,
      name: form.value.name,
      amount_cents: Math.round(form.value.amountYuan * 100),
      quantity: form.value.quantity,
    });
    if (!error && data) {
      codesBatch.value = form.value.batch_no;
      plainCodes.value = (data as any).codes || [];
      showCodes.value = true;
      showCreate.value = false;
      load();
    }
  } finally {
    saving.value = false;
  }
}

function copyCodes() {
  navigator.clipboard?.writeText(plainCodes.value.join("\n"));
  window.$message?.success("已複製到剪貼板");
}

onMounted(load);
</script>

<template>
  <div>
    <div class="mb-8px">
      <NButton v-if="checkAuth('giftcard:write')" size="small" type="primary" @click="showCreate = true">新建批次</NButton>
      <span class="ml-8px text-12px text-gray-400">兌換碼明文僅創建時一次性展示，庫內無明文（安全鐵律）</span>
    </div>
    <NDataTable :columns="columns" :data="batches" :loading="loading" size="small"  :max-height="540" :scroll-x="790" :row-key="(row: any) => row.id" />
    <div class="mt-8px flex items-center justify-end gap-8px">
      <NButton size="small" :disabled="page <= 1 || loading" @click="page--, load()">上一頁</NButton>
      <NButton size="small" :disabled="page * 20 >= total || loading" @click="page++, load()">下一頁</NButton>
    </div>

    <NModal :show="Boolean(deleteTarget)" preset="dialog" type="warning" title="刪除禮品卡批次"
      style="width: min(460px, calc(100vw - 32px))" :mask-closable="!deleting" :close-on-esc="!deleting" :closable="!deleting"
      @update:show="(show) => { if (!show && !deleting) deleteTarget = null; }">
      <p style="overflow-wrap: anywhere">確定刪除批次「{{ deleteTarget?.name }}」（{{ deleteTarget?.batch_no }}）？</p>
      <p>該批次會從列表移除，所有未兌換的禮品卡立即作廢，無法恢復。</p>
      <p>已兌換的餘額和流水記錄保留，批次號不可重複使用。</p>
      <template #action>
        <NButton :disabled="deleting" @click="deleteTarget = null">取消</NButton>
        <NButton type="error" :loading="deleting" @click="handleDelete">確認刪除</NButton>
      </template>
    </NModal>

    <NModal v-model:show="showCreate" preset="dialog" title="新建禮品卡批次" style="width: 440px">
      <NForm :model="form" label-placement="left" label-width="72">
        <NFormItem label="批次號" required>
          <NInput v-model:value="form.batch_no" placeholder="如 GC-2026-001" />
        </NFormItem>
        <NFormItem label="名稱" required>
          <NInput v-model:value="form.name" :placeholder="`如 50 ${currencyUnit()} 禮品卡`" />
        </NFormItem>
        <NFormItem :label="`面值(${currencyUnit()})`" required>
          <NInputNumber v-model:value="form.amountYuan" :min="0.01" :precision="2" class="w-full" />
        </NFormItem>
        <NFormItem label="數量" required>
          <NInputNumber v-model:value="form.quantity" :min="1" :max="10000" class="w-full" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showCreate = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="handleCreate">創建</NButton>
      </template>
    </NModal>

    <!-- 明文碼一次性展示 -->
    <NModal :show="showCodes" preset="dialog" :title="`批次 ${codesBatch} 兌換碼（僅此一次展示）`" style="width: 480px" :mask-closable="false">
      <NInput :value="plainCodes.join('\n')" type="textarea" :rows="8" readonly />
      <template #action>
        <NButton @click="copyCodes">複製全部</NButton>
        <NButton type="primary" @click="showCodes = false">我已妥善保存</NButton>
      </template>
    </NModal>
  </div>
</template>
