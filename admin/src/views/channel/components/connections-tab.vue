<script setup lang="ts">
// 貨源渠道管理（supply:read / supply:write 超管專屬）：
// 連接 CRUD（三類驅動憑據動態表單）/ 測試連接 / 手動同步（採集全量|增量 / 僅價格 /
// 僅狀態）/ 定時計劃對話框（三 scope 間隔+時間窗+請求節奏）/ 限流狀態徽標與倒計時 /
// 同步任務抽屜（進度統計與取消）。
import { computed, defineAsyncComponent, h, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import {
  NButton, NDataTable, NDropdown, NInput, NInputNumber, NModal, NForm, NFormItem,
  NSelect, NSpace, NSwitch, NTag, NDrawer, NAlert,
} from "naive-ui";
import type { DataTableColumns, DropdownOption } from "naive-ui";
import {
  fetchSupplyConnections, createSupplyConnection, updateSupplyConnection, deleteSupplyConnection,
  pingSupplyConnection, createSupplySyncTask, fetchSupplySyncTasks, cancelSupplySyncTask,
} from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, formatMoney, yuanToFen } from "@/utils/money";
import FilterTabs from "@/components/common/filter-tabs.vue";
import TablePager from "@/components/common/table-pager.vue";
const ImportTaskProgress = defineAsyncComponent(() => import("./import-task-progress.vue"));
const ImportModal = defineAsyncComponent(() => import("./import-modal.vue"));
import { useResponsiveTier, type TableTier } from "./use-responsive-tier";

defineOptions({ name: "SupplyConnectionsTab" });

const loading = ref(false);
const connections = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const statusFilter = ref<"" | "active" | "disabled">("");

const statusTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "已啓用", value: "active", type: "success" as const },
  { label: "已禁用", value: "disabled", type: "error" as const },
];

const driverMeta: Record<string, { label: string; tag: "success" | "info" | "warning" }> = {
  zcard: { label: "ZCard", tag: "success" },
  dujiao_next: { label: "獨角數卡", tag: "info" },
  acg_faka: { label: "異次元", tag: "warning" },
};

const canWrite = () => checkAuth("supply:write");

// ── 容器寬分檔（full ≥1080 / mid ≥720 / compact）：任意屏寬下操作列完整可見，不依賴橫向滾動 ──
const wrapRef = ref<HTMLElement | null>(null);
const { tier } = useResponsiveTier(wrapRef);

function fmtTime(ts?: number) {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

// ── 限流狀態（AIMD 節奏器）──
const nowTick = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  timer = setInterval(() => (nowTick.value = Date.now()), 1000);
});
onUnmounted(() => timer && clearInterval(timer));

function rateLimitView(row: any): { label: string; type: "error" | "warning" | "success" } | null {
  if (!row.rate_limit_until) return null;
  const remain = row.rate_limit_until * 1000 - nowTick.value;
  if (remain <= 0) return { label: "冷卻已過（半開探測中）", type: "warning" };
  const min = Math.floor(remain / 60000);
  const sec = Math.floor((remain % 60000) / 1000);
  return { label: `限流熔斷 ${min}:${String(sec).padStart(2, "0")}`, type: "error" };
}

// scheduleSummary 定時計劃概要（列表 ⏰ 標記與 title）。
function scheduleSummary(row: any): { on: boolean; tip: string } {
  try {
    const sch = JSON.parse(row.settings || "{}").schedule;
    if (!sch?.enabled && !sch?.low_stock?.enabled) return { on: false, tip: "未啓用定時同步" };
    const parts: string[] = [];
    if (sch.low_stock?.enabled) parts.push(`庫存≤${sch.low_stock.threshold}：${sch.low_stock.interval}分鐘檢查`);
    if (sch.enabled && sch.collect?.enabled) parts.push(`採集 ${sch.collect.interval}分鐘`);
    if (sch.enabled && sch.price?.enabled) parts.push(`價格 ${sch.price.interval}分鐘`);
    if (sch.enabled && sch.status?.enabled) parts.push(`狀態 ${sch.status.interval}分鐘`);
    return { on: true, tip: parts.length ? `定時：${parts.join(" / ")}` : "定時總開關開（未啓用任何任務）" };
  } catch {
    return { on: false, tip: "" };
  }
}

function adaptiveDelayMs(row: any): number {
  try {
    const st = JSON.parse(row.rate_state || "{}");
    return Number(st.current_delay_ms) || 0;
  } catch {
    return 0;
  }
}

// ── 列表 ──
async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchSupplyConnections({ page: page.value, page_size: pageSize.value });
    if (!error && data) {
      const rows = (data as any).connections || [];
      connections.value =
        statusFilter.value === "" ? rows : rows.filter((r: any) => r.status === statusFilter.value);
      total.value = (data as any).total || rows.length;
    }
  } finally {
    loading.value = false;
  }
}

function onSearch() {
  page.value = 1;
  load();
}

// ── 新增/編輯（驅動憑據動態表單）──
const showForm = ref(false);
const saving = ref(false);
const editingId = ref(0);
const form = reactive({
  name: "",
  driver: "zcard",
  base_url: "",
  credentials: "", // JSON 明文（編輯留空 = 不改）
  exchange_rate: 1,
  price_markup_percent: 10,
  markupAmountYuan: 0,
  failAction: "auto_refund",
  productUrlTemplate: "",
  price_rounding_mode: "none",
  auto_sync_price: true,
  stock_mode: "real",
});

