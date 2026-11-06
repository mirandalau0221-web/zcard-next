<script setup lang="ts">
import { request as shippingRequest } from "@/service/request";
import { countryOptions, type RegionData } from "../../../../packages/shipping";
const shippingRegions = ref<RegionData>({});
onMounted(async () => {
  const r = await shippingRequest<{ data_json: string }>({
    url: "/api/v1/storefront/shipping/regions",
  });
  if (r.data)
    try {
      shippingRegions.value = JSON.parse(r.data.data_json);
    } catch {}
});
/**
 * 商品管理（/ 前端面，2026-08-17 表單補全）：
 * 全字段表單（分類/描述/封面+圖集 media 上傳/排序/上下架三態/發貨模式/庫存顯示/積分價）
 * + SKU 規格管理子表格 + 下單控件配置（獨立彈窗）。
 */
import ListingManagement from "./components/listing-management.vue";
import { ref, reactive, computed, onMounted, onBeforeUnmount, h, watch } from "vue";
import { onBeforeRouteLeave, useRoute, useRouter } from "vue-router";
import { NButton, NTag, NSpace, NPopconfirm, NInputNumber, NPopover } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import {
  fetchProducts,
  createSupplySyncTask,
  fetchSupplySyncTask,
  setProductLock,
  fetchProduct,
  createProduct,
  updateProduct,
  deleteProduct,
  batchUpdateProductStatus,
  batchUpdateProductCategory,
  fetchCategories,
  fetchSupplyConnectionOptions,
} from "@/service/api";
import { formatMoney, centsToYuan, currencyUnit, yuanToFen } from "@/utils/money";
import { checkAuth } from "@/directives";
import MediaField from "@/components/common/media-picker/media-field.vue";
import RichEditor from "@/components/common/rich-editor/index.vue";
import TablePager from "@/components/common/table-pager.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";
import ClassifyProducts from "./components/classify-products.vue";
import DeliverySources from "./components/delivery-sources.vue";
import SkuPanel from "./components/sku-panel.vue";
import ControlPanel from "./components/control-panel.vue";
import CategoryModal from "./components/category-modal.vue";
import CategoryIcon from "@/components/common/category-icon.vue";
import ReviewsDrawer from "./components/reviews-drawer.vue";
import DeleteProductModal from "./components/delete-product-modal.vue";
import BatchContentModal from "./components/batch-content-modal.vue";
import CategoryPlacements from "./components/category-placements.vue";

const deleteTarget = ref<{ id: number; name: string } | null>(null);

defineOptions({ name: "ProductManagement" });

const route = useRoute();
const router = useRouter();

const loading = ref(false);
const saving = ref(false);
const showCreate = ref(false);
const skuPanel = ref<InstanceType<typeof SkuPanel> | null>(null);
const editingId = ref(0);
const copying = ref(false);
const copySourceId = ref(0);
const copySKUs = computed(() => checkAuth("catalog:sku_read") && checkAuth("catalog:sku_write"));
const copyControls = computed(() => checkAuth("catalog:control_read") && checkAuth("catalog:control_write"));
const openingProduct = ref(false);
const deliveryProduct = ref<any>(null);
const showDeliverySources = ref(false);
function openDeliverySources(row: any) {
  deliveryProduct.value = row;
  showDeliverySources.value = true;
}
async function deliverySaved() {
  loadList();
  independentlySaved.value = true;
  if (editingId.value === deliveryProduct.value?.id) {
    const { data } = await fetchProduct(editingId.value);
    if (data) {
      formData.fulfillment_mode = (data as any).fulfillment_mode || "auto";
      editingProduct.value = data;
      await skuPanel.value?.refreshDeliveryModes();
    }
  }
}
const editingProduct = ref<any>(null);
const editorLocked = computed(() => !!editingProduct.value?.is_locked);
const lockFilter = ref<string | null>(null);
const showPlacements = ref(false);
const batchReport = ref("");
const batchFailures = ref<string[]>([]);
const lockBusy = ref<number | null>(null);
const pageLockedCount = computed(() => products.value.filter((p) => p.is_locked).length);
const selectedCategoryName = computed(
  () => categories.value.find((c) => c.id === categoryFilter.value)?.name || "當前分類",
);
async function changeLock(row: any) {
  if (lockBusy.value !== null) return;
  const next = !row.is_locked;
  window.$dialog?.warning({
    title: next ? "鎖定商品？" : "解鎖商品？",
    content: next
      ? `「${row.name}」鎖定後不能修改或刪除，批量操作及上游資料、價格、上下架同步會自動跳過。正常訂單履約不受影響。`
      : `「${row.name}」將恢復編輯，並按原有規則參與後續上游同步。`,
    positiveText: next ? "鎖定商品" : "解鎖商品",
    negativeText: "取消",
    onPositiveClick: async () => {
      lockBusy.value = row.id;
      try {
        const { data, error } = await setProductLock(row.id, next, Number(row.lock_version || 0));
        if (error || !data) {
          await loadList();
          return false;
        }
        checkedKeys.value = checkedKeys.value.filter((id) => id !== row.id);
        if (editingId.value === row.id) {
          editingProduct.value = { ...editingProduct.value, ...data };
        }
        window.$message?.success(next ? "商品已鎖定，批量操作將自動跳過" : "商品已解鎖");
        await loadList();
      } finally {
        lockBusy.value = null;
      }
    },
  });
}

const controlPanel = ref<InstanceType<typeof ControlPanel> | null>(null);
const initialProduct = ref("");
const independentlySaved = ref(false);
const editorBusy = computed(
  () => saving.value || !!skuPanel.value?.saving || !!controlPanel.value?.saving,
);
const hasUnsaved = computed(
  () =>
    !!initialProduct.value &&
    (JSON.stringify(buildPayload()) !== initialProduct.value ||
      !!skuPanel.value?.hasPending ||
      !!controlPanel.value?.hasPending),
);
let discardPrompt: Promise<boolean> | undefined;
function confirmDiscard(): Promise<boolean> {
  if (!hasUnsaved.value) return Promise.resolve(true);
  if (discardPrompt) return discardPrompt;
  discardPrompt = new Promise<boolean>((resolve) => {
    const dialog = window.$dialog;
    if (!dialog) {
      resolve(false);
      return;
    }
    dialog.warning({
      title: "放棄未保存修改？",
      content: independentlySaved.value
        ? "關閉將放棄尚未保存的內容。已單獨保存或刪除的規格、控件仍然生效。"
        : "本次尚未保存的商品、規格及控件輸入將被放棄。",
      positiveText: "放棄修改",
      negativeText: "繼續編輯",
      maskClosable: false,
      onPositiveClick: () => resolve(true),
      onNegativeClick: () => resolve(false),
      onClose: () => resolve(false),
      onEsc: () => resolve(false),
    });
  }).finally(() => {
    discardPrompt = undefined;
  });
  return discardPrompt;
}
async function requestClose() {
  if (editorBusy.value) return;
  if (await confirmDiscard()) showCreate.value = false;
}
function afterEditorLeave() {
  if (!showCreate.value) resetForm();
}
onBeforeRouteLeave(async () => {
  if (!showCreate.value) return true;
  if (editorBusy.value) {
    window.$message?.warning("正在保存，請稍後離開");
    return false;
  }
  if (!(await confirmDiscard())) return false;
  showCreate.value = false;
  resetForm();
  return true;
});
function beforeUnload(event: BeforeUnloadEvent) {
  if (showCreate.value && (hasUnsaved.value || editorBusy.value)) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(() => window.addEventListener("beforeunload", beforeUnload));
onBeforeUnmount(() => window.removeEventListener("beforeunload", beforeUnload));
const showListing = ref(false);
const listingIds = ref<number[]>([]);
const listingFilter = ref<Record<string, unknown>>({});
const listingAction = ref("enable");
const listingScope = ref("");
const inventoryFilter = ref("");
const restockedOnly = ref(false);
const autoListingFilter = ref<string | null>(null);
const inventoryTabs = [
  { label: "全部", value: "" },
  { label: "有庫存", value: "available" },
  { label: "庫存告急", value: "low" },
  { label: "無庫存", value: "empty" },
  { label: "待確認", value: "unknown" },
];
function currentFilters() {
  return {
    keyword: keyword.value || undefined,
    is_locked: lockFilter.value === null ? undefined : lockFilter.value === "locked",
    status: statusFilter.value || undefined,
    inventory: inventoryFilter.value || undefined,
    restocked_only: restockedOnly.value || undefined,
    auto_listing:
      autoListingFilter.value === null ? undefined : autoListingFilter.value === "enabled",
    category_id: categoryFilter.value || undefined,
    upstream_source_id: (supplyFilter.value ?? 0) > 0 ? supplyFilter.value! : undefined,
    local_only: supplyFilter.value === 0 || undefined,
  };
}
function openListing(ids: number[] = [], action = "enable") {
  listingIds.value = [...ids];
  listingAction.value = action;
  listingFilter.value = { ...currentFilters() };
  listingScope.value = ids.length
    ? `已選 ${ids.length} 件商品`
    : `當前全部篩選結果（${total.value} 件，包含其他頁）`;
  showListing.value = true;
}
function listingSaved() {
  checkedKeys.value = [];
  void loadList();
}
function showRestocked() {
  const next = !restockedOnly.value;
  if (next) {
    statusFilter.value = -1;
    inventoryFilter.value = "available";
  }
  restockedOnly.value = next;
  onSearch();
}

const checkingStock = ref(false);
const stockCheckMessage = ref("");
let stockCheckTimer: ReturnType<typeof setTimeout> | undefined;
let stockCheckSequence = 0;
async function checkChannelStock() {
  if (!supplyFilter.value || checkingStock.value) return;
  checkingStock.value = true;
  stockCheckMessage.value = "正在創建後台檢查任務…";
  const seq = ++stockCheckSequence;
  const r = await createSupplySyncTask({
    connection_id: supplyFilter.value,
    scope: "listing",
    mode: "full",
  });
  if (seq !== stockCheckSequence) return;
  if (r.error) {
    checkingStock.value = false;
    stockCheckMessage.value = "檢查任務未能啓動，請到貨源任務列表查看是否已有任務運行。";
    return;
  }
  const task = (r.data as any)?.task || r.data;
  const id = (task as any)?.id;
  if (!id) {
    checkingStock.value = false;
    stockCheckMessage.value = "任務已提交，請在貨源任務列表查看進度。";
    return;
  }
  const poll = async () => {
    if (seq !== stockCheckSequence) return;
    const x = await fetchSupplySyncTask(id);
    if (seq !== stockCheckSequence) return;
    const t = (x.data as any)?.task || x.data;
    if (x.error) {
      checkingStock.value = false;
      stockCheckMessage.value = `任務 #${id} 在後台繼續，可到貨源任務列表查看。`;
      return;
    }
    stockCheckMessage.value = `庫存與補貨檢查 #${id} · ${t?.processed || 0} 件已處理。僅自動管理商品會按規則修改上下架，其他商品只更新庫存信息。`;
    if (["done", "failed", "canceled"].includes(t?.status)) {
      checkingStock.value = false;
      stockCheckMessage.value = `檢查 #${id} ${t.status === "done" ? "已完成" : "有未完成項，請到貨源任務列表查看"}；已刷新列表。`;
      void loadList();
      return;
    }
    stockCheckTimer = setTimeout(poll, 3000);
  };
  void poll();
}
onBeforeUnmount(() => {
  stockCheckSequence++;
  clearTimeout(stockCheckTimer);
});
const keyword = ref("");
const products = ref<any[]>([]);
const categories = ref<any[]>([]);
const supplyConnections = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);

