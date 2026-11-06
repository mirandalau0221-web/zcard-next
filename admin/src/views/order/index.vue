<script setup lang="ts">
import PhysicalOrderPanel from "./components/physical-order-panel.vue";
import { shippingStatus } from "../../../../packages/shipping";
import OrderAnswers from "@/components/common/order-answers.vue";
import { orderStatusText as statusText, orderStatusType as statusType } from "@/utils/order-status";
import TablePager from "@/components/common/table-pager.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";
import { useRoute, useRouter } from "vue-router";
import { ref, reactive, onMounted, h, watch, computed } from "vue";
import {
  NButton,
  NTag,
  NSpace,
  NPopconfirm,
  NModal,
  NDescriptions,
  NDescriptionsItem,
  NDivider,
  NSelect,
} from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useAppStore } from "@/store/modules/app";
import { checkAuth } from "@/directives";
import {
  fetchOrders,
  fetchOrder,
  cancelOrder,
  deleteOrders,
  createRefund,
  manualDeliver,
  fetchDeliveries,
} from "@/service/api";
import ManualDeliverDialog from "./components/manual-deliver-dialog.vue";
import PendingDeliverTab from "./components/pending-deliver-tab.vue";
import { currencyUnit, formatMoney, formatSignedMoney } from "@/utils/money";

defineOptions({ name: "OrderManagement" });
const route = useRoute();
const router = useRouter();
const reportFilter = computed(() => ({
  product_id: Number(route.query.product_id) || undefined,
  start_time: Number(route.query.start_time) || undefined,
  end_time: Number(route.query.end_time) || undefined,
  time_field: String(route.query.time_field || "created"),
  channel_id: Number(route.query.channel_id) || undefined,
  channel_code: String(route.query.channel_code || "") || undefined,
}));
const fromDashboard = computed(() => Boolean(reportFilter.value.start_time || route.query.status));

const loading = ref(false);
const orders = ref<any[]>([]);
const page = ref(1);
const pageSize = ref(20);
// 遊標鏈：cursors[p-1] = 第 p 頁起始遊標（next_cursor = 滿頁時末行 ID；0 = 無更多）
const cursors = ref<number[]>([0]);
const hasMore = ref(false);
const shippingFilter = ref("");
const statusFilter = ref<string>(String(route.query.status || ""));
const keywordInput = ref("");
const keyword = ref("");
const selectedOrders = ref<string[]>([]);
const deleting = ref(false);
let listRequest = 0;
const showDetail = ref(false);
const showManualDeliver = ref(false);
const pendingRefreshKey = ref(0);
async function afterManualDeliver() {
  if (detail.value) await handleDetail(detail.value.order_no);
  await loadOrders();
  pendingRefreshKey.value++;
}
const detail = ref<{
  [key: string]: any;
  order_no: string;
  status: string;
  total_cents: number;
  refunded_cents?: number;
  paid_fee_cents?: number;
  refunded_fee_cents?: number;
  cost_cents: number;
  expired_at?: number;
  expiry_retry_at?: number;
  expiry_review?: boolean;
  expiry_reason?: string;
  contact?: string;
  guest_contact?: string;
  client_ip?: string;
  items?: {
    id?: number;
    name?: string;
    sku_name?: string;
    form_answers_json?: string;
    assigned_admin_id?: number;
    is_self?: boolean;
    upstream_source_name?: string;
    upstream_driver?: string;
    upstream_product_code?: string;
    upstream_url?: string;
  }[];
  amount_lines?: any[];
  status_events?: any[];
} | null>(null);

// 快捷篩選卡片（statusType 同色系：成功綠/待付黃/取消紅/其餘藍）
const statusTabs = [
  { label: "待完成發貨", value: "needs_delivery", type: "info" as const },
  { label: "待處理退款", value: "needs_refund", type: "warning" as const },
  { label: "全部", value: "", type: "default" as const },
  { label: "待支付", value: "pending_payment", type: "warning" as const },
  { label: "已支付", value: "paid", type: "success" as const },
  { label: "履約中", value: "fulfilling", type: "info" as const },
  { label: "已發貨", value: "delivered", type: "success" as const },
  { label: "已完成", value: "completed", type: "success" as const },
  { label: "已取消", value: "canceled", type: "error" as const },
  { label: "已過期", value: "expired", type: "error" as const },
  { label: "已退款", value: "refunded", type: "error" as const },
];

// ── 枚舉翻譯（ 修復：業務名稱化，未知值回顯原值）──

function fulfillmentTypeText(t: string) {
  const map: Record<string, string> = {
    auto: "自動發貨",
    manual: "手動發貨",
    upstream: "上游代發",
    shipping: "快遞配送",
  };
  return map[t] || t || "-";
}