const credFields = computed(() => {
  switch (form.driver) {
    case "dujiao_next":
      return [
        { key: "api_key", label: "Api Key" },
        { key: "api_secret", label: "Api Secret" },
      ];
    case "acg_faka":
      return [
        { key: "app_id", label: "商戶ID（app_id）" },
        { key: "app_key", label: "對接密鑰（app_key）" },
      ];
    default:
      return [
        { key: "api_key", label: "Api Key" },
        { key: "api_secret", label: "Api Secret" },
      ];
  }
});

const credDraft = reactive<Record<string, string>>({});

function openCreate() {
  editingId.value = 0;
  form.name = "";
  form.driver = "zcard";
  form.base_url = "";
  form.credentials = "";
  form.exchange_rate = 1;
  form.price_markup_percent = 10;
  form.markupAmountYuan = 0;
  form.failAction = "auto_refund";
  form.productUrlTemplate = "";
  form.price_rounding_mode = "none";
  form.auto_sync_price = true;
  form.stock_mode = "real";
  Object.keys(credDraft).forEach((k) => delete credDraft[k]);
  showForm.value = true;
}

function openEdit(row: any) {
  editingId.value = row.id;
  form.name = row.name;
  form.driver = row.driver;
  form.base_url = row.base_url;
  form.credentials = "";
  form.exchange_rate = row.exchange_rate || 1;
  form.price_markup_percent = row.price_markup_percent || 0;
  form.markupAmountYuan = (row.price_markup_amount || 0) / 100;
  try {
    const st = JSON.parse(row.settings || "{}");
    form.failAction = st.failure_action || "auto_refund";
    form.productUrlTemplate = st.product_url_template || "";
  } catch {
    form.failAction = "auto_refund";
  }
  form.price_rounding_mode = row.price_rounding_mode || "none";
  form.auto_sync_price = !!row.auto_sync_price;
  form.stock_mode = row.stock_mode || "real";
  Object.keys(credDraft).forEach((k) => delete credDraft[k]);
  showForm.value = true;
}

async function submitForm() {
  if (saving.value) return;
  if (!form.name || !form.base_url) {
    window.$message?.warning("名稱與上游地址必填");
    return;
  }
  let credentials = form.credentials;
  if (editingId.value === 0 || Object.values(credDraft).some((v) => v !== "")) {
    credentials = JSON.stringify(credDraft);
  }
  saving.value = true;
  try {
    const payload: Record<string, unknown> = {
      name: form.name,
      driver: form.driver,
      base_url: form.base_url,
      exchange_rate: form.exchange_rate,
      price_markup_percent: form.price_markup_percent,
      price_markup_amount: yuanToFen(form.markupAmountYuan || 0),
      price_rounding_mode: form.price_rounding_mode,
      auto_sync_price: form.auto_sync_price,
      stock_mode: form.stock_mode,
    };
    if (credentials) payload.credentials = credentials;
    // settings：編輯時保留原值（定時計劃單獨維護），疊加失敗策略
    let baseSettings: Record<string, any> = {};
    if (editingId.value) {
      const row = connections.value.find((r) => r.id === editingId.value);
      try {
        baseSettings = JSON.parse(row?.settings || "{}");
      } catch {
        /* 空 */
      }
    }
    if (form.failAction !== "auto_refund") baseSettings.failure_action = form.failAction;
    else delete baseSettings.failure_action;
    if (form.productUrlTemplate.trim()) baseSettings.product_url_template = form.productUrlTemplate.trim();
    else delete baseSettings.product_url_template;
    payload.settings = JSON.stringify(baseSettings);
    if (editingId.value) {
      const { error } = await updateSupplyConnection(editingId.value, payload);
      if (error) return;
      window.$message?.success("連接已更新，已有商品將在執行價格同步時按規則處理");
    } else {
      const { error } = await createSupplyConnection(payload as any);
      if (error) return;
      window.$message?.success("連接已創建");
    }
    showForm.value = false;
    load();
  } finally {
    saving.value = false;
  }
}

// ── 測試連接 ──
const pinging = reactive<Record<number, boolean>>({});
async function handlePing(row: any) {
  pinging[row.id] = true;
  try {
    const { data, error } = await pingSupplyConnection(row.id);
    if (!error && data) {
      const d = data as any;
      if (d.ok === true) {
        const balance = d.balance_cents ?? d.balance ?? (d.currency ? 0 : -1);
        window.$message?.success(`連接成功：${d.site_name || "上游"}，餘額 ${balance == null || balance < 0 ? "未知" : formatMoney(balance)}`);
      } else {
        window.$message?.error(`連接失敗：${d.error || "上游未確認連接成功，請重試"}`);
      }
      await load();
    }
  } finally {
    pinging[row.id] = false;
  }
}

// ── 同步任務 ──
function handleSync(row: any, scope: string, mode: string) {
  createSupplySyncTask({ connection_id: row.id, scope, mode }).then(({ error }) => {
    if (!error) {
      window.$message?.success("同步任務已創建");
      openTasks(row.id);
      load();
    }
  });
}