// 快捷篩選卡片（後端 status 口徑：0=全部 1=上架 2=隱藏 -1=僅下架；low_stock=庫存告急）
const statusFilter = ref<number>(0);
watch(
  [statusFilter, inventoryFilter],
  () => {
    if (restockedOnly.value && (statusFilter.value !== -1 || inventoryFilter.value !== "available"))
      restockedOnly.value = false;
  },
  { flush: "sync" },
);
const categoryFilter = ref<number | null>(null); // 分類篩選（null=全部）
const supplyFilter = ref<number | null>(null); // 渠道篩選（null=全部 0=自營 >0=渠道ID）
const statusTabs = [
  { label: "全部", value: 0, type: "default" as const },
  { label: "已上架", value: 1, type: "success" as const },
  { label: "已隱藏", value: 2, type: "warning" as const },
  { label: "已下架", value: -1, type: "error" as const },
];

// 列表多選（批量上下架）
const showReviews = ref(false);
const reviewProduct = ref<any>(null);

function openReviews(row: any) {
  reviewProduct.value = row;
  showReviews.value = true;
}

const checkedKeys = ref<number[]>([]);
const showBatchContent = ref(false);
const batchContentIDs = ref<number[]>([]);
const batchContentCategory = ref<number | null>(null);
const offPageSelected = computed(
  () => checkedKeys.value.filter((id) => !products.value.some((p) => p.id === id)).length,
);
const contentProtection = ref({ cover: false, description: false });
watch(
  [
    keyword,
    categoryFilter,
    supplyFilter,
    statusFilter,
    lockFilter,
    inventoryFilter,
    restockedOnly,
    autoListingFilter,
  ],
  () => {
    checkedKeys.value = [];
  },
  { flush: "sync" },
);
function updateCheckedKeys(keys: Array<string | number>) {
  if (loading.value) return;
  const pageIDs = new Set(products.value.map((p) => p.id));
  checkedKeys.value = [
    ...new Set([
      ...checkedKeys.value.filter((id) => !pageIDs.has(id)),
      ...keys.map(Number),
    ]),
  ];
}
function openBatchContent() {
  batchContentIDs.value = [...checkedKeys.value];
  batchContentCategory.value = categoryFilter.value;
  showBatchContent.value = true;
}
function contentSaved() {
  checkedKeys.value = [];
  loadList();
}

// ── 單元格價格編輯（售價/成本）：鉛筆圖標 → 氣泡輸入（大廠輕量編輯模式）──
// 金額純文本居中展示；點筆彈 NPopover（受控顯隱），氣泡內輸入金額 → 確定/Enter 保存、Esc 關閉。
const cellDraft = reactive<Record<string, number>>({});
const cellPopover = reactive<Record<string, boolean>>({}); // 受控氣泡顯隱

function draftKey(row: any, field: "price" | "cost") {
  return `${row.id}:${field}`;
}

async function commitCellEdit(row: any, field: "price" | "cost") {
  if (row.is_locked) return;
  const key = draftKey(row, field);
  const draft = cellDraft[key];
  cellPopover[key] = false; // 無論成敗先收氣泡
  if (draft === undefined) return; // 未輸入
  cellDraft[key] = undefined as any; // 清草稿防連發
  const centsKey = field === "price" ? "price_cents" : "factory_price_cents";
  const current = row[centsKey] || 0;
  if (field === "price" && draft <= 0) {
    window.$message?.warning("售價必須大於 0");
    return;
  }
  if (yuanToFen(draft) === current) return; // 值未變
  const payload: Record<string, any> = {
    // 攜帶行內既有字段（服務端 PUT 全量語義——防零值覆蓋積分價等）
    factory_price_cents: row.factory_price_cents || 0,
    points_required: row.points_required || 0,
    status: row.status,
    sort: row.sort || 0,
    stock_visible: row.stock_visible !== false,
    delivery_mode: row.delivery_mode || "",
    is_recommend: !!row.is_recommend,
  };
  payload[centsKey] = yuanToFen(draft);
  const { error } = await updateProduct(row.id, payload);
  if (!error) {
    window.$message?.success(`「${row.name}」${field === "price" ? "售價" : "成本"}已更新`);
    loadList();
  }
}

// 推薦開關（列表快速設置）：同改價套路——攜帶行內既有字段防全量語義清零
// （status/sort/factory_price/points_required/stock_visible 在服務端恆寫入，缺省即被置零）
async function toggleRecommend(row: any) {
  if (row.is_locked) return;
  const v = !row.is_recommend;
  const payload: Record<string, any> = {
    factory_price_cents: row.factory_price_cents || 0,
    points_required: row.points_required || 0,
    status: row.status,
    sort: row.sort || 0,
    stock_visible: row.stock_visible !== false,
    delivery_mode: row.delivery_mode || "",
    is_recommend: v,
  };
  const { error } = await updateProduct(row.id, payload);
  if (!error) {
    window.$message?.success(`「${row.name}」已${v ? "設為首頁推薦" : "取消推薦"}`);
    loadList();
  }
}

// priceLine 單條價格行：金額文本 + 鉛筆（點擊彈氣泡輸入改價）
function priceLine(row: any, field: "price" | "cost") {
  const centsKey = field === "price" ? "price_cents" : "factory_price_cents";
  const cents = row[centsKey] || 0;
  const key = draftKey(row, field);
  const label = field === "price" ? "售價" : "成本";
  return h("div", { class: "money-cell group inline-flex items-center gap-4px" }, [
    h("span", {}, cents ? formatMoney(cents) : "-"),
    // 改價鉛筆僅 catalog:write 可見（渲染函數內求值，權限變更後隨表格重渲染生效）
    checkAuth("catalog:write") && !row.is_locked
      ? h(
          NPopover,
          {
            show: !!cellPopover[key],
            placement: "left",
            trigger: "click", // click 觸發：點外部/再點鉛筆 → onUpdateShow(false) 自動收起
            onUpdateShow: (v: boolean) => {
              cellPopover[key] = v;
              if (v) cellDraft[key] = cents ? Number(centsToYuan(cents)) : 0; // 開氣泡帶入當前值
            },
          },
          {
            trigger: () =>
              h(
                "span",
                {
                  class:
                    "cursor-pointer text-12px text-gray-400 transition-colors hover:text-primary",
                  title: `修改${label}`,
                },
                "✎",
              ),
            default: () =>
              h("div", { class: "flex items-center gap-8px" }, [
                h("span", { class: "text-13px whitespace-nowrap" }, `${label}(${currencyUnit()})`),
                h(NInputNumber, {
                  value: cellDraft[key] ?? 0,
                  size: "small",
                  min: field === "price" ? 0.01 : 0,
                  precision: 2,
                  showButton: false,
                  placeholder: label,
                  style: "width: 110px",
                  autofocus: true,
                  inputProps: {
                    onKeyup: (e: KeyboardEvent) => {
                      if (e.key === "Enter") commitCellEdit(row, field);
                      else if (e.key === "Escape") {
                        cellDraft[key] = undefined as any;
                        cellPopover[key] = false;
                      }
                    },
                  },
                  onUpdateValue: (v: number | null) => {
                    cellDraft[key] = v ?? 0;
                  },
                }),
                h(
                  NButton,
                  { size: "small", type: "primary", onClick: () => commitCellEdit(row, field) },
                  { default: () => "確定" },
                ),
              ]),
          },
        )
      : null,
  ]);
}

// priceCell 價格塊：售價/成本/加價三行合一（標籤與數值緊鄰，行內鉛筆氣泡改價）
function priceCell(row: any) {
 if(row.product_kind=== "sms_channel") return h("span",{class:"text-primary"},"商品頁實時選價");
 if(row.product_kind=== "social_service") return h("span",{class:"text-primary"},"商品頁動態報價");
  const price = row.price_cents || 0;
  const cost = row.factory_price_cents || 0;
  // (售價 − 成本) / 成本（成本基準；成本 0 無法計算）
  const markup = cost > 0 && price > 0 ? Math.round(((price - cost) / cost) * 100) : null;
  // 行結構：灰標籤(定寬對齊) + 數值緊鄰其後（大廠 definition-list 式，不兩端撐開）
  const line = (label: string, value: any) =>
    h("div", { class: "flex items-center gap-6px leading-20px" }, [
      h("span", { class: "w-30px shrink-0 text-12px text-gray-400" }, label),
      value,
    ]);
  return h("div", { class: "flex flex-col gap-2px py-2px" }, [
    line("售價", priceLine(row, "price")),
    line("成本", priceLine(row, "cost")),
    line(
      "毛利",
      markup === null
        ? h("span", { class: "text-gray-300" }, "-")
        : h(
            NTag,
            { size: "small", bordered: false, type: markup >= 0 ? "success" : "error" },
            { default: () => `${markup >= 0 ? "+" : ""}${markup}%` },
          ),
    ),
  ]);
}