function fulfillmentStatusText(s: string) {
  const map: Record<string, string> = {
    pending: "待處理",
    shipped: "已寄出",
    received: "已收貨",
    refunded: "已取消交付",
    delivering: "履約中",
    manual: "待人工補發",
    delivered: "已履約",
    failed: "履約失敗",
  };
  return map[s] || s || "-";
}

function amountTypeText(t: string) {
  const map: Record<string, string> = {
    shipping: "運費",
    base_price: "商品價",
    sku_adjust: "SKU 加價",
    member_discount: "會員折扣",
    group_discount: "團購折扣",
    promo_discount: "促銷折扣",
    coupon_discount: "優惠券折扣",
    points_discount: "積分抵扣",
    subsite_markup: "分站加價",
    fee: "手續費",
    tax: "税費",
    rounding_adjust: "取整調整",
  };
  return map[t] || t;
}

function sourceTypeText(s: string) {
  const map: Record<string, string> = {
    member_level: "會員等級",
    member_group: "會員分組",
    coupon: "優惠券",
    flash_sale: "限時秒殺",
    points: "積分",
    subsite: "分站",
    manual: "手動",
    gift_card: "禮品卡",
  };
  return map[s] || s || "-";
}

// 狀態事件節點顏色（橫向時間線：成功綠/取消紅/其他藍）
function eventDotColor(evt: any): string {
  if (["paid", "delivered", "completed", "fulfilled"].includes(evt.event)) return "#18a058";
  if (["canceled", "expired", "refunded"].includes(evt.event)) return "#d03050";
  return "#2080f0";
}

// 事件排序（時間升序——後端已排，防禦性再排一次）
function sortedEvents(evts: any[]) {
  return [...(evts || [])]
    .map((evt) => {
      if (evt.event === "completed" && evt.from_status && evt.from_status !== "delivered")
        return {
          ...evt,
          event: "fetch_pending",
          to_status: evt.from_status,
          reason: "舊版取貨事件誤記完成，以訂單當前狀態為準",
        };
      if (
        evt.event === "delivered" &&
        ["fulfilling", "partially_delivered"].includes(evt.to_status)
      )
        return { ...evt, event: evt.to_status };
      return evt;
    })
    .sort((a, b) => (a.created_at || 0) - (b.created_at || 0));
}

function operatorText(op: string) {
  const map: Record<string, string> = {
    system: "系統",
    user: "用戶",
    admin: "管理員",
    worker: "工單處理",
  };
  return map[op] || op || "-";
}

function eventText(evt: string) {
  const map: Record<string, string> = {
    fulfillment_progress: "履約進度更新",
    shipment_created: "快遞發貨",
    shipment_corrected: "快遞信息更正",
    shipping_address_changed: "收貨地址更正",
    shipment_received: "確認收貨",
    return_restocked: "退貨入庫",
    return_registered: "退貨登記",
    item_refund: "商品退款 / 取消",
    created: "創建訂單",
    paid: "支付成功",
    fulfilled: "履約完成",
    delivered: "發貨完成",
    completed: "訂單完成",
    fetch_pending: "取貨查詢（待發貨）",
    manual_delivered: "人工補發",
    fulfillment_manual: "轉人工處理",
    service_started: "開始處理服務",
    fulfilling: "等待發貨",
    partially_delivered: "部分發貨",
    canceled: "訂單取消",
    expired: "訂單過期",
    expiry_check: "超時核對",
    payment_review: "到賬待核對",
    refund_requested: "申請退款",
    refund_partial: "部分退款到賬",
    refunded: "退款完成",
  };
  return map[evt] || evt;
}

function hasDiscount(detail: any) {
  return (detail?.amount_lines || []).some((l: any) => (l.amount_cents || 0) < 0);
}

function formatTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