const showTasks = ref(false);
const tasksConn = ref(0);
const selectedImportTask = ref(0);
let taskPoll: ReturnType<typeof setInterval> | undefined;
watch(showTasks, show => {
  clearInterval(taskPoll);
  if (show) taskPoll = setInterval(() => { if (!document.hidden) loadTasks(true); }, 5000);
});
onUnmounted(() => clearInterval(taskPoll));
const tasks = ref<any[]>([]);
const tasksLoading = ref(false);
function openTasks(connectionId: number) {
  selectedImportTask.value = 0;
  tasksConn.value = connectionId;
  showTasks.value = true;
  loadTasks();
}
async function loadTasks(quiet = false) {
  if (tasksLoading.value) return;
  const connectionId = tasksConn.value;
  tasksLoading.value = true;
  try {
    const { data, error } = await fetchSupplySyncTasks({ connection_id: tasksConn.value || undefined, page: 1, page_size: 20 }, quiet);
    if (!error && data && connectionId === tasksConn.value) tasks.value = (data as any).tasks || [];
  } finally {
    tasksLoading.value = false;
  }
}
function handleCancelTask(id: number) {
  cancelSupplySyncTask(id).then(() => loadTasks());
}

// 手動重跑（failed/canceled/done 均可按原參數重建任務）
async function handleRerunTask(row: any) {
  if (row.scope === "import") { selectedImportTask.value = Number(row.id); return; }
  const { error } = await createSupplySyncTask({
    connection_id: row.connection_id,
    scope: row.error_code === "STOCK_QUERY_FAILED" ? "stock" : row.scope || "collect",
    mode: row.error_code === "STOCK_QUERY_FAILED" ? "failed" : row.mode || "full",
  });
  if (!error) {
    window.$message?.success("已重新創建任務");
    loadTasks();
  }
}

const taskColumns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 48 },
  { title: "範圍", key: "scope", width: 56, render: (r) => ({ collect: "採集", price: "價格", status: "狀態", stock: "庫存", listing: "補貨檢查", import: "導入" } as any)[r.scope || "collect"] || r.scope },
  { title: "模式", key: "mode", width: 64, ellipsis: { tooltip: true }, render: r => r.mode === "selected" ? "所選商品" : r.mode },
  {
    title: "狀態",
    key: "status",
    width: 84,
    render: (r) =>
      h(
        NTag,
        { size: "small", type: r.status === "done" ? "success" : r.status === "failed" ? "error" : r.status === "processing" ? "info" : r.status === "canceled" ? "default" : "warning", bordered: false },
        { default: () => r.scope === "import" && r.error_code ? (r.status === "done" ? "部分需處理" : "需處理") : ({ done: "完成", failed: "失敗", processing: "執行中", canceled: "已取消", pending: "排隊中" } as any)[r.status] || r.status },
      ),
  },
  {
    title: "進度",
    key: "processed",
    width: 140,
    render: (r) =>
      h("div", { class: "flex flex-col" }, [
        h("span", `${r.processed || 0}/${r.total || 0} 件`),
        r.current_stage
          ? h("span", { class: "text-11px text-gray-400" }, `${stageText(r.current_stage)}${r.page ? ` · 第 ${r.page} 頁` : ""}`)
          : null,
      ]),
  },
  {
    title: "統計",
    key: "stats",
    width: 200,
    render: (r) => r.scope === "import" ? `已保存 ${Number(r.created || 0) + Number(r.updated || 0)} · 跳過 ${r.manual_skipped || 0} · 失敗 ${r.failed_count || 0} · 庫存待確認 ${r.stock_pending_count || 0}` :
      h(
        "div",
        {
          class: "text-12px",
          title: `新增 ${r.created || 0} · 更新 ${r.updated || 0} · 價格變更 ${r.price_updated || 0} · 保護跳過（人工改價或商品鎖定）${r.manual_skipped || 0} · 隱藏 ${r.hidden || 0} · 對賬下架 ${r.deleted || 0}`,
        },
        [
          h("span", { class: "text-success" }, `新${r.created || 0}`),
          " ",
          h("span", { class: "text-info" }, `更${r.updated || 0}`),
          " ",
          h("span", { class: "text-primary" }, `價${r.price_updated || 0}`),
          " ",
          h("span", { class: "text-warning" }, `護${r.manual_skipped || 0}`),
          " ",
          h("span", { class: "text-gray-400" }, `藏${r.hidden || 0}`),
          " ",
          h("span", { class: "text-error" }, `刪${r.deleted || 0}`),
        ],
      ),
  },
  {
    title: "上游調用",
    key: "error_code",
    minWidth: 150,
    ellipsis: { tooltip: true },
    render: (r) =>
      r.error_code
        ? h("span", { title: r.error_context || "", class: "text-error" }, r.scope === "import" ? r.error_context : r.error_code === "STOCK_QUERY_FAILED" ? r.error_context || "庫存查詢失敗" : `${r.error_code}`)
        : h("span", { class: "text-gray-400" }, r.status === "done" ? "正常" : "-"),
  },
  {
    title: "操作",
    key: "actions",
    width: 96,
    render: (r) =>
      h("div", { class: "flex gap-4px" }, [
        r.scope === "import" ? h(NButton, { size: "tiny", onClick: () => selectedImportTask.value = Number(r.id) }, { default: () => "查看進度" }) : null,
        r.scope !== "import" && (r.status === "processing" || r.status === "pending") && canWrite()
          ? h(NButton, { size: "tiny", quaternary: true, onClick: () => handleCancelTask(r.id) }, { default: () => "取消" })
          : null,
        r.scope !== "import" && ["failed", "canceled", "done"].includes(r.status) && canWrite()
          ? h(NButton, { size: "tiny", type: "primary", quaternary: true, onClick: () => handleRerunTask(r) }, { default: () => r.error_code === "STOCK_QUERY_FAILED" ? "重試庫存" : "重跑" })
          : null,
      ]),
  },
];