// statsCell 庫存/已售塊：兩行，標籤定寬 + 數值緊鄰（與價格塊視覺一致）
function statsCell(row: any) {
 if(row.product_kind === "sms_channel")return h("span",{},"按選項庫存");
  const stock =
    row.stock ?? (row.stock_status === "unknown" || row.stock_status === "stale" ? -2 : 0); // -1 = 不限（鏈接/兌換碼類不入卡池；代發上遊無限）
  const sold = row.sold_count ?? 0;
  const line = (label: string, value: any) =>
    h("div", { class: "flex items-center gap-6px leading-20px" }, [
      h("span", { class: "w-30px shrink-0 text-12px text-gray-400" }, label),
      value,
    ]);
  // 預警按後台閾值和實際規格計算，不能用商品合計掩蓋缺貨規格。
  const stockNode =
    (row.product_property || row.goods_type) === "physical" && row.track_inventory === false
      ? h("span", {}, "不管理庫存")
      : row.stock_status === "stale"
      ? h(
          "span",
          {
            class: "text-12px",
            title: `上次同步：${row.stock_checked_at ? new Date(row.stock_checked_at * 1000).toLocaleString() : "未知"}。僅供參考，買家進入詳情時重新查詢。`,
          },
          row.stock_reference === -1 ? "上次不限" : `參考 ${row.stock_reference ?? 0} 件`,
        )
      : stock < -1
        ? h(
            "span",
            {
              class: "text-12px",
              title: "上游暫未返回庫存，可到貨源渠道執行同步；買家進入商品詳情時會重新查詢。",
            },
            "上游未返回",
          )
        : stock === -1
          ? h("span", {}, "不限")
          : stock <= 0
            ? h("span", { class: "font-medium text-red-500" }, "0 件")
            : h("span", { class: row.low_stock_message ? "text-orange-500" : "" }, `${stock} 件`);
  return h("div", { class: "flex flex-col gap-2px py-2px" }, [
    line("庫存", stockNode),
    line("已售", h("span", {}, `${sold} 件`)),
    row.low_stock_message
      ? h(
          "span",
          { class: "text-12px text-orange-500 max-w-180px truncate", title: row.low_stock_message },
          `預警：${row.low_stock_message}`,
        )
      : null,
  ]);
}

// 分類管理彈窗態
const showCategory = ref(false);

// 分步表單（大廠發佈商品模式：基礎 → 價格庫存 → 商品描述 → 規格與控件 → 高級設置）
const step = ref(1);
const stepCount = computed(() => formData.product_kind === "social_service" ? 3 : formData.product_property === "physical" ? 4 : 5);

const formData = reactive({
 product_kind:"standard",
  name: "",
  category_id: null as number | null,
  description: "",
  cover: [] as string[],
  images: [] as string[],
  price_yuan: 0,
  factory_price_yuan: 0,
  points_required: 0,
  product_property: "virtual" as "virtual" | "physical",
  track_inventory: true,
  sales_visible: true,
  shipping_mode: "free",
  shipping_fee_yuan: 0,
  shipping_countries: [] as string[],
  physical_stock: 0,
  stock_type: "card",
  fulfillment_mode: "auto",
  manual_stock: -1,
  direct_content: "",
  delivery_mode: "status",
  stock_visible: true,
  dedup: true,
  sort: 0,
  status: 1,
  is_recommend: false,
});
const isSocialProduct = computed(() => formData.product_kind === "social_service");
const isDynamicProduct = computed(() => isSocialProduct.value || formData.product_kind === "sms_channel");
const directContentSet = ref(false); // 編輯時已配置直發內容（不回顯明文，留空=不變）
const savedInventoryTracked = computed(() => editingProduct.value?.track_inventory !== false);
const inventoryModePending = computed(() =>
  !!editingId.value && formData.product_property === "physical" &&
  formData.track_inventory !== savedInventoryTracked.value,
);
watch(stepCount, (count) => { if (step.value > count) step.value = count; });

const stockTypeOptions = [
  { label: "卡密（一卡一賣，導入卡池發貨）", value: "card" },
  { label: "鏈接（直發 · 同一鏈接反覆賣，如網盤資源）", value: "url" },
  { label: "兌換碼（直發 · 同一碼反覆賣，如口令/激活指引）", value: "code" },
];
const deliveryModeOptions = [
  { label: "標記發貨（卡密保留）", value: "status" },
  { label: "即刪發貨（發出即刪）", value: "delete" },
];

