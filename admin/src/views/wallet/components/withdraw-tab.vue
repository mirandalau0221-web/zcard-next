<script setup lang="ts">
// 提現審核/打款（wallet:withdraw 列表 / wallet:withdraw_review 超管專屬）。
import { useRoute } from "vue-router";
import { onMounted, ref, h, watch } from "vue";
import { NButton, NDataTable, NInput, NModal, NForm, NFormItem, NPopconfirm, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchWithdrawals, reviewWithdrawal, payWithdrawal } from "@/service/api";
import { checkAuth } from "@/directives";
import { formatMoney } from "@/utils/money";
import FilterTabs from "@/components/common/filter-tabs.vue";

defineOptions({ name: "WithdrawTab" });
const route = useRoute();

const loading = ref(false);
const showQr = ref(""); // 收款碼大圖

function fmtTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

function methodText(t: string) {
  return ({ alipay: "支付寶", wechat: "微信", usdt_trc20: "USDT TRC20", bank: "銀行轉賬" } as Record<string, string>)[t] || t;
}
const withdrawals = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const statusFilter = ref<string>(String(route.query.status || ""));
watch(() => route.query.status, status => { statusFilter.value = String(status || ""); page.value = 1; load(); });

const showReject = ref(false);
const rejecting = ref(false);
const rejectTarget = ref<any>(null);
const rejectReason = ref("");

const canReview = () => checkAuth("wallet:withdraw_review");

// 快捷篩選卡片（與 statusTag 同色系）
const statusTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "待審核", value: "pending", type: "warning" as const },
  { label: "已通過", value: "approved", type: "info" as const },
  { label: "已打款", value: "paid", type: "success" as const },
  { label: "已駁回", value: "rejected", type: "error" as const },
];

function statusTag(s: string) {
  const type = s === "paid" ? "success" : s === "pending" ? "warning" : s === "rejected" ? "error" : "info";
  const text = ({ pending: "待審核", approved: "已通過", paid: "已打款", rejected: "已駁回" } as any)[s] || s;
  return h(NTag, { type, size: "small" }, { default: () => text });
}

const columns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 60 },
  {
    title: "用户",
    key: "user_id",
    width: 110,
    render: (row) => row.username ? `#${row.user_id} ${row.username}` : `#${row.user_id}`,
  },
  {
    title: "金額",
    key: "amount_cents",
    width: 90,
    render: (row) => formatMoney(row.amount_cents),
  },
  { title: "手續費", key: "fee_cents", width: 84, render: (row) => formatMoney(row.fee_cents) },
  {
    title: "方式",
    key: "method_type",
    width: 110,
    render: (row) => row.method_name || methodText(row.method_type),
  },
  {
    title: "收款信息",
    key: "account",
    width: 190,
    render: (row) =>
      h("div", { class: "flex items-center gap-6px" }, [
        h("span", { class: "truncate", style: "max-width: 120px", title: row.account }, row.account || "-"),
        row.qr_code_url
          ? h(
              "a",
              { href: "#", onClick: (e: Event) => { e.preventDefault(); showQr.value = row.qr_code_url; } },
              [h("img", { src: row.qr_code_url, style: "width: 28px; height: 28px; object-fit: cover; border-radius: 4px; border: 1px solid #e5e7eb", alt: "收款碼" })],
            )
          : null,
      ]),
  },
  { title: "狀態", key: "status", width: 84, render: (row) => statusTag(row.status) },
  { title: "駁回原因", key: "reject_reason", width: 110, ellipsis: true },
  { title: "申請時間", key: "created_at", width: 140, render: (row) => fmtTime(row.created_at) },
  { title: "審核時間", key: "reviewed_at", width: 140, render: (row) => fmtTime(row.reviewed_at) },
  {
    title: "操作",
    key: "actions",
    width: 170,
    render: (row) =>
      h("div", { class: "flex gap-4px" }, [
        row.status === "pending" && canReview()
          ? h(NPopconfirm, { onPositiveClick: () => handleReview(row, true) }, { trigger: () => h(NButton, { size: "tiny", type: "success", secondary: true }, { default: () => "通過" }), default: () => "通過該提現申請？" })
          : null,
        row.status === "pending" && canReview()
          ? h(NButton, { size: "tiny", type: "warning", secondary: true, onClick: () => openReject(row) }, { default: () => "駁回" })
          : null,
        row.status === "approved" && canReview()
          ? h(NButton, { size: "tiny", type: "primary", secondary: true, onClick: () => openPay(row) }, { default: () => "確認打款" })
          : null,
      ]),
  },
];

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchWithdrawals({ status: statusFilter.value || undefined, page: page.value, page_size: 20 });
    if (!error && data) {
      withdrawals.value = (data as any).withdrawals || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    loading.value = false;
  }
}

async function handleReview(row: any, approve: boolean) {
  const { error } = await reviewWithdrawal(row.id, approve);
  if (!error) {
    window.$message?.success(approve ? "已通過（金額保持凍結待打款）" : "已駁回");
    load();
  }
}

// 打款 Modal（收款信息核對 + 回執填寫）
const payTarget = ref<any>(null);
const payReceipt = ref("");
const showPay = ref(false);
const paying = ref(false);

function openPay(row: any) {
  payTarget.value = row;
  payReceipt.value = "";
  showPay.value = true;
}