function stageText(stage: string) {
  return (
    ({
      fetching_products: "拉取商品",
      fetching_stock: "補查庫存",
      saving_products: "寫入本地",
      reconciling: "刪除對賬",
      finalizing: "收尾",
    } as Record<string, string>)[stage] || stage
  );
}

function syncActions(row: any): DropdownOption[] {
  return [
    { label: "採集（增量）", key: "collect:incremental", disabled: !canWrite() },
    { label: "採集（全量 + 刪除對賬）", key: "collect:full", disabled: !canWrite() },
    { label: "僅補查庫存", key: "stock:full", disabled: !canWrite() },
    { label: "僅重試失敗庫存", key: "stock:failed", disabled: !canWrite() },
    { label: "僅同步價格", key: "price:incremental", disabled: !canWrite() },
    { label: "僅同步上下架/庫存", key: "status:incremental", disabled: !canWrite() },
  ];
}

// ── 交互式導入（ D）──
const showImport = ref(false);
const importConn = ref<any>(null);
function openImport(row: any) {
  importConn.value = row;
  showImport.value = true;
}

// ── 定時計劃對話框（settings.schedule；S2/S3 參數）──
const showSchedule = ref(false);
const scheduleConn = ref<any>(null);
const schedule = reactive({
  enabled: false,
  request_delay: 1,
  stock_concurrency: 3,
  stock_request_delay_ms: 200,
  low_stock: { enabled: false, threshold: 10, interval: 5 },
  collect: { enabled: false, interval: 360, mode: "incremental", windows: "" },
  price: { enabled: false, interval: 30, windows: "" },
  status: { enabled: false, interval: 60, windows: "" },
});

// scheduleStatus 執行狀態面板數據（上次執行 + 下次預計）。
const scheduleStatus = computed(() => {
  const conn = scheduleConn.value;
  if (!conn) return [];
  const fmt = (ts?: number) => (ts ? new Date(ts * 1000).toLocaleString() : "從未執行");
  const next = (ts: number | undefined, intervalMin: number) => {
    if (!ts) return "即將（首輪）";
    const nextAt = new Date((ts + intervalMin * 60) * 1000);
    return nextAt <= new Date() ? "到期（下輪掃描派發）" : nextAt.toLocaleString();
  };
  return [
    {
      label: "採集商品",
      on: schedule.collect.enabled,
      last: fmt(conn.last_collect_at || conn.last_synced_at),
      next: next(conn.last_collect_at || conn.last_synced_at, schedule.collect.interval),
    },
    {
      label: "同步價格",
      on: schedule.price.enabled,
      last: fmt(conn.last_price_sync_at),
      next: next(conn.last_price_sync_at, schedule.price.interval),
    },
    {
      label: "上下架/庫存",
      on: schedule.status.enabled,
      last: fmt(conn.last_status_sync_at),
      next: next(conn.last_status_sync_at, schedule.status.interval),
    },
  ];
});

function openSchedule(row: any) {
  scheduleConn.value = row;
  const s = JSON.parse(row.settings || "{}").schedule || {};
  schedule.enabled = !!s.enabled;
  schedule.request_delay = Number(s.request_delay ?? 1);
  schedule.stock_concurrency = Number(s.stock_concurrency ?? 3);
  schedule.low_stock.enabled = s.low_stock?.enabled === true;
  schedule.low_stock.threshold = Number(s.low_stock?.threshold ?? 10);
  schedule.low_stock.interval = Number(s.low_stock?.interval ?? 5);
  schedule.stock_request_delay_ms = Number(s.stock_request_delay_ms ?? 200);
  const fill = (key: "collect" | "price" | "status") => {
    const src = s[key] || {};
    schedule[key].enabled = !!src.enabled;
    schedule[key].interval = Number(src.interval ?? { collect: 360, price: 30, status: 60 }[key]);
    if (key === "collect") schedule[key].mode = src.mode || "incremental";
    schedule[key].windows = Array.isArray(src.windows)
      ? src.windows.map((w: any) => `${w.start}-${w.end}`).join(", ")
      : "";
  };
  fill("collect");
  fill("price")
  fill("status");
  showSchedule.value = true;
}

const scheduleSaving = ref(false);
const lowStockError = computed(() => !Number.isInteger(schedule.low_stock.threshold) || schedule.low_stock.threshold < 1 || schedule.low_stock.threshold > 1000000
  ? "庫存數量須為 1–1000000 的整數" : !Number.isInteger(schedule.low_stock.interval) || schedule.low_stock.interval < 5 || schedule.low_stock.interval > 1440
  ? "檢查間隔須為 5–1440 分鐘的整數" : "");