const columns: DataTableColumns<any> = [
  { type: "selection", disabled: (row) => !canDelete(row) },
  { title: "訂單號", key: "order_no", width: 200, ellipsis: { tooltip: true } },
  {
    title: "訂單商品",
    key: "items",
    minWidth: 240,
    render: (row) =>
      row.items?.length
        ? h(
            "div",
            { class: "order-product-names" },
            row.items.map((it: any) =>
              h(
                "div",
                {},
                `${it.name || `商品 #${it.product_id}`}${it.sku_name ? ` · ${it.sku_name}` : ""} ×${it.quantity}`,
              ),
            ),
          )
        : "—",
  },
  {
    title: "狀態",
    key: "status",
    width: 100,
    render: (row) =>
      h(
        NTag,
        { type: statusType(row.status), size: "small" },
        { default: () => statusText(row.status) },
      ),
  },
  {
    title: "配送狀態",
    key: "shipping_status",
    width: 120,
    render: (row) => (row.commerce_version === 1 ? shippingStatus(row.shipping_status) : "—"),
  },
  {
    title: "總額",
    key: "total_cents",
    width: 100,
    render: (row) => formatMoney(row.total_cents),
  },
  {
    title: "聯繫方式",
    key: "contact",
    width: 140,
    ellipsis: { tooltip: true },
    render: (row) => row.contact || row.guest_contact || "-",
  },
  {
    title: "創建時間",
    key: "created_at",
    width: 160,
    render: (row) => formatTime(row.created_at),
  },
  {
    title: "操作",
    key: "actions",
    width: 140,
    render: (row) =>
      h(
        NSpace,
        { size: "small" },
        {
          default: () => [
            h(
              NButton,
              { size: "small", onClick: () => handleDetail(row.order_no) },
              { default: () => "詳情" },
            ),
            row.status === "pending_payment" && checkAuth("order:cancel")
              ? h(
                  NPopconfirm,
                  { onPositiveClick: () => handleCancel(row.order_no) },
                  {
                    trigger: () =>
                      h(NButton, { size: "small", type: "warning" }, { default: () => "取消" }),
                    default: () => "確定取消該訂單？",
                  },
                )
              : null,
            canDelete(row)
              ? h(
                  NPopconfirm,
                  { onPositiveClick: () => handleDelete([row.order_no]) },
                  {
                    trigger: () =>
                      h(
                        NButton,
                        { size: "small", type: "error", disabled: deleting.value },
                        { default: () => "刪除" },
                      ),
                    default: () => "從管理列表刪除該訂單？關聯支付及審計記錄將保留。",
                  },
                )
              : null,
          ],
        },
      ),
  },
];

const itemColumns: DataTableColumns<any> = [
  {
    title: "商品",
    key: "name",
    width: 180,
    ellipsis: { tooltip: true },
    render: (row) =>
      (row.name || `#${row.product_id}`) + (row.sku_name ? `（${row.sku_name}）` : ""),
  },
  {
    title: "數量 / 配送",
    key: "quantity",
    width: 150,
    render: (row) => h("div", {}, [
      h("div", {}, `購買 ${row.quantity}`),
      ...(Number(row.canceled_quantity) > 0 ? [h("div", { class: "text-12px text-gray-500" }, `已取消 ${row.canceled_quantity}`)] : []),
      ...(row.goods_type === "physical" ? [
        h("div", { class: "text-12px text-gray-500" }, `已寄 ${row.shipped_quantity || 0} · 已收 ${row.received_quantity || 0}`),
        ...(Number(row.returned_quantity) > 0 ? [h("div", { class: "text-12px text-gray-500" }, `退回入庫 ${row.returned_quantity}`)] : []),
      ] : []),
    ]),
  },
  {
    title: "單價",
    key: "unit_price_cents",
    width: 90,
    render: (row) => formatMoney(row.unit_price_cents),
  },
  { title: "小計", key: "amount_cents", width: 90, render: (row) => formatMoney(row.amount_cents) },
  { title: "成本", key: "cost_cents", width: 90, render: (row) => formatMoney(row.cost_cents) },
  {
    title: "來源",
    key: "is_self",
    width: 80,
    render: (row) =>
      h(
        NTag,
        { size: "small", type: row.is_self ? "success" : "warning", bordered: false },
        { default: () => (row.is_self ? "自營" : "上游") },
      ),
  },
  {
    title: "上游渠道",
    key: "upstream_source_name",
    width: 120,
    ellipsis: { tooltip: true },
    render: (row) => row.upstream_source_name || "-",
  },
  {
    title: "履約",
    key: "fulfillment",
    width: 150,
    render: (row) =>
      h(
        NSpace,
        { size: 4 },
        {
          default: () => [
            h(
              NTag,
              { size: "small", bordered: false },
              { default: () => fulfillmentTypeText(row.fulfillment_type) },
            ),
            h(
              NTag,
              {
                size: "small",
                bordered: false,
                type: row.fulfillment_status === "delivered" ? "success" : "default",
              },
              { default: () => fulfillmentStatusText(row.fulfillment_status) },
            ),
          ],
        },
      ),
  },
];

const amountColumns: DataTableColumns<any> = [
  { title: "#", key: "seq", width: 40 },
  { title: "類型", key: "type", width: 140, render: (row) => amountTypeText(row.type) },
  {
    title: "金額",
    key: "amount_cents",
    width: 90,
    render: (row) =>
      h(
        "span",
        { style: { color: (row.amount_cents || 0) < 0 ? "#18a058" : undefined } },
        { default: () => formatSignedMoney(row.amount_cents) },
      ),
  },
  {
    title: "來源",
    key: "source_type",
    width: 100,
    render: (row) => sourceTypeText(row.source_type),
  },
];

