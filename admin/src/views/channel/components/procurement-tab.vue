<script setup lang="ts">
import { orderStatusText } from "@/utils/order-status";
// 採購單管理（procurement:read / procurement:write）：上游拿貨單（客户購買 →
// 上游採購 → 卡密回填鏈路的運行軌跡）。狀態篩選 + 手動重試 / 轉人工。
import { h, computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { NInput, NButton, NDataTable, NTag, NPopconfirm, NModal, NCard, NSpin, NAlert, NDescriptions, NDescriptionsItem, NPagination } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchProcurement, fetchDeliveries, fetchProcurements, retryProcurement, markProcurementManual } from "@/service/api";
import { checkAuth } from "@/directives";
import { formatMoney } from "@/utils/money";
import ManualDeliverDialog from "@/views/order/components/manual-deliver-dialog.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";
import TablePager from "@/components/common/table-pager.vue";
import { useResponsiveTier, type TableTier } from "./use-responsive-tier";

defineOptions({ name: "ProcurementTab" });

const { te, t } = useI18n();

/** 狀態枚舉 → 按當前語言渲染業務名稱，未收錄的狀態回顯原值 */
function statusText(s?: string) {
  if (!s) return "-";
  if (s === "manual") return "待人工處理";
  const key = `procurement.status.${s}`;
  return te(key) ? t(key) : s;
}

const loading = ref(false);
const rows = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const statusFilter = ref("");
const route = useRoute();
const orderNoFilter = ref(String(route.query.order_no || ""));
let listRequest = 0;
watch(() => route.query.order_no, value => { orderNoFilter.value = String(value || ""); statusFilter.value = ""; onSearch(); });

const statusTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: statusText("pending"), value: "pending", type: "warning" as const },
  { label: statusText("submitted"), value: "submitted", type: "info" as const },
  { label: statusText("polling"), value: "polling", type: "info" as const },
  { label: statusText("fulfilled"), value: "fulfilled", type: "success" as const },
  { label: statusText("rejected"), value: "rejected", type: "error" as const },
  { label: statusText("refunded"), value: "refunded", type: "default" as const },
  { label: statusText("manual"), value: "manual", type: "warning" as const },
];

const statusTag: Record<string, "success" | "error" | "warning" | "info" | "default"> = {
  fulfilled: "success", rejected: "error", manual: "warning",
  submitted: "info", polling: "info", refunding: "warning", refunded: "default", pending: "warning",
};

const canRetry = () => checkAuth("procurement:write");

// ── 容器寬分檔（full ≥1080 / mid ≥720 / compact）：任意屏寬下操作列完整可見，不依賴橫向滾動 ──
const wrapRef = ref<HTMLElement | null>(null);
const { tier } = useResponsiveTier(wrapRef);

function fmtTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