async function submitSchedule() {
  if (lowStockError.value || scheduleSaving.value) return;
  scheduleSaving.value = true;
  try {
  const parseWindows = (s: string) =>
    s.split(/[,，]/)
      .map((seg) => seg.trim())
      .filter(Boolean)
      .map((seg) => {
        const [start, end] = seg.split("-").map((x) => x.trim());
        return { start: start || "00:00", end: end || "23:59" };
      });
  const settings = JSON.parse(scheduleConn.value.settings || "{}");
  settings.schedule = {
    enabled: schedule.enabled,
    request_delay: schedule.request_delay,
    stock_concurrency: schedule.stock_concurrency,
    low_stock: { ...schedule.low_stock },
    stock_request_delay_ms: schedule.stock_request_delay_ms,
    collect: {
      enabled: schedule.collect.enabled,
      interval: schedule.collect.interval,
      mode: schedule.collect.mode,
      windows: parseWindows(schedule.collect.windows),
    },
    price: { enabled: schedule.price.enabled, interval: schedule.price.interval, windows: parseWindows(schedule.price.windows) },
    status: { enabled: schedule.status.enabled, interval: schedule.status.interval, windows: parseWindows(schedule.status.windows) },
  };
  const { error } = await updateSupplyConnection(scheduleConn.value.id, { settings: JSON.stringify(settings) });
  if (!error) {
    window.$message?.success("定時計劃已保存");
    showSchedule.value = false;
    load();
  }
  } finally { scheduleSaving.value = false; }
}

// ── 響應式列集：compact 只留 名稱/限流/操作；mid 去掉 ID/餘額/最近採集；full 全列 ──
// 文本列只設 minWidth 不設 width（即彈性列，富餘寬度自動分攤），maxWidth 封頂防寬屏上游地址無限膨脹留白。
function nameCol(t: TableTier) {
  return {
    title: "名稱",
    key: "name",
    minWidth: t === "compact" ? 96 : 120,
    maxWidth: t === "compact" ? 220 : 300,
    render: (row: any) => {
      const sch = scheduleSummary(row);
      // compact 下被裁掉的列信息併入懸停提示
      const tip =
        t === "compact"
          ? `${row.name}${sch.on ? ` · ${sch.tip}` : ""}\n${driverMeta[row.driver]?.label || row.driver} · ${row.base_url}`
          : sch.tip;
      return h("div", { class: "flex min-w-0 items-center gap-4px" }, [
        h("span", { class: "truncate", title: tip }, row.name),
        row.last_ping_at > 0 && !row.last_ping_ok
          ? h(NTag, { size: "small", type: "error", bordered: false, title: `最近測試：${fmtTime(row.last_ping_at)}\n${row.last_error || "連接失敗"}` }, { default: () => "連接失敗" })
          : null,
        sch.on ? h("span", { title: sch.tip, class: "cursor-help shrink-0" }, "⏰") : null,
      ]);
    },
  };
}

function typeCol() {
  return {
    title: "類型",
    key: "driver",
    width: 84,
    render: (row: any) => h(NTag, { size: "small", type: driverMeta[row.driver]?.tag || "default", bordered: false }, { default: () => driverMeta[row.driver]?.label || row.driver }),
  };
}

function rateCol(t: TableTier) {
  return {
    title: "限流狀態",
    key: "rate",
    align: "center" as const,
    width: t === "compact" ? 100 : t === "mid" ? 150 : 158,
    render: (row: any) => {
      const rl = rateLimitView(row);
      const delay = adaptiveDelayMs(row);
      if (t === "compact") {
        const label = !rl ? "正常" : rl.type === "warning" ? "半開探測" : rl.label.replace("限流熔斷", "熔斷");
        return h(NTag, { size: "tiny", type: rl?.type || "success", bordered: false }, { default: () => label });
      }
      // 居中棧：狀態徽章 + 自適應間隔説明（大廠表格徽章列慣例）
      return h("div", { class: "flex flex-col items-center justify-center gap-2px" }, [
        rl
          ? h(NTag, { size: "tiny", type: rl.type, bordered: false }, { default: () => rl.label })
          : h(NTag, { size: "tiny", type: "success", bordered: false }, { default: () => "正常" }),
        delay > 0
          ? h("span", { class: "text-11px whitespace-nowrap text-gray-400" }, `自適應間隔 ${(delay / 1000).toFixed(0)}s`)
          : null,
      ]);
    },
  };
}

// 操作列：full/mid 為「測試 + 同步▾ + 任務 + 更多▾」；compact 收進單個「操作▾」（功能不減）
function moreMenu(): DropdownOption[] {
  const opts: DropdownOption[] = [{ label: "⏰ 定時計劃", key: "schedule" }];
  if (canWrite())
    opts.push(
      { label: "導入商品", key: "import" },
      { label: "編輯", key: "edit" },
      { type: "divider", key: "dv" },
      { label: "刪除", key: "delete" },
    );
  return opts;
}

function compactMenu(row: any): DropdownOption[] {
  const opts: DropdownOption[] = [{ label: "測試連接", key: "ping" }];
  if (canWrite()) opts.push({ label: "同步", key: "sync", children: syncActions(row) });
  opts.push({ label: "同步任務", key: "tasks" }, { label: "⏰ 定時計劃", key: "schedule" });
  if (canWrite())
    opts.push(
      { label: "導入商品", key: "import" },
      { label: "編輯", key: "edit" },
      { type: "divider", key: "dv" },
      { label: "刪除", key: "delete" },
    );
  return opts;
}