// 分類樹（NTreeSelect 層級數據：label/key/children，無限深度——parent_id 建鏈）
const categoryTreeOptions = computed(() => {
  const map = new Map<number, any>();
  for (const c of categories.value)
    map.set(c.id, {
      label: c.name,
      key: c.id,
      icon: c.icon,
      parent_id: c.parent_id || 0,
      children: [],
    });
  const roots: any[] = [];
  for (const node of map.values()) {
    const parent = map.get(node.parent_id);
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  return roots;
});

function stepPrev() {
  if (step.value > 1) step.value -= 1;
}

// 分步守衞：離開「基礎信息」須有名稱；離開「價格庫存」須有售價
function stepNext() {
  if (step.value === 1 && !formData.name.trim()) {
    window.$message?.warning("請先填寫商品名稱");
    return;
  }
  if (step.value === 2 && (!isDynamicProduct.value && formData.price_yuan <= 0)) {
    window.$message?.warning("請先填寫有效售價");
    return;
  }
  if (step.value < stepCount.value) step.value += 1;
}

const columns: DataTableColumns<any> = [
  { type: "selection" },
  { title: "ID", key: "id", width: 56 },
  {
    title: "封面",
    key: "cover",
    width: 72,
    render: (row) => {
      const canEdit = checkAuth("catalog:write");
      const label = row.cover ? "更換封面" : "補封面";
      return h(
        canEdit ? "button" : "div",
        {
          ...(canEdit ? {
            type: "button",
            disabled: row.is_locked,
            onClick: () => handleEdit(row, 3),
            "aria-label": `${label}：${row.name}`,
          } : {}),
          title: canEdit ? row.is_locked ? "商品已鎖定，請先解鎖後修改封面" : label : "商品封面",
          class: "block h-44px w-44px rounded-4px p-0 disabled:cursor-not-allowed disabled:opacity-60",
        },
        row.cover
        ? h("img", { src: row.cover, alt: `${row.name}封面`, class: "h-44px w-44px rounded-4px object-cover" })
        : // 暫無主圖佔位（大廠縮略圖佔位慣例：淺灰底 + 相機 SVG + 提示文案）
          h(
            "div",
            {
              class:
                "flex h-44px w-44px flex-col items-center justify-center gap-2px rounded-4px border border-dashed border-gray-300 bg-gray-50 dark:border-gray-600 dark:bg-gray-800",
            },
            [
              h(
                "svg",
                {
                  viewBox: "0 0 24 24",
                  width: "14",
                  height: "14",
                  fill: "none",
                  stroke: "currentColor",
                  "stroke-width": "1.5",
                },
                [
                  h("path", {
                    d: "M3 8.5A2.5 2.5 0 0 1 5.5 6h1.2a2 2 0 0 0 1.7-1l.5-.8A2 2 0 0 1 10.6 3h2.8a2 2 0 0 1 1.7 1.2l.5.8a2 2 0 0 0 1.7 1h1.2A2.5 2.5 0 0 1 21 8.5v8A2.5 2.5 0 0 1 18.5 19h-13A2.5 2.5 0 0 1 3 16.5v-8Z",
                    "stroke-linejoin": "round",
                  }),
                  h("circle", { cx: "12", cy: "12.5", r: "3.2" }),
                ],
              ),
              h("span", { class: "text-11px leading-12px text-gray-500" }, canEdit ? "補封面" : "暫無封面"),
            ],
          ),
      );
    },
  },
  {
    title: "商品名",
    key: "name",
    minWidth: 140,
    render: (row) =>
      h("div", { class: "flex flex-col gap-4px" }, [
        h("span", null, row.name),
        row.is_locked
          ? h(
              NTag,
              { size: "small", bordered: false },
              { default: () => "已鎖定 · 編輯和同步受保護" },
            )
          : null,
        row.is_recommend
          ? h(
              NTag,
              { size: "small", type: "warning", bordered: false },
              { default: () => "首頁推薦" },
            )
          : null,
      ]),
  },
  {
    title: "分類",
    key: "category_id",
    width: 84,
    render: (row) => categories.value.find((c: any) => c.id === row.category_id)?.name || "-",
  },
  {
    // 價格塊：售價/成本/加價三行合一（標籤與數值緊鄰，行內鉛筆氣泡改價）
    title: "價格",
    key: "pricing",
    width: 168,
    render: (row) => priceCell(row),
  },
  {
    // 庫存/已售塊（與價格塊同風格）：卡密類=可用數（0 紅）；鏈接/兌換碼類=不限
    title: "庫存 / 已售",
    key: "stats",
    width: 112,
    render: (row) => statsCell(row),
  },
  {
    title: "貨源",
    key: "upstream_source_id",
    width: 150,
    render: (row) => {
      if (!row.upstream_source_id) {
        return h(NTag, { size: "small", bordered: false }, { default: () => "自營" });
      }
      const conn = supplyConnections.value.find((c: any) => c.id === row.upstream_source_id);
      const name = conn?.name || `#${row.upstream_source_id}`;
      // 上游商品鏈接（渠道配置 product_url_template：{base}/{code} 佔位）
      let link = "";
      if (conn) {
        try {
          const tpl = JSON.parse(conn.settings || "{}").product_url_template;
          if (tpl && row.upstream_product_code) {
            link = tpl
              .replaceAll("{base}", conn.base_url)
              .replaceAll("{code}", row.upstream_product_code);
          }
        } catch {
          /* 無模板 */
        }
      }
      return h("div", { class: "flex items-center gap-4px" }, [
        h(
          NTag,
          { size: "small", bordered: false, type: "info" },
          { default: () => `代發 · ${name}` },
        ),
        link
          ? h(
              "a",
              {
                href: link,
                target: "_blank",
                rel: "noopener noreferrer",
                title: link,
                class: "text-12px text-blue-500 hover:underline",
              },
              "↗",
            )
          : null,
      ]);
    },
  },
  {
    title: "類型",
    key: "stock_type",
    width: 64,
    render: (row) => {
      if ((row.product_property || row.goods_type) === "physical") return "實體";
      if (row.product_kind === "sms_channel") return "接碼";
      if (row.fulfillment_mode === "manual") return "人工服務";
      const map: Record<string, string> = { card: "卡密", url: "鏈接", code: "兌換碼" };
      return map[row.stock_type] || row.stock_type;
    },
  },
  {
    title: "自動管理 / 原因",
    key: "auto_listing",
    width: 230,
    render: (row) =>
      h("div", { class: "flex flex-col gap-4px" }, [
        h(
          NButton,
          {
            size: "small",
            text: true,
            type: row.auto_listing ? "primary" : "default",
            disabled: row.is_locked || !checkAuth("catalog:write"),
            onClick: () => openListing([row.id], row.auto_listing ? "disable" : "enable"),
          },
          {
            default: () =>
              row.is_locked
                ? "已鎖定 · 不自動調整"
                : row.auto_listing
                  ? "自動上下架 · 已開啓"
                  : "自動上下架 · 未開啓",
          },
        ),
        row.listing_observed_at
          ? h(
              "span",
              { class: "text-12px" },
              `檢查於 ${new Date(Number(row.listing_observed_at) * 1000).toLocaleString()}`,
            )
          : null,
        h(
          "span",
          { class: "text-12px" },
          row.listing_message ||
            (
              {
                manual: "人工設置",
                stock_out: "缺貨自動下架",
                upstream_unavailable: "上游不可售，需人工核實",
                stock_recovered: "補貨自動恢復",
              } as Record<string, string>
            )[row.listing_reason] ||
            "人工管理",
        ),
      ]),
  },
  {
    title: "狀態",
    key: "status",
    width: 84,
    render: (row) =>
      row.is_locked || !checkAuth("catalog:write")
        ? h(
            NTag,
            { size: "small", bordered: false },
            {
              default: () => (row.status === 1 ? "已上架" : row.status === 2 ? "已隱藏" : "已下架"),
            },
          )
        : // 標籤即開關（大廠模式）：點擊標籤 Popconfirm 確認上下架
          h(
            NPopconfirm,
            {
              onPositiveClick: () =>
                handleBatchStatus(
                  [row.id],
                  row.status === 1 ? 0 : 1,
                  row.status === 1 ? "下架" : "上架",
                ),
            },
            {
              trigger: () =>
                h(
                  NTag,
                  {
                    type: row.status === 1 ? "success" : row.status === 2 ? "warning" : "default",
                    size: "small",
                    class: "cursor-pointer",
                    style: "cursor: pointer",
                  },
                  {
                    default: () =>
                      `${row.status === 1 ? "上架" : row.status === 2 ? "隱藏" : "下架"}${row.status === 2 ? " ↻" : ""}`,
                  },
                ),
              default: () =>
                `是否${row.status === 1 ? "下架" : "上架"}「${row.name}」？此操作會暫停自動上下架。`,
            },
          ),
  },
  {
    // 推薦列（標籤即開關，與狀態列同款）：點擊切換首頁推薦——列表直接可設，無需進編輯表單
    title: "首頁推薦",
    key: "is_recommend",
    width: 84,
    render: (row) => {
      if (row.is_locked || !checkAuth("catalog:write")) {
        return h(
          NTag,
          { type: row.is_recommend ? "warning" : "default", size: "small", bordered: false },
          { default: () => (row.is_recommend ? "已推薦" : "-") },
        );
      }
      return h(
        NPopconfirm,
        { onPositiveClick: () => toggleRecommend(row) },
        {
          trigger: () =>
            h(
              NTag,
              {
                type: row.is_recommend ? "warning" : "default",
                size: "small",
                bordered: !!row.is_recommend,
                class: "cursor-pointer",
                style: "cursor: pointer",
              },
              { default: () => (row.is_recommend ? "⭐ 已推薦" : "設推薦") },
            ),
          default: () =>
            row.is_recommend
              ? `取消「${row.name}」的首頁推薦？`
              : `將「${row.name}」設為首頁推薦（storefront 首頁推薦位展示）？`,
        },
      );
    },
  },
  {
    title: "操作",
    key: "actions",
    width: 144,
    fixed: "right",
    render: (row) =>
      h(
        NSpace,
        { size: "small" },
        {
          default: () => [
            checkAuth("inventory:import") && row.stock_type === "card" && row.product_kind !== "social_service" && (row.product_property || row.goods_type) !== "physical"
              ? h(NButton, { size: "small", disabled: row.is_locked, onClick: () => router.push({ name: "inventory", query: { product_id: row.id, open_import: "1" } }) }, { default: () => "導入卡密" })
              : null,
            checkAuth("catalog:write")
              ? h(
                  NButton,
                  { size: "small", onClick: () => handleEdit(row) },
                  { default: () => (row.is_locked ? "查看" : "編輯") },
                )
              : null,
            checkAuth("catalog:write") && canCopyProduct(row)
              ? h(
                  NButton,
                  { size: "small", disabled: openingProduct.value, onClick: () => openProduct(row, 1, true) },
                  { default: () => "複製" },
                )
              : null,
            checkAuth("catalog:write") && row.upstream_source_id && row.product_kind !== "social_service"
              ? h(
                  NButton,
                  { size: "small", onClick: () => openDeliverySources(row) },
                  { default: () => "發貨設置" },
                )
              : null,
            checkAuth("catalog:lock")
              ? h(
                  NButton,
                  {
                    size: "small",
                    loading: lockBusy.value === row.id,
                    disabled: lockBusy.value !== null,
                    onClick: () => changeLock(row),
                  },
                  { default: () => (row.is_locked ? "解鎖" : "鎖定") },
                )
              : null,
            checkAuth("catalog:review_read")
              ? h(
                  NButton,
                  { size: "small", quaternary: true, onClick: () => openReviews(row) },
                  { default: () => "評價" },
                )
              : null,
            checkAuth("catalog:delete")
              ? h(
                  NButton,
                  {
                    size: "small",
                    type: "error",
                    title: "刪除商品，保留訂單快照",
                    onClick: () => {
                      deleteTarget.value = row;
                    },
                  },
                  { default: () => "刪除" },
                )
              : null,
          ],
        },
      ),
  },
];

let listSequence = 0;
async function loadList() {
  const sequence = ++listSequence;
  loading.value = true;
  try {
    const { data, error } = await fetchProducts({
      ...currentFilters(),
      page: page.value,
      page_size: pageSize.value,
    });
    if (sequence !== listSequence) return;
    if (!error && data) {
      products.value = (data as any).products || [];

      total.value = (data as any).total || 0;
    }
  } finally {
    if (sequence === listSequence) loading.value = false;
  }
}

// 篩選/搜索變化：回第 1 頁重查（停留舊頁可能超出範圍顯示空列表）
function onSearch() {
  page.value = 1;
  loadList();
}

// ── 左側分類樹（含「全部分類」根節點；大廠交互：左樹右列表）──
const filterCategoryTree = computed(() => [
  { key: 0, label: "全部分類" },
  ...categoryTreeOptions.value,
]);
const selectedCatKeys = ref<Array<string | number>>([0]);

function onTreeSelect(keys: Array<string | number>) {
  selectedCatKeys.value = keys.length ? keys : [0];
  const k = keys[0];
  categoryFilter.value = typeof k === "number" && k > 0 ? k : null;
  onSearch();
}

// 懸停顯示完整分類名（長名不截斷可讀）+ 單行省略 class（CSS 收口）
function catNodeProps({ option }: { option: any }) {
  return { title: option.label || "", class: "cat-node" };
}

// 優先展示分類自定義圖標，缺省或圖片加載失敗時按節點類型回退。
function catRenderPrefix({ option }: { option: any }) {
  const icon = option.key === 0 ? "🏠" : option.children?.length ? "📁" : "🏷️";
  return h(CategoryIcon, { icon: option.icon, fallback: icon, class: "cat-prefix" });
}

// ── 展開/收縮全部（分類多時快速導航）──
const expandedCatKeys = ref<Array<string | number>>([]);

function collectAllKeys(nodes: any[]): Array<string | number> {
  const keys: Array<string | number> = [];
  const walk = (list: any[]) => {
    for (const n of list) {
      keys.push(n.key);
      if (n.children?.length) walk(n.children);
    }
  };
  walk(nodes);
  return keys;
}

function expandAllCats() {
  expandedCatKeys.value = collectAllKeys(filterCategoryTree.value);
}

function collapseAllCats() {
  expandedCatKeys.value = [];
}

async function loadCategories() {
  const { data, error } = await fetchCategories();
  if (!error && data) categories.value = (data as any).categories || [];
  expandAllCats(); // 分類加載完默認全部展開
}

async function loadConnections() {
  // catalog.ts 只讀別名；無權限時靜默降級為空（貨源列回落渠道 #ID，下拉僅剩「自營」）
  const { data, error } = await fetchSupplyConnectionOptions();
  if (!error && data) supplyConnections.value = (data as any).connections || [];
}

// 渠道篩選下拉選項（自營 + 各供貨渠道）
const supplyOptions = computed(() => [
  ...supplyConnections.value.map((c: any) => ({ label: c.name, value: c.id })),
]);

// 批量上下架（多選工具欄與行內切換共用；status 1=上架 0=下架）
async function handleBatchStatus(ids: number[], status: number, label: string) {
  if (!ids.length) return;
  const { data, error } = await batchUpdateProductStatus(ids, status);
  if (!error) {
    batchReport.value = `已${label} ${data?.updated || 0} 件，跳過鎖定商品 ${data?.skipped_locked || 0} 件`;
    batchFailures.value = [];
    batchReport.value += "；人工設置已暫停相關商品自動上下架";
    window.$message?.success(batchReport.value);
    checkedKeys.value = [];
    loadList();
  }
}

// 彈窗固定本次所選商品；取消或請求失敗均保留原選擇。
const showClassifyProducts = ref(false);
const showBatchCategory = ref(false);
const batchCategorySaving = ref(false);
const batchCategoryIds = ref<number[]>([]);
const batchCategoryId = ref<number | null>(null);
const batchCategoryOptions = computed(() => {
  const byId = new Map(categories.value.map((c) => [c.id, c]));
  return categories.value.map((c) => {
    const names: string[] = [];
    const seen = new Set<number>();
    let node = c;
    let hidden = false;
    while (node && !seen.has(node.id)) {
      seen.add(node.id);
      names.unshift(node.name);
      hidden ||= Boolean(node.hide);
      node = byId.get(node.parent_id);
    }
    return { label: names.join(" / ") + (hidden ? "（已隱藏）" : ""), value: c.id, hidden };
  });
});
const batchCategoryTarget = computed(() =>
  batchCategoryOptions.value.find((c) => c.value === batchCategoryId.value),
);
async function openBatchCategory() {
  if (!checkedKeys.value.length) return;
  batchCategoryIds.value = [...checkedKeys.value];
  batchCategoryId.value = null;
  await loadCategories();
  showBatchCategory.value = true;
}
async function saveBatchCategory() {
  if (!batchCategoryId.value || batchCategorySaving.value) return;
  batchCategorySaving.value = true;
  try {
    const { data, error } = await batchUpdateProductCategory(
      batchCategoryIds.value,
      batchCategoryId.value,
    );
    if (!error && data) {
      batchReport.value = `已將 ${data.updated || 0} 件商品移至「${batchCategoryTarget.value?.label}」，跳過鎖定商品 ${data.skipped_locked || 0} 件`;
      batchFailures.value = [];
      window.$message?.success(batchReport.value);
      showBatchCategory.value = false;
      checkedKeys.value = [];
      await loadList();
    }
  } finally {
    batchCategorySaving.value = false;
  }
}

// ── 批量刪除（保留訂單快照及履約記錄）──
const batchDeleting = ref(false);
async function handleBatchDelete() {
  if (!checkedKeys.value.length || batchDeleting.value) return;
  const ids = [...checkedKeys.value];
  batchDeleting.value = true;
  try {
    // 順序提交，避免 SQLite 寫鎖競爭；過程中改變選擇不影響本次刪除範圍。
    const results: Awaited<ReturnType<typeof deleteProduct>>[] = [];
    for (const id of ids) results.push(await deleteProduct(id));
    const ok = results.filter((r) => !r.error).length;
    batchFailures.value = results.flatMap((r, i) => r.error ? [
      `${products.value.find((p) => p.id === ids[i])?.name || `商品 ${ids[i]}`}：${(r.error as any)?.response?.data?.message || "刪除未成功，請刷新後重試"}`,
    ] : []);
    batchReport.value = `已刪除 ${ok} 件，保留訂單記錄，失敗 ${batchFailures.value.length} 件`;
    checkedKeys.value = ids.filter((_, i) => !!results[i].error);
    await loadList();
    await loadCategories();
  } finally {
    batchDeleting.value = false;
  }
}

function resetForm() {
  copying.value = false;
  copySourceId.value = 0;
  editingProduct.value = null;
  independentlySaved.value = false;
  contentProtection.value = { cover: false, description: false };
  editingId.value = 0;
  step.value = 1; // 新開彈窗回到第一步
  Object.assign(formData, {
    name: "",
    category_id: null,
    description: "",
    cover: [],
    images: [],
    product_kind:"standard",
    price_yuan: 0,
    factory_price_yuan: 0,
    points_required: 0,
    product_property: "virtual",
    track_inventory: true,
    sales_visible: true,
    shipping_mode: "free",
    shipping_fee_yuan: 0,
    shipping_countries: [] as string[],
    physical_stock: 0,
    stock_type: "card",
    fulfillment_mode: "auto",
    manual_stock: -1,
    direct_content: "",
    delivery_mode: "status",
    stock_visible: true,
    dedup: true,
    sort: 0,
    status: 1,
    is_recommend: false,
  });
  directContentSet.value = false;
  initialProduct.value = JSON.stringify(buildPayload());
}

function canCopyProduct(p: any) {
  return (!p.product_kind || p.product_kind === "standard") && p.delivery_kind !== "social_service";
}

function handleEdit(row: any, initialStep = 1) {
  return openProduct(row, initialStep, false);
}

async function openProduct(row: any, initialStep: number, copy: boolean) {
  if (openingProduct.value || showCreate.value) return;
  openingProduct.value = true;
  try {
    // 列表可能不含詳情和完整交付設置。
    const { data, error } = await fetchProduct(row.id);
    if (error || !data || showCreate.value) return;
    if (copy && !canCopyProduct(data)) {
      window.$message?.warning("此商品由貨源動態報價，請從貨源管理添加");
      return;
    }
    fillProductForm(data, copy);
    copying.value = copy;
    copySourceId.value = copy && (copySKUs.value || copyControls.value) ? data.id : 0;
    step.value = initialStep;
    independentlySaved.value = false;
    initialProduct.value = JSON.stringify(buildPayload());
    showCreate.value = true;
  } finally {
    openingProduct.value = false;
  }
}

function fillProductForm(p: any, copy: boolean) {
  editingProduct.value = copy ? null : p;
  editingId.value = copy ? 0 : p.id;
  contentProtection.value = { cover: !copy && !!p.cover_protected, description: !copy && !!p.description_protected };
  Object.assign(formData, {
    name: copy ? `${p.name}（副本）` : p.name,
    category_id: p.category_id || null,
    description: p.description || "",
    cover: p.cover ? [p.cover] : [],
    images: [...(p.images || [])],
    product_kind:p.product_kind || (p.delivery_kind === "social_service" ? "social_service" : "standard"),
    price_yuan: Number(centsToYuan(p.price_cents)),
    factory_price_yuan: p.factory_price_cents ? Number(centsToYuan(p.factory_price_cents)) : 0,
    points_required: p.points_required || 0,
    product_property: p.product_property || p.goods_type || "virtual",
    track_inventory: p.track_inventory !== false,
    sales_visible: p.sales_visible !== false,
    shipping_mode: p.shipping_mode || "free",
    shipping_fee_yuan: centsToYuan(p.shipping_fee_cents || 0),
    shipping_countries: [...(p.shipping_countries || [])],
    physical_stock: Number(p.physical_stock || 0),
    stock_type: p.stock_type,
    fulfillment_mode: p.fulfillment_mode || "auto",
    manual_stock: Number(p.manual_stock ?? -1),
    direct_content: "",
    delivery_mode: p.delivery_mode || "status",
    stock_visible: p.stock_visible !== false,
    dedup: p.dedup !== false,
    sort: p.sort || 0,
    status: p.status,
    is_recommend: !!p.is_recommend,
  });
  if (copy) {
    const manual = p.fulfillment_mode === "manual";
    Object.assign(formData, {
      physical_stock: 0,
      fulfillment_mode: manual ? "manual" : "auto",
      manual_stock: manual ? 0 : -1,
      status: 0,
      is_recommend: false,
    });
  }
  directContentSet.value = !copy && !!p.has_direct_content;
}

function buildPayload() {
  // 元 → 分（鐵律 15：提交統一 *100，經 utils/money 防浮點）
  const physical = formData.product_property === "physical";
  return {
    ...(copySourceId.value ? { copy_from_product_id: copySourceId.value } : {}),
    name: formData.name,
    category_id: formData.category_id || 0,
    description: formData.description,
    cover: formData.cover[0] || "",
    images: formData.images,
    images_set: true,
    price_cents: isDynamicProduct.value ? 0 : yuanToFen(formData.price_yuan),
    factory_price_cents: isDynamicProduct.value ? 0 : yuanToFen(formData.factory_price_yuan || 0),
    points_required: physical || isDynamicProduct.value ? 0 : formData.points_required || 0,
    product_property: formData.product_property,
    goods_type: formData.product_property,
    track_inventory: physical ? formData.track_inventory : true,
    sales_visible: formData.sales_visible,
    shipping_mode: formData.shipping_mode,
    shipping_fee_cents: physical && formData.shipping_mode === "fixed" ? yuanToFen(formData.shipping_fee_yuan || 0) : 0,
    shipping_countries: formData.shipping_countries,
    ...(physical && formData.track_inventory
      ? {
          physical_stock: formData.physical_stock,
          expected_physical_stock: Number(editingProduct.value?.physical_stock || 0),
        }
      : {}),
    stock_type: physical ? "card" : formData.stock_type,
    fulfillment_mode: physical ? "auto" : formData.fulfillment_mode,
    ...(!physical && !isSocialProduct.value ? { manual_stock: formData.manual_stock } : {}),
    ...(!physical && !isDynamicProduct.value && formData.direct_content ? { direct_content: formData.direct_content } : {}),
    delivery_mode: physical ? "status" : formData.delivery_mode,
    stock_visible: formData.stock_visible,
    dedup: physical ? false : formData.dedup,
    sort: formData.sort || 0,
    status: formData.status,
    is_recommend: formData.is_recommend,
  };
}

async function handleSave() {
  if (editorLocked.value) return;
  if (editorBusy.value) return;
  if (controlPanel.value?.hasPending) {
    window.$message?.warning("下單控件還有未保存輸入，請先點擊創建/更新控件或取消編輯");
    return;
  }
  if (!formData.name.trim()) {
    step.value = 1;
    window.$message?.warning("請先填寫商品名稱");
    return;
  }
  if (!isDynamicProduct.value && formData.price_yuan <= 0) {
    step.value = 2;
    window.$message?.warning("請先填寫有效售價");
    return;
  }
  saving.value = true;
  try {
    if (skuPanel.value && !(await skuPanel.value.savePending())) return;
    const payload = buildPayload();
    const { error } = editingId.value
      ? await updateProduct(editingId.value, payload)
      : await createProduct(payload);
    if (!error) {
      window.$message?.success(editingId.value ? "更新成功" : "創建成功");
      showCreate.value = false;
      resetForm();
      loadList();
    } else if (
      (error as any)?.response?.data?.reason === "catalog.PRODUCT_LOCKED" &&
      editingId.value
    ) {
      const { data: latest } = await fetchProduct(editingId.value);
      if (latest) editingProduct.value = latest as any;
      window.$message?.warning("商品已被鎖定，未提交的輸入仍保留在當前頁面。");
    }
  } finally {
    saving.value = false;
  }
}

// 創建態「保存並配置」（第 4 步 CTA）：先建商品（SKU/控件 API 依賴 product_id），
// 彈窗原地轉編輯態並停留在本步——規格/控件面板隨即激活，一次流程走完
async function saveAndContinue() {
  if (editorBusy.value) return;
  if (!formData.name || (!isDynamicProduct.value && formData.price_yuan <= 0)) {
    window.$message?.warning("請先完成商品名稱與售價");
    return;
  }
  saving.value = true;
  try {
    const { data, error } = await createProduct(buildPayload());
    if (!error) {
      const created = (data as any) || {};
      if (created.id) {
        editingId.value = created.id;
        copying.value = false;
        copySourceId.value = 0;
        editingProduct.value = created;
        initialProduct.value = JSON.stringify(buildPayload());
        independentlySaved.value = true;
        window.$message?.success("商品已創建，可繼續配置規格與控件");
        loadList();
      }
    }
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  if (route.query.low_stock === "1") {
    statusFilter.value = 0;
    inventoryFilter.value = "low";
  }
  loadList();
  loadCategories();
  loadConnections();
});
</script>

<template>
  <div class="product-management min-h-500px flex flex-1 gap-16px overflow-hidden">
    <ListingManagement
      v-model:show="showListing"
      :ids="listingIds"
      :filter="listingFilter"
      :initial-action="listingAction"
      :scope-label="listingScope"
      @saved="listingSaved"
    />
    <DeleteProductModal
      :show="!!deleteTarget"
      :product="deleteTarget"
      @update:show="!$event && (deleteTarget = null)"
      @deleted="loadList(); loadCategories()"
    />
    <!-- 左側：分類樹（大廠後台交互——左樹篩選 + 右列表；懸停顯示完整分類名） -->
    <NCard
      title="商品分類"
      class="product-category-card shrink-0"
      :content-style="{
        display: 'flex',
        flexDirection: 'column',
        minHeight: 0,
        overflow: 'hidden',
      }"
    >
      <template #header-extra>
        <NButton
          v-auth="'catalog:category_write'"
          size="tiny"
          quaternary
          @click="showCategory = true"
          >管理</NButton
        >
      </template>
      <!-- 展開/收起獨立成行（放 header 時窄屏與標題/管理按鈕擠在一行會換行錯位） -->
      <div class="mb-8px flex shrink-0 items-center gap-8px">
        <NButton size="tiny" quaternary class="flex-1" @click="expandAllCats">展開全部</NButton>
        <NButton size="tiny" quaternary class="flex-1" @click="collapseAllCats">收起全部</NButton>
      </div>
      <div class="product-category-scroll">
        <NTree
          block-line
          :indent="14"
          :data="filterCategoryTree"
          :selected-keys="selectedCatKeys"
          v-model:expanded-keys="expandedCatKeys"
          :node-props="catNodeProps"
          :render-prefix="catRenderPrefix"
          @update:selected-keys="onTreeSelect"
        />
      </div>
    </NCard>
    <!-- 右側：商品列表 -->
    <NCard
      title="商品管理"
      class="product-list-card min-w-0 flex-1"
      :content-style="{
        display: 'flex',
        flexDirection: 'column',
        minHeight: 0,
        overflow: 'hidden',
      }"
    >
      <div class="mb-16px flex shrink-0 flex-wrap items-center gap-12px">
        <NButton
          v-auth="'catalog:write'"
          type="primary"
          @click="
            resetForm();
            showCreate = true;
          "
        >
          新增商品
        </NButton>
        <NButton v-auth="'catalog:category_write'" @click="showCategory = true"> 分類管理 </NButton>
        <NInput
          v-model:value="keyword"
          placeholder="搜索商品名"
          clearable
          style="width: 200px; max-width: 100%"
          @keyup.enter="onSearch"
        />
        <NSelect
          v-model:value="supplyFilter"
          :options="supplyOptions"
          placeholder="全部渠道"
          clearable
          style="width: 170px; max-width: 100%"
          @update:value="onSearch"
        />
        <NButton
          size="small"
          quaternary
          :type="supplyFilter === 0 ? 'primary' : 'default'"
          @click="((supplyFilter = supplyFilter === 0 ? null : 0), onSearch())"
        >
          {{ supplyFilter === 0 ? "✓ 僅自營" : "僅自營" }}
        </NButton>
        <NButton @click="onSearch">搜索</NButton>
        <NButton
          v-auth="'catalog:write'"
          @click="loadCategories().then(() => (showClassifyProducts = true))"
          >按關鍵詞分類</NButton
        >
      </div>

      <div class="mb-12px flex flex-wrap items-center gap-12px">
        <NSelect
          v-model:value="lockFilter"
          :options="[
            { label: '已鎖定', value: 'locked' },
            { label: '未鎖定', value: 'unlocked' },
          ]"
          placeholder="全部鎖定狀態"
          clearable
          style="width: 170px"
          @update:value="onSearch"
        />
        <template v-if="categoryFilter"
          ><span>{{ selectedCategoryName }} · 包含子分類</span
          ><NButton v-auth="'catalog:category_read'" @click="showPlacements = true"
            >置頂與推薦</NButton
          ></template
        >
        <span v-if="pageLockedCount" class="text-13px"
          >本頁 {{ pageLockedCount }} 件已鎖定，編輯和同步會跳過，刪除仍可選。</span
        >
      </div>
      <NAlert
        v-if="batchReport"
        :type="batchFailures.length ? 'warning' : 'success'"
        closable
        class="mb-12px"
        @close="batchReport = ''"
        >{{ batchReport }}
        <ul v-if="batchFailures.length">
          <li v-for="failure in batchFailures" :key="failure">{{ failure }}</li>
        </ul></NAlert
      >
      <div class="mb-8px flex flex-wrap items-center gap-8px shrink-0">
        <span>上架狀態</span
        ><FilterTabs v-model:value="statusFilter" :options="statusTabs" @change="onSearch" />
      </div>
      <div class="mb-12px flex flex-wrap items-center gap-8px shrink-0">
        <span>庫存狀態</span
        ><FilterTabs v-model:value="inventoryFilter" :options="inventoryTabs" @change="onSearch" />
      </div>
      <div class="mb-12px flex flex-wrap items-center gap-8px shrink-0">
        <NButton :type="restockedOnly ? 'primary' : 'default'" @click="showRestocked"
          >{{ restockedOnly ? "✓ " : "" }}補貨待上架</NButton
        >
        <NSelect
          v-model:value="autoListingFilter"
          :options="[
            { label: '自動管理已開啓', value: 'enabled' },
            { label: '自動管理未開啓', value: 'disabled' },
          ]"
          clearable
          placeholder="全部管理方式"
          style="width: 180px"
          @update:value="onSearch"
        />
        <NButton v-auth="'catalog:write'" :disabled="loading || !total" @click="openListing()"
          >管理全部篩選結果（{{ total }} 件）</NButton
        >
        <NButton
          v-auth="'supply:sync'"
          :disabled="!supplyFilter || supplyFilter < 0 || checkingStock"
          :loading="checkingStock"
          @click="checkChannelStock"
          >檢查該貨源庫存與補貨</NButton
        >
        <span class="text-12px"
          >{{
            supplyFilter ? "按所選貨源後台檢查" : "選擇一個貨源後可檢查庫存"
          }}；待確認包含過期或查詢失敗，不能當作缺貨。</span
        >
        <NButton
          v-if="restockedOnly"
          text
          @click="
            restockedOnly = false;
            onSearch();
          "
          >取消補貨限定</NButton
        >
      </div>
      <NAlert
        v-if="stockCheckMessage"
        type="info"
        class="mb-12px"
        role="status"
        aria-live="polite"
        >{{ stockCheckMessage }}</NAlert
      >

      <!-- 批量操作條（勾選後出現） -->
      <div
        v-if="checkedKeys.length || categoryFilter"
        class="mb-12px flex shrink-0 flex-wrap items-center gap-8px rounded-6px bg-primary-50 px-12px py-8px dark:bg-gray-800"
      >
        <span class="text-13px"
          >已選 <b>{{ checkedKeys.length }}</b> 件<span v-if="pageLockedCount"
            >，編輯操作會跳過本頁 {{ pageLockedCount }} 件鎖定商品</span
          ><span v-if="offPageSelected">（其他頁 {{ offPageSelected }} 件）</span></span
        >
        <NPopconfirm @positive-click="handleBatchStatus([...checkedKeys], 1, '上架')">
          <template #trigger>
            <NButton
              v-auth="'catalog:write'"
              size="small"
              type="success"
              :disabled="!checkedKeys.length"
              >批量上架</NButton
            >
          </template>
          確定上架選中的 {{ checkedKeys.length }} 件商品？同時暫停這些商品的自動上下架。
        </NPopconfirm>
        <NPopconfirm @positive-click="handleBatchStatus([...checkedKeys], 0, '下架')">
          <template #trigger>
            <NButton
              v-auth="'catalog:write'"
              size="small"
              type="warning"
              :disabled="!checkedKeys.length"
              >批量下架</NButton
            >
          </template>
          確定下架選中的 {{ checkedKeys.length }} 件商品？同時暫停這些商品的自動上下架。
        </NPopconfirm>
        <NButton
          v-auth="'catalog:write'"
          size="small"
          :disabled="!checkedKeys.length"
          @click="openListing(checkedKeys)"
          >自動上下架設置</NButton
        >
        <NButton v-auth="'catalog:write'" size="small" type="primary" @click="openBatchContent"
          >批量修改內容</NButton
        >
        <NButton
          v-auth="'catalog:write'"
          size="small"
          type="primary"
          :disabled="!checkedKeys.length"
          @click="openBatchCategory"
          >修改分類</NButton
        >
        <NPopconfirm @positive-click="handleBatchDelete">
          <template #trigger>
            <NButton
              v-auth="'catalog:delete'"
              size="small"
              type="error"
              :loading="batchDeleting"
              :disabled="!checkedKeys.length"
              >批量刪除</NButton
            >
          </template>
          確定刪除選中的
          {{ checkedKeys.length }}
          件商品？商品立即從商城和供貨目錄移除，剩餘庫存歸檔，歷史訂單保留；失敗不影響其餘商品。
        </NPopconfirm>
        <NButton v-if="checkedKeys.length" size="small" quaternary @click="checkedKeys = []"
          >取消選擇</NButton
        >
      </div>

      <NDataTable
        flex-height
        :scroll-x="1200"
        class="min-h-0 flex-1"
        :columns="columns"
        :data="products"
        :loading="loading"
        :row-key="(row: any) => row.id"
        :checked-row-keys="checkedKeys"
        @update:checked-row-keys="updateCheckedKeys"
      />

      <!-- 可複用分頁條（共N條/首頁/頁碼/每頁條數/跳頁/末頁） -->
      <TablePager
        class="shrink-0"
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="total"
        @change="loadList"
      />
    </NCard>

    <CategoryPlacements
      v-model:show="showPlacements"
      :category-id="categoryFilter || 0"
      :category-name="selectedCategoryName"
      :categories="categories"
    />
    <ClassifyProducts
      v-model:show="showClassifyProducts"
      :categories="batchCategoryOptions"
      @saved="loadList"
    />
    <BatchContentModal
      v-model:show="showBatchContent"
      :ids="batchContentIDs"
      :category-id="batchContentCategory"
      :categories="batchCategoryOptions"
      @saved="contentSaved"
    />

    <NModal
      v-model:show="showBatchCategory"
      preset="card"
      title="批量修改分類"
      class="w-560px max-w-[calc(100vw-32px)]"
      :mask-closable="!batchCategorySaving"
      :closable="!batchCategorySaving"
      :close-on-esc="!batchCategorySaving"
    >
      <p class="mb-16px">
        將選中的 <b>{{ batchCategoryIds.length }}</b> 件商品移至同一個分類。
      </p>
      <NFormItem label="目標分類" required>
        <NSelect
          v-model:value="batchCategoryId"
          :options="batchCategoryOptions"
          filterable
          placeholder="搜索分類名稱或完整路徑"
          :disabled="batchCategorySaving"
        />
      </NFormItem>
      <p v-if="batchCategoryTarget" class="mb-12px break-words">
        目標：{{ batchCategoryTarget.label }}
      </p>
      <NAlert v-if="batchCategoryTarget?.hidden" type="warning" class="mb-12px">
        目標分類或其上級已隱藏，移入後這些商品將不在前台展示。
      </NAlert>
      <p class="text-13px text-gray-500">僅修改分類，商品價格、庫存、規格和上下架狀態保持不變。</p>
      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton :disabled="batchCategorySaving" @click="showBatchCategory = false">取消</NButton>
          <NButton
            type="primary"
            :loading="batchCategorySaving"
            :disabled="!batchCategoryId"
            @click="saveBatchCategory"
            >確認修改</NButton
          >
        </div>
      </template>
    </NModal>

    <DeliverySources
      v-if="deliveryProduct"
      v-model:show="showDeliverySources"
      :product-id="deliveryProduct.id"
      :readonly="deliveryProduct.is_locked"
      @saved="deliverySaved"
    />
    <!-- 新增/編輯彈窗（分步表單：基礎 → 價格庫存 → 商品描述 → 規格與控件 → 高級設置） -->
    <NModal
      :show="showCreate"
      @update:show="!$event && requestClose()"
      :closable="!editorBusy"
      :mask-closable="!editorBusy"
      :close-on-esc="!editorBusy"
      @after-leave="afterEditorLeave"
      preset="card"
      :title="editingId ? '編輯商品' : copying ? '複製商品' : '新增商品'"
      class="w-780px"
      :style="{
        width: step === 3 || step === 4 ? '880px' : undefined,
        maxWidth: 'calc(100vw - 24px)',
      }"
      :class="step === 3 ? 'product-editor-step-modal' : undefined"
    >
      <NAlert v-if="editorLocked" type="info" class="mb-12px"
        >商品已鎖定，編輯和同步受保護；仍可在列表刪除並保留訂單記錄。
        <span v-if="editingProduct.locked_at"
          >鎖定時間：{{ new Date(editingProduct.locked_at * 1000).toLocaleString() }}。</span
        >
        <NButton
          v-if="checkAuth('catalog:lock')"
          text
          :loading="lockBusy === editingId"
          @click="changeLock(editingProduct)"
          >解鎖商品</NButton
        ><span v-else>請聯繫有解鎖權限的管理員。</span>
      </NAlert>
      <div :inert="editorBusy || undefined">
        <NAlert v-if="copying" type="info" class="mb-12px">
          核對資料後保存為新商品，默認下架。
          <template v-if="copySKUs && copyControls">SKU 規格和下單控件會隨商品保存。</template>
          <template v-else-if="copySKUs">SKU 規格會隨商品保存。</template>
          <template v-else-if="copyControls">下單控件會隨商品保存。</template>
          庫存、貨源和直發內容需單獨配置。
        </NAlert>
        <NAlert v-if="independentlySaved" type="info" class="mb-12px"
          >已有內容單獨保存並生效；關閉只放棄尚未保存的輸入。</NAlert
        >
        <NSteps
          :current="step"
          size="small"
          class="mb-20px px-12px"
          @update:current="(v: number) => (step = v)"
        >
          <NStep title="基礎信息" />
          <NStep title="價格庫存" />
          <NStep title="圖片與詳情" description="商品描述" />
          <NStep v-if="!isSocialProduct" title="規格與控件" />
          <NStep v-if="formData.product_property !== 'physical' && !isSocialProduct" title="高級設置" />
        </NSteps>

        <!-- 商品描述步：完全展開（編輯器整高可見，不內滾）；其餘步驟限高內滾 -->
        <NScrollbar v-if="step !== 3" class="max-h-460px px-12px">
          <!-- 第 1 步：基礎信息 -->
          <NForm
            v-if="step === 1"
            :model="formData"
            :disabled="editorLocked"
            label-placement="left"
            label-width="100"
          >
            <NFormItem label="商品名稱" path="name" :rule="{ required: true }">
              <NInput v-model:value="formData.name" placeholder="請輸入商品名稱" />
            </NFormItem>
            <NFormItem label="商品分類">
              <div class="flex w-full items-center gap-8px">
                <NTreeSelect
                  v-model:value="formData.category_id"
                  :options="categoryTreeOptions"
                  placeholder="按層級選擇分類（可空）"
                  clearable
                  default-expand-all
                  class="flex-1"
                />
                <NButton
                  v-auth="'catalog:category_write'"
                  :disabled="editorLocked"
                  size="small"
                  quaternary
                  type="primary"
                  @click="showCategory = true"
                >
                  管理分類
                </NButton>
              </div>
            </NFormItem>
            <NFormItem label="商品屬性"
              ><NSelect
                v-model:value="formData.product_property"
                :disabled="!!editingId || !!copySourceId || formData.product_kind==='sms_channel'"
                :options="[
                  { label: '虛擬商品', value: 'virtual' },
                  { label: '實體商品（快遞配送）', value: 'physical' },
                ]"
            /></NFormItem>
            <template v-if="formData.product_property === 'physical'">
              <NFormItem label="運費方式"
                ><NSelect
                  v-model:value="formData.shipping_mode"
                  :options="[
                    { label: '包郵', value: 'free' },
                    { label: '固定運費', value: 'fixed' },
                    { label: '運費到付', value: 'collect' },
                  ]"
              /></NFormItem>
              <NAlert v-if="formData.shipping_mode === 'collect'" type="info" class="mb-12px">
                買家收貨時向快遞支付運費。發貨時請向承運公司選擇運費到付。
              </NAlert>
              <NFormItem v-if="formData.shipping_mode === 'fixed'" :label="`運費（${currencyUnit()}）`"
                ><NInputNumber
                  v-model:value="formData.shipping_fee_yuan"
                  :min="0.01"
                  :precision="2"
              /></NFormItem>
              <NFormItem label="配送國家"
                ><NSelect
                  v-model:value="formData.shipping_countries"
                  multiple
                  filterable
                  :options="countryOptions(shippingRegions)"
                  placeholder="選擇可配送的國家或地區"
              /></NFormItem>
              <NFormItem label="庫存管理">
                <NSwitch v-model:value="formData.track_inventory" />
                <span class="ml-8px text-12px text-gray-400">關閉後不限制下單數量，保留已有庫存記錄</span>
              </NFormItem>
              <NAlert v-if="inventoryModePending" type="info" class="mb-12px">
                庫存管理切換將在保存商品後生效。規格庫存暫按已保存的設置處理，請先保存商品再調整規格。
              </NAlert>
              <NFormItem v-if="formData.track_inventory" label="可售庫存"
                ><div>
                  <NInputNumber
                    v-model:value="formData.physical_stock"
                    :min="0"
                    :max="100000000"
                    :precision="0"
                  />
                  <p class="text-12px opacity-60">
                    無規格商品使用此庫存；多規格請在規格中調整。新增首個規格前須清空此庫存；已有無規格訂單請新建商品。數量不含待付款預佔。
                  </p>
                  <p v-if="inventoryModePending && formData.track_inventory" class="text-12px text-orange-500">
                    重新開啓前，請核對可售數量；多規格商品保存後請逐個核對規格庫存。
                  </p>
                </div></NFormItem
              >
              <p class="mb-12px text-12px opacity-60">
                同一商品多件或多規格，每單隻收一次運費。多個商品的運費相加。
              </p>
            </template>
            <NButton
              v-if="formData.product_property !== 'physical' && !isDynamicProduct && editingId && checkAuth('catalog:write')"
              class="mb-12px"
              @click="openDeliverySources(editingProduct)"
              >發貨設置：上游採購 / 我的卡密 / 重複發貨</NButton
            >
            <NFormItem v-if="formData.product_property !== 'physical' && !isDynamicProduct" label="交付方式"
              ><NSelect
                v-model:value="formData.fulfillment_mode"
                :disabled="['local', 'reuse'].includes(formData.fulfillment_mode)"
                :options="[
                  { label: '自動交付', value: 'auto' },
                  { label: '人工服務', value: 'manual' },
                  { label: '我的卡密（請在發貨設置修改）', value: 'local', disabled: true },
                  { label: '重複發貨（請在發貨設置修改）', value: 'reuse', disabled: true },
                ]"
            /></NFormItem>
            <NFormItem v-if="formData.product_property !== 'physical' && !isSocialProduct" label="人工可售總量"
              ><div class="w-full">
                <NInputNumber v-model:value="formData.manual_stock" :min="-1" :precision="0" />
                <p class="text-12px opacity-60">
                  -1 不限；包含已售及待付款佔用，所有人工規格共享。取消或全額退款釋放額度。
                </p>
              </div></NFormItem
            >
            <NFormItem
              v-if="formData.product_property !== 'physical' && !isSocialProduct && formData.fulfillment_mode !== 'manual'"
              label="庫存類型"
            >
              <NSelect v-model:value="formData.stock_type" :options="stockTypeOptions" />
            </NFormItem>
            <NFormItem
              v-if="
                formData.product_property !== 'physical' &&
                formData.fulfillment_mode !== 'manual' &&
                formData.stock_type !== 'card'
              "
              label="直發內容"
            >
              <div class="w-full">
                <NInput
                  v-model:value="formData.direct_content"
                  type="textarea"
                  :rows="3"
                  :placeholder="
                    directContentSet
                      ? '已設置（加密存儲不回顯）——留空保持不變，填寫則覆蓋'
                      : '買家支付後直接收到的內容，例如網盤鏈接、兌換碼、資源口令等。同一內容會發給每個買家，可反覆售賣。'
                  "
                />
                <div class="mt-4px text-11px text-gray-400">
                  鏈接/兌換碼商品無需導入卡密，買家付款後系統自動把該內容直接交付（庫存顯示「不限」）
                </div>
              </div>
            </NFormItem>
            <NFormItem label="狀態">
              <NRadioGroup v-model:value="formData.status">
                <NSpace>
                  <NRadio :value="1">上架</NRadio>
                  <NRadio :value="0">下架</NRadio>
                  <NRadio :value="2">隱藏（會員可見）</NRadio>
                </NSpace>
              </NRadioGroup>
            </NFormItem>
          </NForm>

          <!-- 第 2 步：價格庫存 -->
          <NForm
            v-else-if="step === 2"
            :model="formData"
            :disabled="editorLocked"
            label-placement="left"
            label-width="100"
          >
            <NAlert v-if="formData.product_kind==='sms_channel'" type="info" class="mb-12px">接碼渠道商品按用戶選擇實時定價。加價沿用貨源導入/映射中保存的比例或固定加價規則；修改規則後立即用於新報價。</NAlert>
            <NAlert v-if="isSocialProduct" type="info" class="mb-12px">社交服務按貨源購買規則動態報價；可編輯商品名稱、分類、推薦和圖片詳情。</NAlert>
            <NFormItem v-if="!isDynamicProduct" :label="`售價（${currencyUnit()}）`" path="price_yuan" :rule="{ required: true }">
              <NInputNumber
                v-model:value="formData.price_yuan"
                :min="0.01"
                :precision="2"
                :step="0.01"
                class="w-full"
              />
            </NFormItem>
            <NFormItem v-if="!isDynamicProduct" :label="`成本價（${currencyUnit()}）`">
              <NInputNumber
                v-model:value="formData.factory_price_yuan"
                :min="0"
                :precision="2"
                :step="0.01"
                class="w-full"
              />
            </NFormItem>
            <NFormItem v-if="formData.product_property !== 'physical' && !isDynamicProduct" label="積分價">
              <NInputNumber
                v-model:value="formData.points_required"
                :min="0"
                :precision="0"
                class="w-full"
              />
              <span class="ml-8px text-12px text-gray-400 whitespace-nowrap"
                >0 = 不參與積分商城</span
              >
            </NFormItem>
            <NFormItem label="前台顯示庫存">
              <NSwitch v-model:value="formData.stock_visible" />
              <span class="ml-8px text-12px text-gray-400">控制買家是否看到庫存數量，與庫存管理獨立</span>
            </NFormItem>
            <NFormItem label="前台顯示銷量">
              <NSwitch v-model:value="formData.sales_visible" />
              <span class="ml-8px text-12px text-gray-400">關閉後前台不顯示已售數量</span>
            </NFormItem>
            <NFormItem label="首頁推薦">
              <NSwitch v-model:value="formData.is_recommend" />
              <span class="ml-8px text-12px text-gray-400"
                >開啓後商品進入 storefront 首頁「推薦商品」區塊</span
              >
            </NFormItem>
            <NFormItem label="排序">
              <NInputNumber v-model:value="formData.sort" :precision="0" class="w-full" />
              <span class="ml-8px text-12px text-gray-400 whitespace-nowrap">小值靠前</span>
            </NFormItem>
          </NForm>

          <!-- 高級設置（第 5 步，內滾容器內） -->
          <NForm
            v-else-if="step === 5 && formData.product_property !== 'physical'"
            :model="formData"
            :disabled="editorLocked"
            label-placement="left"
            label-width="100"
          >
            <NFormItem label="發貨模式">
              <NSelect v-model:value="formData.delivery_mode" :options="deliveryModeOptions" />
            </NFormItem>
            <NFormItem label="導入去重">
              <NSwitch v-model:value="formData.dedup" />
              <span class="ml-8px text-12px text-gray-400">卡密導入時按內容去重</span>
            </NFormItem>
          </NForm>
        </NScrollbar>

        <!-- 第 3 步：商品描述（完全展開——封面/圖集/編輯器整高可見，不內滾；彈窗整體上移） -->
        <div v-if="step === 3" class="px-12px">
          <NAlert
            v-if="contentProtection.cover || contentProtection.description"
            type="info"
            class="mb-12px"
          >
            本地內容保護：{{
              [
                contentProtection.cover ? "封面" : "",
                contentProtection.description ? "產品介紹" : "",
              ]
                .filter(Boolean)
                .join("、")
            }}。 可在「批量修改內容」中選擇該商品並恢復跟隨上游。
          </NAlert>
          <NForm :model="formData" :disabled="editorLocked" label-placement="top">
            <div class="flex flex-col gap-16px sm:flex-row sm:gap-24px">
              <NFormItem label="封面圖" class="w-full sm:w-240px">
                <MediaField
                  :disabled="editorLocked"
                  v-model:value="formData.cover"
                  tip="建議 1:1，列表縮略圖"
                />
              </NFormItem>
              <NFormItem label="詳情圖集" class="min-w-0 flex-1">
                <MediaField
                  :disabled="editorLocked"
                  v-model:value="formData.images"
                  multiple
                  tip="詳情頁輪播，可多選"
                />
              </NFormItem>
            </div>
            <NFormItem label="商品描述">
              <div class="w-full">
                <div v-if="editorLocked" class="break-words" v-html="formData.description" />
                <RichEditor
                  v-else
                  v-model="formData.description"
                  height="420px"
                  placeholder="商品詳情（所見即所得；插圖走素材庫）"
                />
                <div class="mt-4px text-12px text-gray-400">
                  輸出 HTML 入庫前經服務端白名單 sanitize（防 XSS）；圖片統一素材庫可複用。
                </div>
              </div>
            </NFormItem>
          </NForm>
        </div>

        <NAlert v-if="step===4 && formData.product_kind==='sms_channel'" type="info">此商品的國家、服務和價格選項由供貨端按頁提供，無需創建 SKU 或下單控件。</NAlert>
        <!-- 第 4 步：規格與控件（面板內嵌——編輯態直接生效；創建態先保存商品） -->
        <div v-if="showCreate && !isDynamicProduct" v-show="step === 4" class="px-12px">
          <template v-if="editingId">
            <NAlert type="info" class="mb-12px"
              >規格的「保存/全部保存/刪除」及控件的「創建/更新/刪除」會立即生效，關閉商品窗口不會撤銷。其餘未保存輸入可通過取消放棄。</NAlert
            >
            <NCard size="small" title="SKU 多規格" class="mb-12px">
              <NAlert v-if="inventoryModePending" type="info" class="mb-12px">
                請先保存商品的庫存管理設置，再重新打開規格調整庫存。
              </NAlert>
              <SkuPanel
                ref="skuPanel"
                :key="editingId"
                :product-id="editingId"
                :physical="formData.product_property === 'physical'"
                :inventory-tracked="savedInventoryTracked"
                :base-physical-stock="Number(editingProduct?.physical_stock || 0)"
                :readonly="editorLocked"
                @persisted="independentlySaved = true"
              />
            </NCard>
            <NCard size="small" title="下單填寫信息">
              <ControlPanel
                ref="controlPanel"
                :product-id="editingId"
                :physical="formData.product_property === 'physical'"
                :readonly="editorLocked"
                @persisted="independentlySaved = true"
              />
            </NCard>
          </template>
          <NCard v-else size="small" class="py-40px">
            <NEmpty :description="copySourceId ? '保存副本後可查看和修改複製的配置' : 'SKU 規格與下單控件掛在已保存的商品上'">
              <template #extra>
                <NButton type="primary" :loading="saving" @click="saveAndContinue">
                  保存商品並配置
                </NButton>
              </template>
            </NEmpty>
          </NCard>
        </div>
      </div>
      <template #footer>
        <div class="flex flex-col gap-8px sm:flex-row sm:items-center sm:justify-between">
          <span class="shrink-0 text-12px text-gray-400">第 {{ step }} / {{ stepCount }} 步</span>
          <NSpace justify="end">
            <NButton :disabled="editorBusy" @click="requestClose">取消</NButton>
            <NButton v-if="step > 1" :disabled="editorBusy" @click="stepPrev">上一步</NButton>
            <NButton v-if="step < stepCount" :disabled="editorBusy" :type="editingId && step === 3 ? 'default' : 'primary'" @click="stepNext"
              >下一步</NButton
            >
            <NButton
              v-if="!editorLocked && (step === stepCount || (editingId && step === 3))"
              type="primary"
              :disabled="editorBusy"
              :loading="saving"
              @click="handleSave"
            >
              {{ editingId ? "保存" : "創建" }}
            </NButton>
          </NSpace>
        </div>
      </template>
    </NModal>

    <!-- 分類管理（新建後自動選中當前表單分類） -->
    <CategoryModal
      v-model:show="showCategory"
      @refresh="loadCategories"
      @created="(id: number) => (formData.category_id = id)"
    />
    <!-- 評價管理抽屜 -->
    <ReviewsDrawer v-model:show="showReviews" :product="reviewProduct" />
  </div>