async function load() {
  const seq = ++listRequest;
  loading.value = true;
  try {
    const { data, error } = await fetchProcurements({
      page: page.value,
      page_size: pageSize.value,
      status: statusFilter.value || undefined,
      order_no: orderNoFilter.value.trim() || undefined,
    });
    if (seq !== listRequest) return;
    if (!error && data) {
      rows.value = (data as any).orders || (data as any).procurements || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    if (seq === listRequest) loading.value = false;
  }
}

function onSearch() {
  page.value = 1;
  load();
}

async function handleRetry(row: any) {
  const { error } = await retryProcurement(row.id);
  if (!error) {
    window.$message?.success("處理完成，請核對最新採購和交付狀態");
    await load();
    if (detail.value?.id === row.id) await openDetail(row.id);
  }
}

async function handleManual(row: any) {
  const { error } = await markProcurementManual(row.id, "管理員手動轉人工，等待人工補發");
  if (!error) {
    window.$message?.success("已轉人工，請在採購詳情點擊人工補發完成處理");
    load();
    await openDetail(row.id);
  }
}

// ── 響應式列集：compact 只留 狀態/上游單號/操作；mid 去掉訂單項/渠道/卡密/重試；full 全列 ──
const orderNoCol = (tr: TableTier) => ({
  title: "上游單號",
  key: "upstream_order_id",
  minWidth: tr === "compact" ? 96 : 110,
  maxWidth: tr === "compact" ? 180 : 220,
  ellipsis: { tooltip: true },
  render: (row: any) => row.upstream_order_id || "-",
});

const errorCol = () => ({
  title: "處理説明",
  key: "last_error",
  minWidth: 140,
  maxWidth: 420,
  ellipsis: { tooltip: true },
  render: (row: any) => row.last_error || "-",
});

const needsRecovery = (row: any) => row.status === "fulfilled" && row.delivery_incomplete;
const retryable = (row: any) => ["pending", "submitted", "polling"].includes(row.status) || (needsRecovery(row) && Number(row.received_cards) > 0);
const manualable = (row: any) => ["pending", "submitted", "polling"].includes(row.status) || needsRecovery(row);

const actionsCol = () => ({
  title: "操作",
  key: "actions",
  width: 172,
  render: (row: any) =>
    h("div", { class: "flex flex-wrap items-center gap-4px" }, [
      h(NButton, { size: "tiny", type: "primary", quaternary: true, onClick: () => openDetail(row.id) }, { default: () => "查看" }),
      ["manual", "rejected", "refunding"].includes(row.status) && canDeliver()
        ? h(NButton, { size: "tiny", type: "primary", onClick: () => openDetail(row.id) }, { default: () => "處理" }) : null,
      retryable(row) && canRetry()
        ? h(NButton, { size: "tiny", type: "primary", quaternary: true, onClick: () => handleRetry(row) }, { default: () => Number(row.received_cards) > 0 ? "重試交付" : "查詢上游" })
        : null,
      manualable(row) && canRetry()
        ? h(NPopconfirm, { onPositiveClick: () => handleManual(row) }, { trigger: () => h(NButton, { size: "tiny", quaternary: true }, { default: () => "轉人工" }), default: () => "停止自動採購並轉人工？轉人工後需核實上游結果，再通過人工補發完成發貨。" })
        : null,
    ]),
});

const columns = computed<DataTableColumns<any>>(() => {
  const tr = tier.value;
  const cols: DataTableColumns<any> = [];
  if (tr !== "compact") cols.push({ title: "ID", key: "id", width: 48 });
  if (tr === "full") {
    // 訂單項 ID 為長數字（雪花位），定寬 + 懸停全文防換行
    cols.push({ title: "訂單項", key: "order_item_id", width: 110, render: (row: any) => h("span", { class: "whitespace-nowrap", title: String(row.order_item_id ?? "") }, String(row.order_item_id ?? "-")) });
    cols.push({ title: "渠道", key: "connection_id", width: 72, render: (row: any) => `#${row.connection_id}` });
  }
  cols.push({ title: "客户訂單號", key: "order_no", minWidth: 120, maxWidth: 190, ellipsis: { tooltip: true }, render: (row: any) => row.order_no || "—" });
  if (tr !== "compact") cols.push(orderNoCol(tr));
  cols.push({
    title: "狀態",
    key: "status",
    width: 84,
    render: (row: any) =>
      h(NTag, { size: "small", type: statusTag[row.status] || "default", bordered: false }, { default: () => needsRecovery(row) ? "交付異常" : statusText(row.status) }),
  });
  if (tr === "full") {
    // 數字列（卡密行數可達十萬級）加寬 + 右對齊，杜絕逐字換行
    cols.push({ title: "卡密行數", key: "received_cards", width: 88, align: "right" as const, render: (row: any) => h("span", { class: "whitespace-nowrap tabular-nums" }, String(row.received_cards ?? row.received_count ?? "-")) });
    cols.push({ title: "重試", key: "retry_count", width: 64, align: "right" as const, render: (row: any) => h("span", { class: "whitespace-nowrap tabular-nums" }, String(row.retry_count ?? 0)) });
  }
  if (tr !== "compact") cols.push(errorCol());
  if (tr !== "compact") cols.push({ title: "更新時間", key: "updated_at", width: 142, render: (row: any) => fmtTime(row.updated_at || row.created_at) });
  cols.push(actionsCol());
  return cols;
});

const showManualDeliver = ref(false);
const canDeliver = () => checkAuth('order:read') && checkAuth('order:deliver');
const canDeliverDetail = computed(() => detail.value && canDeliver() && ['manual','rejected','refunding'].includes(detail.value.status) && ['paid','fulfilling','partially_delivered'].includes(detail.value.order_status));
async function afterManualDeliver() {
  const id = detail.value?.id;
  await load();
  if (id) await openDetail(id);
}
const detailVisible = ref(false);
const detailLoading = ref(false);
const detail = ref<any>(null);
const detailError = ref('');
const deliveries = ref<any[]>([]);
const deliveryLoading = ref(false);
const deliveryError = ref('');
const deliveryPage = ref(1);
const deliveryTotal = ref(0);
let detailRequest = 0;
const canViewDelivery = () => checkAuth('order:view_delivery');
const money = (value?: number) => formatMoney(Number(value || 0));
function closeDetail() { ++detailRequest; detailVisible.value = false; detail.value = null; deliveries.value = []; }
async function openDetail(id: number) {
  const seq = ++detailRequest;
  detailVisible.value = true; detailLoading.value = true; detail.value = null;
  detailError.value = ''; deliveries.value = []; deliveryError.value = ''; deliveryTotal.value = 0; deliveryPage.value = 1;
  const { data, error } = await fetchProcurement(id);
  if (seq !== detailRequest) return;
  detailLoading.value = false;
  if (error || !data) { detailError.value = '讀取採購詳情失敗，請關閉後重試'; return; }
  detail.value = data;
  if (canViewDelivery()) void loadDelivery();
}
async function loadDelivery() {
  const seq = detailRequest;
  deliveryLoading.value = true; deliveryError.value = ''; deliveries.value = [];
  const { data, error } = await fetchDeliveries(detail.value.order_no, deliveryPage.value, 20, detail.value.order_item_id);
  if (seq !== detailRequest) return;
  deliveryLoading.value = false;
  if (error) { deliveryError.value = '讀取發貨內容失敗'; return; }
  deliveries.value = (data as any)?.deliveries || [];
  deliveryTotal.value = Number((data as any)?.total || 0);
}
onMounted(load);
</script>

<template>
  <div ref="wrapRef">
    <NModal :show="detailVisible" @update:show="(show) => { if (!show) closeDetail(); }">
      <NCard title="採購單詳情" closable class="procurement-detail" @close="closeDetail">
        <NSpin :show="detailLoading">
          <NAlert v-if="detailError" type="error">{{ detailError }}</NAlert>
          <template v-if="detail">
            <NAlert v-if="needsRecovery(detail)" type="warning" class="mb-12px">採購已返回結果，但此商品尚未完成交付。優先重試已有卡密的交付；需要換卡補發時，請先核實上游結果並轉人工。</NAlert>
            <div class="flex flex-wrap gap-8px mb-12px" v-if="canRetry()">
              <NButton v-if="retryable(detail)" @click="handleRetry(detail)">{{ Number(detail.received_cards) > 0 ? "重試交付" : "查詢上游" }}</NButton>
              <NPopconfirm v-if="manualable(detail)" @positive-click="handleManual(detail)"><template #trigger><NButton>轉人工</NButton></template>確認已核實上游扣款及出貨結果，需要人工處理？</NPopconfirm>
            </div>
            <NAlert v-if="detail.status === 'manual'" type="warning" class="mb-16px" title="等待人工處理">
              自動採購已停止，轉人工不代表訂單已發貨。請先核實上游是否已扣款或出貨，拿到卡密後點擊下方“人工補發”；買家繼續使用原訂單取貨。
            </NAlert>
            <div v-if="canDeliverDetail" class="mb-16px flex flex-wrap items-center gap-12px">
              <NButton type="primary" @click="showManualDeliver = true">人工補發</NButton>
              <span>將自動選中本採購單對應商品，提交後同步更新訂單與採購狀態。</span>
            </div>
            <p v-else-if="detail.status === 'manual' && canDeliver()">銷售訂單當前為{{ orderStatusText(detail.order_status) }}，無法補發。請先核對訂單狀態。</p>
            <p v-else-if="detail.status === 'manual' && !canDeliver()">當前賬號缺少訂單查看或發貨權限，請聯繫管理員處理。</p>
            <NDescriptions bordered label-placement="top" :column="tier === 'compact' ? 1 : 3">
              <NDescriptionsItem label="銷售訂單">{{ detail.order_no }} · {{ orderStatusText(detail.order_status) }}</NDescriptionsItem>
              <NDescriptionsItem label="採購狀態">{{ statusText(detail.status) }}</NDescriptionsItem>
              <NDescriptionsItem label="採購單號">{{ detail.id }}</NDescriptionsItem>
              <NDescriptionsItem label="商品" :span="2">{{ detail.product_name }}<template v-if="detail.sku_name"> / {{ detail.sku_name }}</template></NDescriptionsItem>
              <NDescriptionsItem label="數量">{{ detail.item_quantity }}</NDescriptionsItem>
              <NDescriptionsItem label="採購來源">{{ detail.connection_name || `渠道 #${detail.connection_id}（已不可用）` }} · {{ detail.connection_driver }}</NDescriptionsItem>
              <NDescriptionsItem label="上游地址" :span="2">{{ detail.connection_url || '—' }}</NDescriptionsItem>
              <NDescriptionsItem label="上游訂單">{{ detail.upstream_order_id || '尚未生成' }}</NDescriptionsItem>
              <NDescriptionsItem label="上游商品編號">{{ detail.upstream_product_code || '—' }}</NDescriptionsItem>
              <NDescriptionsItem label="下單時間">{{ fmtTime(detail.created_at) }}</NDescriptionsItem>
              <NDescriptionsItem label="銷售單價">{{ money(detail.sale_unit_cents) }}</NDescriptionsItem>
              <NDescriptionsItem label="商品小計">{{ money(detail.sale_amount_cents) }}</NDescriptionsItem>
              <NDescriptionsItem label="成交金額（優惠分攤）">{{ money(detail.allocated_sale_cents) }}</NDescriptionsItem>
              <NDescriptionsItem label="成本單價（下單快照）">{{ detail.cost_basis === 'order_snapshot' ? money(detail.cost_unit_cents) : '未記錄' }}</NDescriptionsItem>
              <NDescriptionsItem label="成本合計">{{ detail.cost_basis === 'order_snapshot' ? money(detail.cost_total_cents) : '未記錄' }}</NDescriptionsItem>
              <NDescriptionsItem label="預計毛利">{{ detail.cost_basis === 'order_snapshot' ? money(detail.profit_cents) : '成本缺失，暫無法計算' }}</NDescriptionsItem>
            </NDescriptions>
            <p class="text-12px text-gray-500">成本取下單時的商品成本快照；預計毛利未計退款、手續費及上游實際扣款差額。</p>
            <NAlert v-if="detail.last_error" type="warning" class="my-12px">{{ detail.last_error }}</NAlert>
            <h3>發貨內容</h3>
            <p v-if="!canViewDelivery()">當前賬號無查看發貨內容權限。</p>
            <NSpin v-else :show="deliveryLoading">
              <NAlert v-if="deliveryError" type="error">{{ deliveryError }} <NButton text @click="loadDelivery">重試</NButton></NAlert>
              <p v-else-if="!deliveryLoading && !deliveries.length">此採購商品暫無發貨記錄。</p>
              <div v-for="row in deliveries" :key="row.id" class="delivery-record"><pre>{{ row.content || row.content_masked || '內容已不可用' }}</pre><small>{{ fmtTime(row.delivered_at) }}</small></div>
              <NPagination v-if="deliveryTotal > 20" v-model:page="deliveryPage" :item-count="deliveryTotal" :page-size="20" :disabled="deliveryLoading" @update:page="loadDelivery" />
            </NSpin>
          </template>
        </NSpin>
      </NCard>
    </NModal>
    <ManualDeliverDialog v-model:show="showManualDeliver" :order-no="detail?.order_no || ''" :default-item-id="detail?.order_item_id" @delivered="afterManualDeliver" />
    <form class="mb-12px flex gap-8px" @submit.prevent="onSearch"><NInput v-model:value="orderNoFilter" clearable placeholder="輸入完整客户訂單號" aria-label="客户訂單號" /><NButton attr-type="submit" :loading="loading">查詢</NButton></form>
    <FilterTabs v-model:value="statusFilter" :options="statusTabs" class="mb-12px" @change="onSearch" />
    <NAlert v-if="!loading && orderNoFilter.trim() && !rows.length" type="info" class="mb-12px">未找到該客户訂單的採購單。請核對訂單號；若訂單已付款，可在訂單詳情核實商品後人工補發，系統巡檢也會為缺失的採購任務建立待人工處理記錄。</NAlert>
    <NDataTable :columns="columns" :data="rows" :loading="loading" size="small" :row-key="(r: any) => r.id" :max-height="540" :scroll-x="300" />
    <div class="mt-12px flex justify-end">
      <TablePager v-model:page="page" v-model:page-size="pageSize" :total="total" @change="load" />
    </div>
  </div>
</template>

<style scoped>
.procurement-detail { width: min(960px, calc(100vw - 24px)); max-height:90vh; overflow:auto; overflow-wrap:anywhere; }
.delivery-record { background:var(--n-color-embedded,#f5f7fa); padding:12px; margin:8px 0; border-radius:6px; }
.delivery-record pre { white-space:pre-wrap; overflow-wrap:anywhere; margin:0 0 8px; font-family:monospace; }
</style>