async function loadOrders() {
  const requestId = ++listRequest;
  loading.value = true;
  selectedOrders.value = [];
  try {
    const cur = cursors.value[page.value - 1] || 0;
    const { data, error } = await fetchOrders({
      ...reportFilter.value,
      status: statusFilter.value || undefined,
      shipping_status: shippingFilter.value || undefined,
      cursor: cur || undefined,
      limit: pageSize.value,
      keyword: keyword.value || undefined,
    });
    if (requestId !== listRequest) return;
    if (!error && data) {
      orders.value = (data as any).orders || [];
      const next = Number((data as any).next_cursor || 0);
      hasMore.value = next > 0;
      if (next > 0) cursors.value[page.value] = next;
    }
  } finally {
    if (requestId === listRequest) loading.value = false;
  }
}

function searchOrders() {
  keyword.value = keywordInput.value.trim();
  resetOrderList();
}

function clearSearch() {
  keywordInput.value = "";
  keyword.value = "";
  resetOrderList();
}

function canDelete(row: any) {
  return (
    checkAuth("order:delete") &&
    ["canceled", "expired"].includes(row.status) &&
    !Number(row.paid_at)
  );
}

async function handleDelete(orderNos: string[]) {
  if (deleting.value || !orderNos.length) return;
  deleting.value = true;
  try {
    const { error } = await deleteOrders(orderNos);
    if (!error) {
      window.$message?.success(`已從列表刪除 ${orderNos.length} 條訂單`);
      if (detail.value && orderNos.includes(detail.value.order_no)) showDetail.value = false;
      resetOrderList();
    }
  } finally {
    deleting.value = false;
  }
}

// 篩選變化：遊標鏈重置回第 1 頁
function resetOrderList() {
  page.value = 1;
  cursors.value = [0];
  hasMore.value = false;
  loadOrders();
}

// 分頁回調：回第 1 頁（首頁/改條數）時舊遊標鏈作廢，隨翻頁重建
function onPagerChange(p: number) {
  if (p === 1) cursors.value = [0];
  loadOrders();
}

// ── 卡密交付（訂單詳情彈窗：完整明文 + 複製；服務端現場解密 + 審計）──
const deliveries = ref<any[]>([]);
const copiedCard = ref<number | null>(null);

async function handleDetail(orderNo: string) {
  const { data, error } = await fetchOrder(orderNo);
  if (!error && data) {
    detail.value = data;
    showDetail.value = true;
  }
  deliveries.value = [];
  if (!detail.value?.items?.some((it:any)=>it.goods_type!=='physical')) return;
  const dres = await fetchDeliveries(orderNo, 1, 100);
  if (!dres.error && dres.data) {
    deliveries.value = (dres.data as any).deliveries || [];
  }
}

async function copyCard(content: string, idx: number) {
  try {
    await navigator.clipboard.writeText(content);
    copiedCard.value = idx;
    setTimeout(() => {
      if (copiedCard.value === idx) copiedCard.value = null;
    }, 1500);
  } catch {
    window.$message?.error("複製失敗");
  }
}

function deliverModeText(mode: string): string {
  return { status: "標記模式", delete: "即刪模式", direct: "直發" }[mode] || mode;
}

// ── 訂單退款（order:refund 超管專屬；渠道錢包/網關/上游三路）──
const showRefund = ref(false);
const refunding = ref(false);
const refundForm = reactive({
  expected_refunded_cents: 0,
  expected_refunded_fee_cents: 0,
  fee_yuan: 0,
  max_fee_cents: 0,
  max_cents: 0,
  order_no: "",
  amount_yuan: 0,
  channel: "wallet" as string,
  reason: "",
});
const refundableStatuses = [
  "paid",
  "fulfilling",
  "delivered",
  "completed",
  "partially_delivered",
  "refunded",
];

async function openRefund(row: any) {
  const { data, error } = await fetchOrder(row.order_no);
  if (error || !data) return;
  const current = data as any;
  if (!current.user_id) {
    window.$message?.warning("遊客訂單沒有會員餘額，請核實其他退款方式");
    return;
  }
  refundForm.order_no = current.order_no;
  refundForm.expected_refunded_cents = Number(current.refunded_cents || 0);
  refundForm.max_cents = Number(current.total_cents) - refundForm.expected_refunded_cents;
  refundForm.expected_refunded_fee_cents = Number(current.refunded_fee_cents || 0);
  refundForm.max_fee_cents = Math.max(
    0,
    Number(current.paid_fee_cents || 0) - refundForm.expected_refunded_fee_cents,
  );
  refundForm.fee_yuan = Number((refundForm.max_fee_cents / 100).toFixed(2));
  if (refundForm.max_cents <= 0 && refundForm.max_fee_cents <= 0) {
    window.$message?.info("訂單已全部退款");
    return;
  }
  refundForm.amount_yuan = Number((refundForm.max_cents / 100).toFixed(2));
  refundForm.channel = "wallet";
  refundForm.reason = "";
  showRefund.value = true;
}