async function handlePay() {
  if (!payTarget.value) return;
  paying.value = true;
  try {
    const { error } = await payWithdrawal(payTarget.value.id, payReceipt.value.trim() || undefined);
    if (!error) {
      window.$message?.success("已確認打款，回執已記錄（客户可見）");
      showPay.value = false;
      load();
    }
  } finally {
    paying.value = false;
  }
}

function openReject(row: any) {
  rejectTarget.value = row;
  rejectReason.value = "";
  showReject.value = true;
}

async function handleReject() {
  if (!rejectTarget.value || !rejectReason.value.trim()) return;
  rejecting.value = true;
  try {
    const { error } = await reviewWithdrawal(rejectTarget.value.id, false, rejectReason.value.trim());
    if (!error) {
      window.$message?.success("已駁回（凍結金額解除）");
      showReject.value = false;
      load();
    }
  } finally {
    rejecting.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div>
    <div class="mb-8px flex flex-wrap items-center justify-between gap-8px">
      <FilterTabs v-model:value="statusFilter" :options="statusTabs" size="small" @change="(page = 1), load()" />
      <span class="text-12px text-gray-400">共 {{ total }} 單</span>
    </div>
    <NDataTable :columns="columns" :data="withdrawals" :loading="loading" size="small"  :max-height="540" />
    <div class="mt-8px flex items-center justify-end gap-8px">
      <NButton size="small" :disabled="page <= 1" @click="page--, load()">上一頁</NButton>
      <NButton size="small" :disabled="withdrawals.length < 20" @click="page++, load()">下一頁</NButton>
    </div>

    <NModal v-model:show="showReject" preset="dialog" :title="`駁回提現 #${rejectTarget?.id || ''}`" style="width: 420px">
      <NForm label-placement="top">
        <NFormItem label="駁回原因（必填，用户可見）" required>
          <NInput v-model:value="rejectReason" type="textarea" :rows="2" placeholder="如：賬户信息有誤" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showReject = false">取消</NButton>
        <NButton type="warning" :loading="rejecting" :disabled="!rejectReason.trim()" @click="handleReject">駁回</NButton>
      
<!-- 收款碼大圖 -->
<NModal :show="!!showQr" preset="card" title="收款二維碼" style="width: 360px" @update:show="(v: boolean) => !v && (showQr = '')">
  <div style="text-align: center">
    <img :src="showQr" style="width: 100%; border-radius: 8px" alt="收款碼" />
  </div>
</NModal>

<!-- 確認打款 Modal（收款核對 + 回執） -->
<NModal :show="showPay" preset="card" :title="`確認打款 #${payTarget?.id || ''}`" style="width: 440px" @update:show="(v: boolean) => (showPay = v)">
  <div v-if="payTarget" class="flex flex-col gap-10px">
    <div class="rounded-8px bg-#f8fafc px-12px py-10px text-13px">
      <div>提現金額：<b class="text-#ff5722">{{ formatMoney(payTarget.amount_cents) }}</b></div>
      <div class="mt-4px">收款方式：{{ payTarget.method_name || methodText(payTarget.method_type) }}</div>
      <div class="mt-4px break-all">收款賬號：{{ payTarget.account }}</div>
      <div class="mt-4px text-#d03050">請核對已線下轉賬完成後再確認</div>
    </div>
    <NFormItem label="打款回執" label-placement="top" :show-feedback="false">
      <NInput v-model:value="payReceipt" placeholder="交易流水號 / 轉賬備註（客户在提現記錄可見）" maxlength="100" />
    </NFormItem>
  </div>
  <template #action>
    <NButton @click="showPay = false">取消</NButton>
    <NButton type="primary" :loading="paying" @click="handlePay">確認已打款</NButton>
  </template>
</NModal>
</template>
    </NModal>
  </div>

<!-- 收款碼大圖 -->
<NModal :show="!!showQr" preset="card" title="收款二維碼" style="width: 360px" @update:show="(v: boolean) => !v && (showQr = '')">
  <div style="text-align: center">
    <img :src="showQr" style="width: 100%; border-radius: 8px" alt="收款碼" />
  </div>
</NModal>

<!-- 確認打款 Modal（收款核對 + 回執） -->
<NModal :show="showPay" preset="card" :title="`確認打款 #${payTarget?.id || ''}`" style="width: 440px" @update:show="(v: boolean) => (showPay = v)">
  <div v-if="payTarget" class="flex flex-col gap-10px">
    <div class="rounded-8px bg-#f8fafc px-12px py-10px text-13px">
      <div>提現金額：<b class="text-#ff5722">{{ formatMoney(payTarget.amount_cents) }}</b></div>
      <div class="mt-4px">收款方式：{{ payTarget.method_name || methodText(payTarget.method_type) }}</div>
      <div class="mt-4px break-all">收款賬號：{{ payTarget.account }}</div>
      <div class="mt-4px text-#d03050">請核對已線下轉賬完成後再確認</div>
    </div>
    <NFormItem label="打款回執" label-placement="top" :show-feedback="false">
      <NInput v-model:value="payReceipt" placeholder="交易流水號 / 轉賬備註（客户在提現記錄可見）" maxlength="100" />
    </NFormItem>
  </div>
  <template #action>
    <NButton @click="showPay = false">取消</NButton>
    <NButton type="primary" :loading="paying" @click="handlePay">確認已打款</NButton>
  </template>
</NModal>
</template>