// 統一分發：形如 collect:incremental 的是同步子項，其餘按 key 走 switch
function onRowAction(row: any, key: string) {
  if (key.includes(":")) {
    const [scope, mode] = key.split(":");
    handleSync(row, scope, mode);
    return;
  }
  switch (key) {
    case "ping":
      handlePing(row);
      break;
    case "tasks":
      openTasks(row.id);
      break;
    case "schedule":
      openSchedule(row);
      break;
    case "import":
      openImport(row);
      break;
    case "edit":
      openEdit(row);
      break;
    case "delete":
      window.$dialog?.warning({
        title: "刪除渠道",
        content: "存在商品映射時不可刪除，確定刪除？",
        positiveText: "刪除",
        negativeText: "取消",
        onPositiveClick: () => handleDelete(row.id),
      });
      break;
  }
}

function actionsCol(t: TableTier) {
  return {
    title: "操作",
    key: "actions",
    width: t === "compact" ? 96 : 270,
    render: (row: any) =>
      t === "compact"
        ? h(
            NDropdown,
            { options: compactMenu(row), trigger: "click", onSelect: (key: string) => onRowAction(row, key) },
            { default: () => h(NButton, { size: "tiny", secondary: true }, { default: () => "操作 ▾" }) },
          )
        : h("div", { class: "flex flex-wrap items-center gap-4px" }, [
            h(NButton, { size: "tiny", loading: pinging[row.id], onClick: () => handlePing(row) }, { default: () => "測試" }),
            canWrite() ? h(NButton, { size: "tiny", type: "primary", secondary: true, onClick: () => onRowAction(row, "import") }, { default: () => "導入商品" }) : null,
            canWrite()
              ? h(
                  NDropdown,
                  { options: syncActions(row), trigger: "click", onSelect: (key: string) => onRowAction(row, key) },
                  { default: () => h(NButton, { size: "tiny", type: "primary", quaternary: true }, { default: () => "同步 ▾" }) },
                )
              : null,
            h(NButton, { size: "tiny", quaternary: true, onClick: () => openTasks(row.id) }, { default: () => "任務" }),
            h(
              NDropdown,
              { options: moreMenu(), trigger: "click", onSelect: (key: string) => onRowAction(row, key) },
              { default: () => h(NButton, { size: "tiny", quaternary: true }, { default: () => "更多 ▾" }) },
            ),
          ]),
  };
}

const columns = computed<DataTableColumns<any>>(() => {
  const t = tier.value;
  const cols: DataTableColumns<any> = [];
  if (t !== "compact") cols.push({ title: "ID", key: "id", width: 48 });
  cols.push(nameCol(t));
  if (t !== "compact") {
    cols.push(typeCol());
    cols.push({
      title: "上游地址",
      key: "base_url",
      minWidth: t === "mid" ? 130 : 150,
      maxWidth: 420,
      ellipsis: { tooltip: true },
    });
  }
  if (t === "full") {
    cols.push({
      title: "餘額", key: "balance_cache", width: 110,
      render: (row: any) => {
        // JSON omits a known zero balance; an unknown balance is explicitly -1.
        const balance = row.balance_cache ?? (row.last_ping_ok ? 0 : -1);
        if (balance < 0) return "—（未取到）";
        const historical = !row.last_ping_ok;
        return h("div", {
          title: historical
            ? "上次成功獲取的餘額，當前未驗證；最新餘額以連接成功後的結果為準"
            : `最近測試：${fmtTime(row.last_ping_at)}`,
        }, [
          h("div", formatMoney(balance)),
          historical ? h("small", { class: "text-amber-600" }, "歷史餘額") : null,
        ]);
      },
    });
    cols.push({ title: "最近採集", key: "last_collect_at", width: 146, render: (row: any) => fmtTime(row.last_collect_at || row.last_synced_at) });
  }
  cols.push(rateCol(t));
  cols.push(actionsCol(t));
  return cols;
});

async function handleDelete(id: number) {
  const { error } = await deleteSupplyConnection(id);
  if (!error) {
    window.$message?.success("已刪除");
    load();
  }
}

onMounted(load);
</script>