async function handleRefund() {
  if (
    refunding.value ||
    Number(refundForm.amount_yuan || 0) + Number(refundForm.fee_yuan || 0) <= 0
  )
    return;
  refunding.value = true;
  try {
    const { data, error } = await createRefund({
      expected_refunded_cents: refundForm.expected_refunded_cents,
      expected_refunded_fee_cents: refundForm.expected_refunded_fee_cents,
      fee_cents: Math.round(Number(refundForm.fee_yuan || 0) * 100),
      order_no: refundForm.order_no,
      amount_cents: Math.round(refundForm.amount_yuan * 100),
      channel: refundForm.channel,
      reason: refundForm.reason || undefined,
    });
    if (!error && (data as any)?.status === "succeeded") {
      window.$message?.success("退款成功，已退至下單會員餘額");
      showRefund.value = false;
      await handleDetail(refundForm.order_no);
      loadOrders();
    }
  } finally {
    refunding.value = false;
  }
}

async function handleCancel(orderNo: string) {
  const { error } = await cancelOrder(orderNo, "管理員取消");
  if (!error) {
    window.$message?.success("取消成功");
    loadOrders();
  }
}

const appStore = useAppStore();
watch(
  () => route.fullPath,
  () => {
    if (route.path !== "/order") return;
    statusFilter.value = String(route.query.status || "");
    keywordInput.value = "";
    keyword.value = "";
    resetOrderList();
  },
);
onMounted(async () => {
  await loadOrders();
  if (typeof route.query.order_no === "string" && route.query.order_no)
    await handleDetail(route.query.order_no);
});
</script>

