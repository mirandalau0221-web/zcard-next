<script setup lang="ts">
import { ref, onMounted, h } from "vue";
import {
  NAlert,
  NButton,
  NDataTable,
  NInput,
  NSelect,
  NPagination,
  type DataTableColumns,
} from "naive-ui";
import { request } from "@/service/request";
import { checkAuth } from "@/directives";
const rows = ref<any[]>([]),
  loading = ref(false),
  error = ref(""),
  page = ref(1),
  total = ref(0),
  sales = ref(false),
  reason = ref(""),
  status = ref(""),
  orderNo = ref("");
const phases: Record<string, string> = {
  purchase: "採購確認中",
  query: "同步會話",
  refund: "零售退款中",
  review: "待人工核對",
  done: "已結束",
};
const options = Object.entries(phases).map(([value, label]) => ({
  value,
  label,
}));
const columns: DataTableColumns<any> = [
  { title: "訂單號", key: "order_no" },
  { title: "貨源", key: "connection_id" },
  {
    title: "任務狀態",
    key: "phase",
    render: (r) => phases[r.phase] || r.phase,
  },
  { title: "會話狀態", key: "state" },
  { title: "買家退款", key: "refund_status" },
  { title: "診斷", key: "last_error" },
  { title: "嘗試次數", key: "attempts" },
  {
    title: "操作",
    key: "actions",
    render: (r) =>
      r.phase !== "done" && checkAuth("procurement:write")
        ? h(
            NButton,
            {
              size: "small",
              disabled: loading.value,
              onClick: () => retry(r.id),
            },
            () => "恢復原任務",
          )
        : null,
  },
];
async function load() {
  loading.value = true;
  const r = await request<any>({
    url: "/api/v1/admin/sms-intents",
    params: {
      page: page.value,
      page_size: 20,
      status: status.value,
      order_no: orderNo.value,
    },
  });
  loading.value = false;
  error.value = r.error ? "接碼診斷加載失敗，請重試" : "";
  if (r.data) {
    rows.value = r.data.items || [];
    total.value = Number(r.data.total || 0);
    sales.value = !!r.data.sales_enabled;
  }
}
async function retry(id: string) {
  if (reason.value.trim().length < 3) {
    error.value = "請先填寫核對和重試原因（至少 3 字）";
    return;
  }
  const r = await request({
    url: `/api/v1/admin/sms-intents/${id}/retry`,
    method: "post",
    data: { reason: reason.value },
  });
  if (!r.error) await load();
}
onMounted(load);
</script>
<template>
  <div class="flex flex-col gap-4">
    <NAlert :type="sales ? 'info' : 'warning'"
      >接碼新購{{
        sales ? "已啓用" : "已關閉"
      }}。關閉新購不影響已有任務恢復。恢復原任務只重放已保存的購買或操作意圖。</NAlert
    ><NAlert v-if="error" type="error">{{ error }}</NAlert>
    <div class="flex flex-wrap gap-2">
      <NInput
        v-model:value="orderNo"
        placeholder="零售訂單號"
        class="max-w-64"
      /><NSelect
        v-model:value="status"
        :options="[{ label: '全部', value: '' }, ...options]"
        class="max-w-48"
      /><NButton
        :loading="loading"
        @click="
          page = 1;
          load();
        "
        >查詢</NButton
      >
    </div>
    <NInput
      v-if="checkAuth('procurement:write')"
      v-model:value="reason"
      placeholder="恢復任務前的核對説明（3–180 字）"
      :maxlength="180"
    /><NDataTable
      :columns="columns"
      :data="rows"
      :loading="loading"
      :scroll-x="950"
      :row-key="(r) => r.id"
    /><NPagination
      v-model:page="page"
      :page-size="20"
      :item-count="total"
      @update:page="load"
    />
  </div>
</template>