</template>

<style>
.product-category-card {
  width: clamp(280px, 24vw, 360px);
  min-height: 0;
  align-self: flex-start;
  /* 使用後台實際的頂欄、標籤欄和頁腳尺寸，避免小高度窗口中被頁腳遮擋。 */
  max-height: calc(
    100dvh - var(--soy-header-height, 56px) - var(--soy-tab-height, 48px) -
      var(--soy-footer-height, 72px) - 32px
  );
}

.product-category-scroll {
  flex: 1;
  min-height: 0;
  overflow: auto;
  scrollbar-width: thin;
  scrollbar-gutter: stable;
  padding-bottom: 4px;
}

@media (max-width: 767px) {
  .product-management {
    flex-direction: column;
    overflow: auto;
  }
  .product-category-card {
    width: 100%;
    max-height: 200px;
  }
  .product-list-card {
    flex: none;
    min-height: 600px;
  }
  .product-list-card .n-data-table {
    min-height: 260px;
  }
}

/* 商品描述步（第 3 步）：編輯器整高展開後彈窗整體上移，保證底部按鈕可見 */
.product-editor-step-modal {
  transform: translateY(-4vh);
}

/* 分類樹：圖標前綴對齊 + 長名單行省略（懸停 title 看全名），多分類不眼花 */
.cat-node .n-tree-node-content__prefix {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}
.cat-node .n-tree-node-content {
  min-width: 0;
}
.cat-node.n-tree-node {
  height: auto;
}
.cat-prefix {
  font-size: 13px;
  line-height: 1;
}
.cat-node .n-tree-node-content__text {
  display: block;
  min-width: 0;
  max-width: none;
  white-space: normal;
  overflow-wrap: anywhere;
  line-height: 1.6;
  padding: 4px 0;
}
</style>