<template>
  <div class="order-page min-h-500px min-w-0 flex-col gap-16px">
    <NCard title="訂單管理" class="flex-1">
      <div v-if="fromDashboard" class="mb-12px flex flex-wrap gap-8px items-center text-13px">
        <span
          >工作台篩選<span v-if="reportFilter.start_time && reportFilter.end_time">
            · {{ reportFilter.time_field === "paid" ? "付款" : "創建" }}時間
            {{
              new Date(reportFilter.start_time * 1000).toLocaleString("zh-TW", {
                timeZone: "Asia/Shanghai",
              })
            }}
            —
            {{
              new Date(reportFilter.end_time * 1000).toLocaleString("zh-TW", {
                timeZone: "Asia/Shanghai",
              })
            }}（北京時間）</span
          ><span v-if="reportFilter.product_id"> · 商品 #{{ reportFilter.product_id }}</span
          ><span v-if="reportFilter.channel_code">
            · 渠道 {{ reportFilter.channel_code }}</span
          ></span
        >
        <NButton size="small" @click="router.replace({ path: '/order', query: {} })"
          >清除篩選</NButton
        >
      </div>
      <NTabs type="line">
        <NTabPane name="orders" tab="訂單列表">
          <form class="mb-12px flex flex-wrap items-center gap-8px" @submit.prevent="searchOrders">
            <NInput
              v-model:value="keywordInput"
              clearable
              :maxlength="150"
              placeholder="搜索訂單號 / 聯繫方式"
              aria-label="搜索訂單號或聯繫方式"
              style="width: 320px; max-width: 100%"
              @clear="clearSearch"
            />
            <NButton attr-type="submit" type="primary">搜索</NButton>
            <NButton @click="clearSearch">重置</NButton>
            <NPopconfirm
              v-if="checkAuth('order:delete')"
              @positive-click="handleDelete([...selectedOrders])"
            >
              <template #trigger
                ><NButton
                  type="error"
                  :loading="deleting"
                  :disabled="loading || !selectedOrders.length"
                  >刪除所選（{{ selectedOrders.length }}）</NButton
                ></template
              >
              確認從管理列表刪除選中的
              {{ selectedOrders.length }}
              條訂單？僅支持已取消、已過期的未付款訂單，關聯支付及審計記錄保留。
            </NPopconfirm>
          </form>
          <div class="mb-16px flex flex-wrap items-center justify-between gap-12px">
            <FilterTabs
              v-model:value="statusFilter"
              :options="statusTabs"
              @change="resetOrderList"
            />
            <NSelect
              v-model:value="shippingFilter"
              class="w-180px"
              :options="[
                { label: '全部配送狀態', value: '' },
                ...['pending_payment', 'pending', 'partial', 'shipped', 'received', 'canceled'].map(
                  (value) => ({ value, label: shippingStatus(value) }),
                ),
              ]"
              @update:value="resetOrderList"
            />
            <NButton @click="loadOrders">刷新</NButton>
          </div>

          <NDataTable
            v-model:checked-row-keys="selectedOrders"
            :row-key="(row) => row.order_no"
            :columns="columns"
            :data="orders"
            :loading="loading"
            :max-height="appStore.isMobile ? undefined : 540"
            :scroll-x="1140"
          />

          <TablePager
            v-model:page="page"
            v-model:page-size="pageSize"
            mode="cursor"
            :has-more="hasMore"
            @change="onPagerChange"
          />
        </NTabPane>
        <NTabPane v-if="checkAuth('order:view_delivery')" name="pending" tab="待發貨">
          <PendingDeliverTab :refresh-key="pendingRefreshKey" @open-order="handleDetail" />
        </NTabPane>
      </NTabs>
    </NCard>

    <!-- 訂單詳情彈窗 -->
    <NModal v-model:show="showDetail" preset="card" title="訂單詳情" style="width: min(900px, 94vw)">
      <template v-if="detail">
        <NDescriptions :column="2" bordered size="small">
          <NDescriptionsItem label="訂單號">{{ detail.order_no }}</NDescriptionsItem>
          <NDescriptionsItem label="狀態">{{ statusText(detail.status) }}</NDescriptionsItem>
          <NDescriptionsItem label="已退本金">{{
            formatMoney(detail.refunded_cents || 0)
          }}</NDescriptionsItem>
          <NDescriptionsItem label="支付手續費">{{
            formatMoney(detail.paid_fee_cents || 0)
          }}</NDescriptionsItem>
          <NDescriptionsItem label="已退手續費">{{
            formatMoney(detail.refunded_fee_cents || 0)
          }}</NDescriptionsItem>
          <NDescriptionsItem label="總額">{{ formatMoney(detail.total_cents) }}</NDescriptionsItem>
          <NDescriptionsItem label="成本">{{ formatMoney(detail.cost_cents) }}</NDescriptionsItem>
          <NDescriptionsItem label="聯繫方式">{{
            detail.contact || detail.guest_contact || "-"
          }}</NDescriptionsItem>
          <NDescriptionsItem label="IP">{{ detail.client_ip || "-" }}</NDescriptionsItem>
          <NDescriptionsItem label="付款截止">{{
            detail.expired_at
              ? new Date(Number(detail.expired_at) * 1000).toLocaleString()
              : "未記錄"
          }}</NDescriptionsItem>
          <NDescriptionsItem label="超時處理">{{
            detail.expiry_review ? "待人工核對" : detail.expiry_reason || "按付款截止時間處理"
          }}</NDescriptionsItem>
          <NDescriptionsItem v-if="detail.expiry_reason" label="核對原因">{{
            detail.expiry_reason
          }}</NDescriptionsItem>
          <NDescriptionsItem
            v-if="detail.expiry_retry_at && !detail.expiry_review"
            label="下次核對"
            >{{
              new Date(Number(detail.expiry_retry_at) * 1000).toLocaleString()
            }}</NDescriptionsItem
          >
          <NDescriptionsItem label="訂單屬性">
            <NSpace :size="4">
              <NTag v-if="hasDiscount(detail)" size="small" type="success" :bordered="false"
                >已用折扣</NTag
              >
              <NTag
                v-if="(detail.items || []).every((it) => it.is_self)"
                size="small"
                type="info"
                :bordered="false"
                >自營</NTag
              >
              <NTag
                v-else-if="(detail.items || []).some((it) => !it.is_self)"
                size="small"
                type="warning"
                :bordered="false"
                >含上游商品</NTag
              >
            </NSpace>
          </NDescriptionsItem>
        </NDescriptions>

        <!-- 上游貨源信息（老項目同款信息區） -->
        <template v-if="(detail.items || []).some((it) => it.upstream_source_name)">
          <NDivider>上游貨源</NDivider>
          <NDescriptions :column="2" bordered size="small">
            <NDescriptionsItem label="上游渠道">{{
              (detail.items || []).find((it) => it.upstream_source_name)?.upstream_source_name ||
              "-"
            }}</NDescriptionsItem>
            <NDescriptionsItem label="上游驅動">{{
              (detail.items || []).find((it) => it.upstream_driver)?.upstream_driver || "-"
            }}</NDescriptionsItem>
            <NDescriptionsItem label="上游商品編碼" :span="2">
              {{
                (detail.items || []).find((it) => it.upstream_product_code)
                  ?.upstream_product_code || "-"
              }}
            </NDescriptionsItem>
            <NDescriptionsItem label="上游地址" :span="2">
              <a
                v-if="(detail.items || []).find((it) => it.upstream_url)"
                :href="(detail.items || []).find((it) => it.upstream_url)?.upstream_url"
                target="_blank"
                rel="noopener noreferrer"
                class="text-blue-500 hover:underline break-all"
              >
                {{ (detail.items || []).find((it) => it.upstream_url)?.upstream_url }} ↗
              </a>
              <span v-else>-</span>
            </NDescriptionsItem>
          </NDescriptions>
        </template>
        <PhysicalOrderPanel
          v-if="Number(detail.commerce_version) === 1"
          :order="detail"
          @refresh="afterManualDeliver"
        />
        <NDivider>商品明細</NDivider>
        <NDataTable
          :data="detail.items || []"
          :columns="itemColumns"
          :scroll-x="1050"
          size="small"
          :max-height="540"
        />
        <!-- 卡密交付（完整明文；含取貨次數/IP——客服核對場景） -->
        <div
          v-for="it in detail.items || []"
          :key="it.id"
          class="my-12px rounded-6px border border-gray-200 p-12px"
        >
          <b>{{ it.name }} {{ it.sku_name }} · 下單資料</b
          ><OrderAnswers :value="it.form_answers_json" />
          <p v-if="it.assigned_admin_id" class="text-12px opacity-60">
            處理人 ID：{{ it.assigned_admin_id }}
          </p>
        </div>
        <template v-if="detail.items?.some((it:any)=>it.goods_type!=='physical')">
        <NDivider>交付結果（{{ deliveries.length }} 條）</NDivider>
        <div v-if="deliveries.length" class="flex flex-col gap-8px">
          <div
            v-for="(d, i) in deliveries"
            :key="d.id"
            class="flex items-center gap-10px rounded-6px border border-gray-200 px-12px py-8px dark:border-gray-700"
          >
            <span class="w-24px shrink-0 text-12px text-gray-400">#{{ i + 1 }}</span>
            <div class="min-w-0 flex-1">
              <span class="text-12px opacity-60">商品項 #{{ d.order_item_id }}</span>
              <pre class="whitespace-pre-wrap break-all text-13px">{{ d.content }}</pre>
            </div>
            <NTag size="small" :bordered="false" class="shrink-0">{{
              d.kind === "service" ? "服務完成" : deliverModeText(d.delivered_mode)
            }}</NTag>
            <span class="shrink-0 text-11px text-gray-400">取貨 {{ d.fetch_count || 0 }} 次</span>
            <NButton
              size="tiny"
              tertiary
              type="primary"
              class="shrink-0"
              @click="copyCard(d.content, i)"
            >
              {{ copiedCard === i ? "已複製" : "複製" }}
            </NButton>
          </div>
        </div>
        <div v-else class="text-12px opacity-50" style="padding: 4px 0">暫無交付記錄</div>
        </template>
        <NDivider>金額明細（{{ (detail.amount_lines || []).length }} 行）</NDivider>
        <NDataTable
          :data="detail.amount_lines || []"
          :columns="amountColumns"
          size="small"
          :max-height="540"
        />
        <NDivider>狀態事件（{{ (detail.status_events || []).length }} 條）</NDivider>
        <!-- 橫向時間線（大廠訂單詳情風格：圓點節點 + 連線 + 時間，可橫向滾動） -->
        <div style="display: flex; align-items: flex-start; overflow-x: auto; padding: 4px 4px 8px">
          <div
            v-for="(evt, i) in sortedEvents(detail.status_events || [])"
            :key="i"
            style="display: flex; align-items: flex-start"
          >
            <div
              v-if="i > 0"
              style="width: 28px; height: 2px; margin-top: 13px; background: #d0d7de"
            />
            <div style="min-width: 96px; padding: 0 4px; text-align: center">
              <div
                style="
                  width: 12px;
                  height: 12px;
                  border-radius: 50%;
                  border: 2px solid #fff;
                  box-shadow: 0 0 0 1px #d0d7de;
                  margin: 0 auto;
                "
                :style="{
                  background: ['paid', 'delivered', 'completed', 'fulfilled'].includes(evt.event)
                    ? '#18a058'
                    : ['canceled', 'expired', 'refunded'].includes(evt.event)
                      ? '#d03050'
                      : '#2080f0',
                }"
              />
              <div
                class="mt-8px text-12px font-500"
                :style="{
                  color: ['paid', 'delivered', 'completed', 'fulfilled'].includes(evt.event)
                    ? '#18a058'
                    : ['canceled', 'expired', 'refunded'].includes(evt.event)
                      ? '#d03050'
                      : '#2080f0',
                }"
              >
                {{ eventText(evt.event) }}
              </div>
              <div class="text-11px opacity-50 mt-2px">
                {{
                  evt.from_status && evt.to_status
                    ? statusText(evt.from_status) + " → " + statusText(evt.to_status)
                    : ""
                }}
              </div>
              <div class="text-11px opacity-60 mt-2px">{{ formatTime(evt.created_at) }}</div>
              <div class="text-11px opacity-50 mt-2px">
                {{ operatorText(evt.operator) }}{{ evt.reason ? "：" + evt.reason : "" }}
              </div>
            </div>
          </div>
          <div
            v-if="(detail.status_events || []).length === 0"
            class="text-12px opacity-50"
            style="padding: 8px 0"
          >
            暫無狀態事件
          </div>
        </div>
      </template>
      <template #footer>
        <div v-if="detail" class="flex justify-end gap-8px">
          <!-- NPopconfirm 的 trigger 槽必須恰好一個子節點：v-if 放在 Popconfirm 自身（空槽會拋 follower 錯誤） -->
          <NButton
            v-if="checkAuth('procurement:read') && detail.items?.some((it:any)=>it.goods_type!=='physical' && it.fulfillment_type==='upstream')"
            @click="
              router.push({
                path: '/channel',
                query: { tab: 'procurement', order_no: detail.order_no },
              });
              showDetail = false;
            "
            >關聯採購單</NButton
          >
          <NButton
            v-if="
              ['paid', 'fulfilling', 'partially_delivered'].includes(detail.status) &&
              detail.items?.some((it: any) => it.goods_type !== 'physical') &&
              checkAuth('order:deliver')
            "
            type="primary"
            @click="showManualDeliver = true"
            >人工補發</NButton
          >
          <NPopconfirm
            v-if="detail.status === 'pending_payment' && checkAuth('order:cancel')"
            @positive-click="handleCancel(detail.order_no)"
          >
            <template #trigger>
              <NButton size="small" type="warning">取消訂單</NButton>
            </template>
            確定取消該訂單？
          </NPopconfirm>
          <NButton
            v-if="
              !Number(detail.commerce_version) &&
              refundableStatuses.includes(detail.status) &&
              checkAuth('order:refund')
            "
            size="small"
            type="error"
            @click="openRefund(detail)"
          >
            退款
          </NButton>
        </div>
      </template>
    </NModal>

    <!-- 退款彈窗（order:refund 超管專屬） -->
    <NModal
      v-model:show="showRefund"
      preset="dialog"
      :title="`訂單退款：${refundForm.order_no}`"
      style="width: 460px"
    >
      <NForm :model="refundForm" label-placement="left" label-width="72">
        <NFormItem label="退款渠道" required>
          <NSelect
            v-model:value="refundForm.channel"
            :options="[
              { label: '退至下單會員餘額', value: 'wallet' },
              { label: '支付網關（暫不支持自動執行）', value: 'gateway', disabled: true },
              { label: '上游退貨（需向上遊核實）', value: 'upstream', disabled: true },
            ]"
          />
        </NFormItem>
        <p class="mb-12px text-13px">
          退款將進入下單會員餘額；關聯佣金或分站利潤的訂單須全額退款。
        </p>
        <NFormItem :label="`退本金(${currencyUnit()})`" required>
          <NInputNumber
            v-model:value="refundForm.amount_yuan"
            :min="0"
            :max="refundForm.max_cents / 100"
            :precision="2"
            class="w-full"
          />
        </NFormItem>
        <NFormItem label="退手續費"
          ><NInputNumber
            v-model:value="refundForm.fee_yuan"
            :min="0"
            :max="refundForm.max_fee_cents / 100"
            :precision="2"
            class="w-full"
        /></NFormItem>
        <p class="text-12px mb-12px">
          合計退至餘額
          {{
            formatMoney(
              Math.round(
                (Number(refundForm.amount_yuan || 0) + Number(refundForm.fee_yuan || 0)) * 100,
              ),
            )
          }}。手續費單獨退還，部分退款可調整或填 0。
        </p>
        <NFormItem label="原因">
          <NInput
            v-model:value="refundForm.reason"
            placeholder="選填，會記入訂單狀態事件與審計"
            :maxlength="180"
          />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showRefund = false">取消</NButton>
        <NButton type="error" :loading="refunding" @click="handleRefund">確認退款到餘額</NButton>
      </template>
    </NModal>
    <ManualDeliverDialog
      v-model:show="showManualDeliver"
      :order-no="detail?.order_no || ''"
      @delivered="afterManualDeliver"
    />
  </div>
</template>

<style scoped>
.order-page {
  flex-shrink: 0;
}
.order-product-names {
  white-space: normal;
  overflow-wrap: anywhere;
  line-height: 1.6;
}
</style>