<template>
  <div ref="wrapRef">
    <div class="mb-12px flex flex-wrap items-center justify-between gap-8px">
      <FilterTabs v-model:value="statusFilter" :options="statusTabs" @change="onSearch" />
      <NButton v-if="canWrite()" size="small" type="primary" @click="openCreate">新增渠道</NButton>
    </div>

    <NDataTable
      :columns="columns"
      :data="connections"
      :loading="loading"
      size="small"
      :row-key="(r: any) => r.id"
      :max-height="540"
      :scroll-x="300"
    />
    <div class="mt-12px flex justify-end">
      <TablePager v-model:page="page" v-model:page-size="pageSize" :total="total" @change="load" />
    </div>

    <!-- 新增/編輯 -->
    <NModal v-model:show="showForm" :closable="!saving" :mask-closable="!saving" :close-on-esc="!saving" preset="card" :title="editingId ? '編輯渠道' : '新增渠道'" style="width: 560px; max-width: 96vw">
      <NForm label-placement="left" label-width="110">
        <NFormItem label="名稱" required>
          <NInput v-model:value="form.name" placeholder="如：主站獨角" />
        </NFormItem>
        <NFormItem label="渠道類型" required>
          <NSelect
            v-model:value="form.driver"
            :disabled="!!editingId"
            :options="[
              { label: 'ZCard（自有協議）', value: 'zcard' },
              { label: '獨角數卡 dujiao-next', value: 'dujiao_next' },
              { label: '異次元 acg-faka', value: 'acg_faka' },
            ]"
          />
        </NFormItem>
        <NFormItem label="上游地址" required>
          <NInput v-model:value="form.base_url" placeholder="https://up.example.com" />
        </NFormItem>
        <NFormItem v-for="f in credFields" :key="f.key" :label="f.label" :required="editingId === 0">
          <NInput v-model:value="credDraft[f.key]" :placeholder="editingId ? '留空 = 不修改' : ''" />
        </NFormItem>
        <NFormItem label="匯率">
          <NInputNumber v-model:value="form.exchange_rate" :min="0.00000001" class="w-full" />
        </NFormItem>
        <NFormItem label="加價規則">
          <div class="w-full">
            <div class="flex flex-wrap items-center gap-8px">
              <span class="w-64px shrink-0 text-13px">比例上浮</span>
              <NInputNumber v-model:value="form.price_markup_percent" :min="0" size="small" class="w-110px" placeholder="0">
                <template #suffix>%</template>
              </NInputNumber>
              <span class="text-12px text-gray-400">＋</span>
              <span class="w-64px shrink-0 text-13px">固定加價</span>
              <NInputNumber v-model:value="form.markupAmountYuan" :min="0" :precision="2" size="small" class="w-120px" placeholder="0.00">
                <template #suffix>{{ currencyUnit() }}</template>
              </NInputNumber>
            </div>
            <div class="mt-4px text-11px text-gray-400">
              本店售價 = 上游價 × 匯率 ×（1 + 比例上浮%）＋ 固定加價；0 表示不加價。用於跟隨渠道的導入及後續價格同步，保存設置不會立即重算已有商品。
            </div>
          </div>
        </NFormItem>
        <NFormItem label="商品鏈接模板">
          <NInput v-model:value="form.productUrlTemplate" placeholder="如 {base}/product/{code}（商品列表跳上游）" />
        </NFormItem>
        <NFormItem label="採購失敗策略">
          <NSelect
            v-model:value="form.failAction"
            :options="[
              { label: '自動退款（默認）', value: 'auto_refund' },
              { label: '轉人工處理', value: 'manual' },
            ]"
          />
        </NFormItem>
        <NFormItem label="取整模式">
          <NSelect
            v-model:value="form.price_rounding_mode"
            :options="[
              { label: '不取整（兩位小數）', value: 'none' },
              { label: `向上取整到整數 ${currencyUnit()}`, value: 'ceil_int' },
              { label: `向上取整到 0.1 ${currencyUnit()}`, value: 'ceil_tenth' },
            ]"
          />
        </NFormItem>
        <NFormItem label="同步自動改價">
          <div>
            <NSpace align="center">
              <NSwitch v-model:value="form.auto_sync_price" />
              <span class="text-12px text-gray-400">關閉後保留本地價；開啓後仍遵循固定覆蓋價和手動改價保護</span>
            </NSpace>
            <div class="text-12px opacity-70">按商品已保存的定價規則同步。歷史商品缺少規則時保留現價，可在導入商品中重新選擇策略；手工商品價和規格價會受到保護。</div>
          </div>
        </NFormItem>
      </NForm>
      <template #footer>
        <NButton size="small" class="mr-8px" :disabled="saving" @click="showForm = false">取消</NButton>
        <NButton size="small" type="primary" :loading="saving" @click="submitForm">
          {{ editingId ? "保存" : "創建" }}
        </NButton>
      </template>
    </NModal>

    <!-- 定時計劃 -->
    <NModal v-model:show="showSchedule" preset="card" title="定時同步計劃" style="width: 620px; max-width: 96vw" content-style="max-height: 70vh; overflow-y: auto">
      <NAlert type="info" :bordered="false" class="mb-12px">商品列表中開啓的自動上下架獨立運行；關閉本頁定時計劃不會暫停它，請在商品列表暫停自動管理。</NAlert>
      <NAlert v-if="scheduleConn" :type="scheduleConn.rate_limit_until > Date.now() / 1000 ? 'warning' : 'info'" :show-icon="false" class="mb-12px">
        <div>低庫存檢查：{{ scheduleConn.low_stock_scanned_at ? new Date(scheduleConn.low_stock_scanned_at * 1000).toLocaleString() : '尚未運行' }}</div>
        <div>{{ scheduleConn.low_stock_message || '保存開啓後在下一輪掃描開始檢查，每分鐘調度一次。' }}</div>
        <div v-if="scheduleConn.status !== 'active'">此貨源已停用，自動檢查暫停。</div>
      </NAlert>
      <!-- 執行狀態（錨點來自連接行數據；下次 = 上次 + 間隔，窗口內生效） -->
      <div v-if="scheduleConn" class="mb-12px rounded-6px bg-gray-50 p-10px dark:bg-gray-800">
        <div class="mb-6px text-13px font-500">自動任務執行狀態</div>
        <div class="flex flex-col gap-3px text-12px text-gray-600 dark:text-gray-300">
          <div v-for="s in scheduleStatus" :key="s.label" class="flex flex-wrap items-center gap-8px">
            <span class="w-70px shrink-0">{{ s.label }}</span>
            <NTag size="tiny" :type="s.on ? 'success' : 'default'" :bordered="false">{{ s.on ? "已啓用" : "未啓用" }}</NTag>
            <span>上次：{{ s.last }}</span>
            <span v-if="s.on" class="text-primary">下次：{{ s.next }}</span>
          </div>
        </div>
      </div>
      <NAlert type="info" :bordered="false" class="mb-12px">
        採集過快可能被上游封鎖 IP：遇 429/WAF 攔截會自動降速並熔斷冷卻（界面列表可見倒計時），
        請求間隔為自適應下限。三類任務各自獨立間隔，均在時間窗口內執行。保存後由系統每分鐘檢查到期自動派發。
      </NAlert>
      <NForm label-placement="left" label-width="130" :disabled="!canWrite() || scheduleSaving">
        <NFormItem label="低庫存加速檢查">
          <NSwitch v-model:value="schedule.low_stock.enabled" aria-label="低庫存加速檢查" />
        </NFormItem>
        <NFormItem label="庫存不超過" :validation-status="lowStockError ? 'error' : undefined" :feedback="lowStockError">
          <NInputNumber v-model:value="schedule.low_stock.threshold" :min="1" :max="1000000" :precision="0" :input-props="{ 'aria-label': '加速檢查庫存閾值' }" class="w-full" />
        </NFormItem>
        <NFormItem label="檢查間隔（分鐘）">
          <NInputNumber v-model:value="schedule.low_stock.interval" :min="5" :max="1440" :precision="0" :input-props="{ 'aria-label': '低庫存檢查間隔' }" class="w-full" />
        </NFormItem>
        <NAlert type="info" :show-icon="false" class="mb-16px">
          對本貨源已導入、仍由上游發貨的規格全天檢查，包含零庫存；與下方採集、調價開關獨立。查詢失敗會退避重試，上游限流會延後。新發現低庫存後進入加速，不能代替下單時的實時校驗。提醒閾值在「系統設置 → 貨源」單獨配置。
        </NAlert>
        <NFormItem label="啓用常規同步">
          <NSwitch v-model:value="schedule.enabled" />
        </NFormItem>
        <NFormItem label="分頁請求間隔(秒)">
          <NInputNumber v-model:value="schedule.request_delay" :min="0" :max="60" class="w-full" />
        </NFormItem>
        <NFormItem label="庫存補查併發">
          <NInputNumber v-model:value="schedule.stock_concurrency" :min="1" :max="10" class="w-full" />
        </NFormItem>
        <NFormItem label="庫存批次間隔(ms)">
          <NInputNumber v-model:value="schedule.stock_request_delay_ms" :min="0" :max="10000" class="w-full" />
        </NFormItem>
        <NFormItem label="採集商品">
          <NSpace align="center" class="w-full">
            <NSwitch v-model:value="schedule.collect.enabled" size="small" />
            <NInputNumber v-model:value="schedule.collect.interval" :min="5" size="small" class="w-100px" />
            <span class="text-12px">分鐘</span>
            <NSelect
              v-model:value="schedule.collect.mode"
              size="small"
              class="w-110px"
              :options="[
                { label: '增量', value: 'incremental' },
                { label: '全量', value: 'full' },
              ]"
            />
          </NSpace>
        </NFormItem>
        <NFormItem label="同步價格">
          <NSpace align="center">
            <NSwitch v-model:value="schedule.price.enabled" size="small" />
            <NInputNumber v-model:value="schedule.price.interval" :min="5" size="small" class="w-100px" />
            <span class="text-12px">分鐘</span>
          </NSpace>
        </NFormItem>
        <NFormItem label="同步上下架/庫存">
          <NSpace align="center">
            <NSwitch v-model:value="schedule.status.enabled" size="small" />
            <NInputNumber v-model:value="schedule.status.interval" :min="5" size="small" class="w-100px" />
            <span class="text-12px">分鐘</span>
          </NSpace>
        </NFormItem>
        <NFormItem label="執行時間窗">
          <NInput v-model:value="schedule.collect.windows" placeholder="如 01:00-05:00, 22:00-06:00（空=全天）" />
        </NFormItem>
      </NForm>
      <template #footer>
        <NButton size="small" class="mr-8px" @click="showSchedule = false">取消</NButton>
        <NButton size="small" type="primary" :loading="scheduleSaving" :disabled="!canWrite() || !!lowStockError" @click="submitSchedule">保存計劃</NButton>
      </template>
    </NModal>

    <!-- 交互式導入 -->
    <ImportModal v-if="showImport" v-model:show="showImport" :connection="importConn" @imported="load" @task-created="id => { openTasks(importConn.id); selectedImportTask = id; }" />

    <!-- 同步任務抽屜：桌面端加寬到 960 讓全列一屏可見；小屏回退 100%，窄屏內部橫向滾動 -->
    <NDrawer v-model:show="showTasks" width="min(960px, 100%)">
      <NDrawerContent :title="selectedImportTask ? '商品導入進度' : '同步任務'" closable>
        <template v-if="selectedImportTask">
          <NButton class="mb-16px" size="small" @click="selectedImportTask = 0">返回任務列表</NButton>
          <ImportTaskProgress :task-id="selectedImportTask" @changed="loadTasks" @configure="() => { const conn = connections.find((r: any) => Number(r.id) === Number(tasksConn)); if (conn) { showTasks = false; openEdit(conn); } }" />
        </template>
        <NDataTable v-else
          :max-height="540"
          size="small"
          :data="tasks"
          :loading="tasksLoading"
          :columns="taskColumns"
          :scroll-x="840"
        />
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
