<script setup lang="ts">
import { ref, computed, onMounted, onActivated, onDeactivated, onUnmounted, watch, h } from "vue";
import { useRouter } from "vue-router";
import { NRadioButton, NRadioGroup, NSpin, NButton } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useEcharts } from "@/hooks/common/echarts";
import { fetchDashboard, fetchTraffic } from "@/service/api";
import { currencyUnit, formatMoney, centsToYuan } from "@/utils/money";
import type {
  DashboardData,
  DashboardStat,
  DashboardTopChannel,
  DashboardTopProduct,
  TrafficPoint,
} from "@/service/api";
import { checkAuth } from "@/directives";
import SponsorCard from "./components/sponsor-card.vue";

defineOptions({ name: "Dashboard" });

const router = useRouter();
const loading = ref(false);
const trafficLoading = ref(false);
const data = ref<DashboardData | null>(null);
const loadError = ref("");
const trafficError = ref("");

// KPI 時間窗（今日/近7天/近30天，切換聯動 4 張指標卡 + 環比基準）
const rangeOptions = [
  { label: "今日", value: "today" },
  { label: "近7天", value: "7d" },
  { label: "近30天", value: "30d" },
];
const range = ref<"today" | "7d" | "30d">("today");

const trendDays = computed(() => (range.value === "today" ? 1 : range.value === "7d" ? 7 : 30));
const rangeLabel = computed(
  () => rangeOptions.find((r) => r.value === range.value)?.label || "今日",
);

const EMPTY: DashboardStat = {
  orders: 0,
  revenue: 0,
  paid_orders: 0,
  cost: 0,
  profit: 0,
  new_users: 0,
  refunds: 0,
  net_revenue: 0,
  unknown_cost_orders: 0,
};

function normalizeStat(s?: DashboardStat): DashboardStat {
  const out = { ...EMPTY };
  for (const key of Object.keys(EMPTY) as (keyof DashboardStat)[]) {
    const value = Number(s?.[key] ?? 0);
    out[key] = Number.isFinite(value) ? value : 0;
  }
  return out;
}
// proto3 零值字段不輸出 → 字段級兜底補 0（杜絕 undefined/NaN）
const statOf = (d: DashboardData | null): DashboardStat => {
  if (!d) return EMPTY;
  const s = range.value === "today" ? d.today : range.value === "7d" ? d.last7d : d.last30d;
  return normalizeStat(s);
};
const prevOf = (d: DashboardData | null): DashboardStat => {
  if (!d) return EMPTY;
  const s = range.value === "today" ? d.yesterday : range.value === "7d" ? d.prev7d : d.prev30d;
  return normalizeStat(s);
};

const cur = computed(() => statOf(data.value));
const prev = computed(() => prevOf(data.value));
const prevLabel = computed(() =>
  range.value === "today"
    ? "較昨日同時段"
    : range.value === "7d"
      ? "較前7天同時段"
      : "較前30天同時段",
);

/** 環比（%）；無基數/非法值返回 null 顯示 — */
function ratio(cur: number, prev: number): number | null {
  if (!Number.isFinite(cur) || !Number.isFinite(prev) || prev === 0) return null;
  return ((cur - prev) / prev) * 100;
}

const kpiCards = computed(() => {
  const c = cur.value;
  const p = prev.value;
  return [
    {
      key: "paid",
      label: "訂單支付金額",
      value: data.value ? formatMoney(c.revenue) : "—",
      sub: `退款 ${formatMoney(c.refunds)} · 淨額 ${formatMoney(c.net_revenue)}`,
      ratio: ratio(c.revenue, p.revenue),
      sparkline: true,
      timeField: "paid",
    },
    {
      key: "paid_orders",
      label: "支付訂單數",
      value: data.value ? String(c.paid_orders) : "—",
      sub: "按付款時間",
      ratio: ratio(c.paid_orders, p.paid_orders),
      sparkline: false,
      timeField: "paid",
    },
    {
      key: "orders",
      label: "下單數",
      value: data.value ? String(c.orders) : "—",
      sub: "不含已刪除訂單",
      ratio: ratio(c.orders, p.orders),
      sparkline: false,
      timeField: "created",
    },
    {
      key: "profit",
      label: "預估毛利",
      value: !data.value || c.unknown_cost_orders ? "—" : formatMoney(c.profit),
      sub: c.unknown_cost_orders ? `${c.unknown_cost_orders} 單成本不完整` : "淨支付額減商品成本",
      ratio: c.unknown_cost_orders || p.unknown_cost_orders ? null : ratio(c.profit, p.profit),
      sparkline: false,
      timeField: "",
    },
  ];
});

// 營收卡迷你趨勢線（複用趨勢數據）
const { domRef: sparkRef, updateOptions: updateSpark } = useEcharts(() => ({
  grid: { left: 0, right: 0, top: 2, bottom: 0 },
  xAxis: { type: "category", show: false, boundaryGap: false, data: [] as string[] },
  yAxis: { type: "value", show: false, min: "dataMin" },
  series: [
    {
      type: "line",
      smooth: true,
      showSymbol: false,
      lineStyle: { width: 1.5, color: "#2080f0" },
      areaStyle: { color: "rgba(32,128,240,0.12)" },
      data: [] as number[],
    },
  ],
}));
function setSparkDom(el: unknown) {
  sparkRef.value = (el as HTMLElement | null) ?? null;
}

// 銷售趨勢主圖（營收面積 + 訂單柱，雙軸）
const { domRef: mainRef, updateOptions: updateMain } = useEcharts(() => ({
  tooltip: { trigger: "axis" },
  legend: { data: [`支付金額(${currencyUnit()})`, "支付訂單數"], top: "0" },
  grid: { left: "3%", right: "4%", bottom: "3%", top: "18%", containLabel: true },
  xAxis: { type: "category", boundaryGap: false, data: [] as string[] },
  yAxis: [
    { type: "value", name: `支付金額(${currencyUnit()})`, splitLine: { lineStyle: { type: "dashed" } } },
    { type: "value", name: "支付訂單數", splitLine: { show: false } },
  ],
  series: [
    {
      name: `支付金額(${currencyUnit()})`,
      type: "line",
      smooth: true,
      showSymbol: true,
      symbolSize: 6,
      itemStyle: { color: "#2080f0" },
      lineStyle: { width: 2 },
      areaStyle: { opacity: 0.12 },
      data: [] as number[],
    },
    {
      name: "支付訂單數",
      type: "bar",
      yAxisIndex: 1,
      barMaxWidth: 16,
      itemStyle: { color: "rgba(32,128,240,0.25)", borderRadius: [3, 3, 0, 0] },
      data: [] as number[],
    },
  ],
}));

watch(currencyUnit, (code) => {
  updateMain((opts) => {
    const label = `支付金額(${code})`;
    opts.legend.data[0] = label;
    opts.yAxis[0].name = label;
    opts.series[0].name = label;
    return opts;
  });
});

function renderCharts(d: DashboardData) {
  updateMain((opts) => {
    opts.xAxis.data = (d.trend || []).map((t) => t.date.slice(5));
    opts.series[0].data = (d.trend || []).map((t) => centsToYuan(t.revenue || 0));
    opts.series[1].data = (d.trend || []).map((t) => t.paid_count || 0);
    return opts;
  });
  updateSpark((opts) => {
    opts.xAxis.data = (d.trend || []).map((t) => t.date);
    opts.series[0].data = (d.trend || []).map((t) => centsToYuan(t.revenue || 0));
    return opts;
  });
}

// 流量趨勢圖（PV 柱 + UV 線；跟隨 trendDays 天數）
const { domRef: trafficRef, updateOptions: updateTraffic } = useEcharts(() => ({
  tooltip: { trigger: "axis" },
  legend: { data: ["PV", "UV"], top: "0" },
  grid: { left: "3%", right: "3%", bottom: "3%", top: "18%", containLabel: true },
  xAxis: { type: "category", boundaryGap: false, data: [] as string[] },
  yAxis: { type: "value", splitLine: { lineStyle: { type: "dashed" } } },
  series: [
    {
      name: "PV",
      type: "bar",
      barMaxWidth: 16,
      itemStyle: { color: "rgba(32,128,240,0.25)", borderRadius: [3, 3, 0, 0] },
      data: [] as number[],
    },
    {
      name: "UV",
      type: "line",
      smooth: true,
      showSymbol: false,
      itemStyle: { color: "#18a058" },
      lineStyle: { width: 2 },
      data: [] as number[],
    },
  ],
}));

function renderTraffic(points: TrafficPoint[]) {
  updateTraffic((opts) => {
    opts.xAxis.data = points.map((p) => p.date.slice(5));
    opts.series[0].data = points.map((p) => p.pv || 0);
    opts.series[1].data = points.map((p) => p.uv || 0);
    return opts;
  });
}

const topProducts = computed(() => (data.value?.top_products || []).slice(0, 5));
const onlineUsers = computed(() => data.value?.online_users || 0);

const topColumns: DataTableColumns<DashboardTopProduct> = [
  { title: "排名", key: "rank", width: 60, render: (_row, index) => index + 1 },
  {
    title: "商品",
    key: "name",
    minWidth: 140,
    render: (row) =>
      h(
        NButton,
        {
          text: true,
          disabled: !checkAuth("order:read"),
          onClick: () => openOrders("paid", undefined, row.product_id),
        },
        () => row.name,
      ),
  },
  { title: "購買件數", key: "sold_qty", width: 90, render: (row) => row.sold_qty || 0 },
  { title: "分攤支付額", key: "revenue", width: 110, render: (row) => formatMoney(row.revenue) },
];

// Show Top 5 plus Other; denominator includes every channel, never just Top 5.
const channels = computed(() => {
  const rows = (data.value?.top_channels || []).map((c) => ({
    ...c,
    amount: Math.max(0, Number(c.amount) || 0),
  }));
  if (rows.length <= 5) return rows;
  const rest = rows.slice(5);
  return [
    ...rows.slice(0, 5),
    {
      channel: "__other",
      channel_id: 0,
      name: "其他",
      channel_state: "other",
      amount: rest.reduce((n, c) => n + c.amount, 0),
      total_count: rest.reduce((n, c) => n + Number(c.total_count || 0), 0),
      success_count: 0,
      failed_count: 0,
    },
  ];
});
const channelTotal = computed(() =>
  (data.value?.top_channels || []).reduce((n, c) => n + Math.max(0, Number(c.amount) || 0), 0),
);
function channelRate(c: DashboardTopChannel) {
  return channelTotal.value > 0
    ? Math.min(100, Math.round((c.amount / channelTotal.value) * 1000) / 10)
    : 0;
}
function channelLabel(c: DashboardTopChannel) {
  return `${c.name || c.channel}${c.channel_state === "deleted" ? "（已刪除）" : c.channel_state === "disabled" ? "（已停用）" : c.channel_state === "legacy" ? "（歷史渠道）" : ""}`;
}
const hasSales = computed(() =>
  (data.value?.trend || []).some((p) => Number(p.paid_count) > 0 || Number(p.revenue) > 0),
);
function openOrders(timeField: string, channel?: DashboardTopChannel, productId?: number) {
  if (!data.value || !timeField || !checkAuth("order:read") || channel?.channel_state === "other")
    return;
  router.push({
    path: "/order",
    query: {
      start_time: String(data.value.range_start),
      end_time: String(data.value.range_end),
      time_field: timeField,
      ...(productId ? { product_id: String(productId) } : {}),
      ...(channel
        ? { channel_id: String(channel.channel_id || 0), channel_code: channel.channel }
        : {}),
    },
  });
}

// ── 待辦事項（點擊跳轉對應管理頁）──
const todos = computed(() => {
  const p = data.value?.pending;
  if (!p) return [];
  return [
    ...(checkAuth("ticket:read")
      ? [
          {
            label: "待回覆工單",
            value: p.open_tickets || 0,
            path: { path: "/ticket", query: { status: "open" } },
            warn: true,
          },
          {
            label: "處理中工單",
            value: p.processing_tickets || 0,
            path: { path: "/ticket", query: { status: "processing" } },
            warn: true,
          },
        ]
      : []),
    // 待審對接申請 → 渠道管理·供貨賬號 tab（自動篩待審核）
    {
      label: "待審對接申請（全站）",
      value: p.pending_supplier_applications,
      path: { path: "/channel", query: { tab: "suppliers", status: "applying" } },
      warn: true,
    },
    {
      label: "待審核提現（全站）",
      value: p.pending_withdrawals,
      path: { path: "/wallet", query: { tab: "withdraw", status: "pending" } },
      warn: true,
    },
    {
      label: "待處理退款",
      value: p.pending_refunds,
      path: { path: "/order", query: { status: "needs_refund" } },
      warn: true,
    },
    {
      label: "待完成發貨",
      value: p.fulfilling_orders,
      path: { path: "/order", query: { status: "needs_delivery" } },
      warn: false,
    },
    ...(checkAuth("payment:read_detail")
      ? [
          {
            label: "到賬待核對",
            value: p.payment_reviews || 0,
            path: { path: "/payment-channel", query: { tab: "payments", review_only: "1" } },
            warn: true,
          },
        ]
      : []),
    // 庫存預警 → 商品管理並自動過濾庫存不足商品（商品頁讀 query 啓用篩選）
    {
      label: "庫存預警",
      value: p.low_stock_products,
      path: { path: "/product", query: { low_stock: "1" } },
      warn: true,
    },
  ];
});

async function loadDashboard(silent = false) {
  if (requesting) {
    rerunRequested = true;
    return;
  }
  requesting = true;
  rerunRequested = false;
  if (!silent) loading.value = true;
  trafficLoading.value = !silent;
  const days = trendDays.value;
  try {
    await Promise.allSettled([
      (async () => {
        try {
          const { data: d, error } = await fetchDashboard(days);
          if (days !== trendDays.value) return;
          if (!error && d) {
            data.value = d;
            loadError.value = "";
            renderCharts(d);
          } else
            loadError.value = data.value
              ? "更新失敗，當前顯示上次成功獲取的數據"
              : "統計加載失敗，請重試";
        } catch {
          if (days === trendDays.value) loadError.value = "統計加載失敗，請重試";
        } finally {
          loading.value = false;
        }
      })(),
      (async () => {
        try {
          const { data: t, error } = await fetchTraffic(days);
          if (days !== trendDays.value) return;
          if (!error && t) {
            renderTraffic(t.points || []);
            trafficError.value = "";
          } else trafficError.value = "訪問統計加載失敗";
        } catch {
          trafficError.value = "訪問統計加載失敗";
        } finally {
          trafficLoading.value = false;
        }
      })(),
    ]);
  } finally {
    requesting = false;
    // A range change during a pending refresh must not be lost.
    if (rerunRequested || days !== trendDays.value) void loadDashboard(true);
  }
}

// KeepAlive 返回首頁立即刷新；停留首頁時每分鐘更新，離開/後台標籤頁不輪詢。
let requesting = false;
let rerunRequested = false;
let refreshTimer: ReturnType<typeof setInterval> | undefined;
function startRefresh() {
  if (refreshTimer) return;
  void loadDashboard(Boolean(data.value));
  refreshTimer = setInterval(() => {
    if (!document.hidden) void loadDashboard(true);
  }, 60_000);
}
function stopRefresh() {
  clearInterval(refreshTimer);
  refreshTimer = undefined;
}
watch(trendDays, () => {
  data.value = null;
  loadDashboard();
});
onMounted(startRefresh);
onActivated(startRefresh);
onDeactivated(stopRefresh);
onUnmounted(stopRefresh);
</script>

<template>
  <NSpin :show="loading">
    <div class="flex flex-col gap-16px">
      <NAlert
        v-if="
          checkAuth('ticket:read') &&
          data?.pending &&
          (data.pending.open_tickets > 0 || data.pending.processing_tickets > 0)
        "
        type="warning"
        title="工單待處理"
      >
        <div class="flex flex-wrap items-center justify-between gap-12px">
          <span
            >待回覆 {{ data.pending.open_tickets || 0 }} 單，處理中
            {{ data.pending.processing_tickets || 0 }} 單<span
              v-if="data.pending.urgent_tickets > 0"
              >，其中 {{ data.pending.urgent_tickets }} 單已付費加急，請優先處理</span
            >。</span
          >
          <NButton
            size="small"
            type="warning"
            @click="
              router.push({
                path: '/ticket',
                query: { status: data.pending.open_tickets > 0 ? 'open' : 'processing' },
              })
            "
            >處理工單</NButton
          >
        </div>
      </NAlert>
      <!-- 頭部：標題 + 在線用户 + KPI 時間窗 -->
      <div class="flex flex-wrap gap-12px items-center justify-between">
        <span class="flex items-center gap-8px">
          <span class="text-16px font-semibold">工作台</span>
          <span class="flex items-center gap-4px text-13px text-gray-500">
            <span class="h-8px w-8px rounded-full bg-green-500"></span>
            在線 {{ onlineUsers }} 人
          </span>
        </span>
        <NRadioGroup v-model:value="range" size="small">
          <NRadioButton v-for="o in rangeOptions" :key="o.value" :value="o.value">{{
            o.label
          }}</NRadioButton>
        </NRadioGroup>
      </div>

      <div class="text-12px text-gray-500">
        北京時間 · {{ data?.subsite_id ? "當前分站" : "主站" }}訂單 · 全站新增用户
        {{ data ? cur.new_users : "—" }} 人<span v-if="data?.generated_at">
          · 更新於
          {{
            new Date(data.generated_at * 1000).toLocaleTimeString("zh-TW", {
              timeZone: "Asia/Shanghai",
            })
          }}</span
        >
      </div>
      <NAlert v-if="loadError" type="warning" :title="loadError"
        ><NButton size="small" @click="loadDashboard()">重新加載</NButton></NAlert
      >
      <NAlert v-else-if="!data && !loading" type="info" title="暫無統計數據" />
      <SponsorCard />

      <!-- KPI 指標卡（營收帶迷你趨勢線；環比紅漲綠跌） -->
      <NGrid :x-gap="16" :y-gap="16" cols="1 m:2 l:4" responsive="screen">
        <NGi v-for="card in kpiCards" :key="card.key">
          <NCard :bordered="false" size="small" class="h-full">
            <div class="flex flex-wrap gap-4px items-center justify-between">
              <span class="text-13px text-gray-500">{{ card.label }}</span>
              <span v-if="card.sub" class="text-12px text-gray-400">{{ card.sub }}</span>
            </div>
            <button
              v-if="card.timeField && checkAuth('order:read')"
              type="button"
              class="mt-6px border-none bg-transparent p-0 cursor-pointer text-26px font-bold leading-tight text-left"
              :disabled="!data"
              :aria-label="`查看${card.label}明細`"
              @click="openOrders(card.timeField)"
            >
              {{ card.value }}
            </button>
            <div v-else class="mt-6px text-26px font-bold leading-tight">{{ card.value }}</div>
            <div class="mt-6px flex items-center gap-4px text-12px">
              <span
                v-if="card.ratio !== null"
                :class="card.ratio >= 0 ? 'text-red-500' : 'text-green-500'"
                class="font-medium"
              >
                {{ card.ratio >= 0 ? "↑" : "↓" }}{{ Math.abs(card.ratio).toFixed(1) }}%
              </span>
              <span v-else class="text-gray-400">—</span>
              <span class="text-gray-400">{{ prevLabel }}</span>
            </div>
            <div v-if="card.sparkline" :ref="setSparkDom" class="mt-8px h-36px"></div>
          </NCard>
        </NGi>
      </NGrid>

      <!-- 中部：銷售趨勢 + 支付渠道排行 -->
      <NGrid :x-gap="16" :y-gap="16" cols="1 l:3" responsive="screen">
        <NGi span="1 l:2">
          <NCard :bordered="false">
            <template #header>
              <div class="flex items-center justify-between">
                <span>訂單支付趨勢 · {{ rangeLabel }}</span>
              </div>
            </template>
            <div v-if="data && !hasSales" class="py-20px text-center text-gray-500">
              該時段暫無支付訂單
            </div>
            <div v-show="hasSales" ref="mainRef" class="h-320px"></div>
          </NCard>
        </NGi>
        <NGi>
          <div class="flex flex-col gap-16px">
            <NCard :title="`訂單支付方式分佈 · ${rangeLabel}`" :bordered="false">
              <div v-if="channels.length" class="flex flex-col gap-14px">
                <div v-for="c in channels" :key="`${c.channel_id}:${c.channel}`">
                  <div class="flex flex-wrap gap-4px items-center justify-between text-13px">
                    <button
                      type="button"
                      class="border-none bg-transparent p-0 cursor-pointer text-left break-all"
                      :disabled="c.channel_state === 'other' || !checkAuth('order:read')"
                      @click="openOrders('paid', c)"
                    >
                      {{ channelLabel(c) }}
                    </button>
                    <span class="text-gray-400">
                      {{ formatMoney(c.amount) }} · {{ c.total_count || 0 }} 筆 ·
                      <span class="font-medium">{{ channelRate(c) }}%</span>
                    </span>
                  </div>
                  <div
                    class="mt-6px h-6px overflow-hidden rounded-full bg-gray-200 dark:bg-gray-700"
                  >
                    <div
                      class="h-full rounded-full"
                      :style="{ width: channelRate(c) + '%', backgroundColor: '#2080f0' }"
                    ></div>
                  </div>
                </div>
              </div>
              <div v-else class="py-20px text-center text-13px text-gray-400">
                該時段暫無訂單支付
              </div>
            </NCard>
            <NCard title="流量趨勢（PV/UV）" :bordered="false">
              <NSpin :show="trafficLoading">
                <div v-if="trafficError" class="text-gray-500">
                  {{ trafficError }} <NButton size="small" @click="loadDashboard()">重試</NButton>
                </div>
                <div v-show="!trafficError" ref="trafficRef" class="h-180px"></div>
              </NSpin>
            </NCard>
          </div>
        </NGi>
      </NGrid>

      <div class="text-12px text-gray-500">
        支付金額包含餘額支付，不含充值及待核對到賬；退款按成功時間計入。預估毛利未扣渠道手續費、佣金等費用，退款不自動衝回商品成本。商品金額按訂單最終應付分攤。
      </div>
      <!-- 底部：商品銷量 Top5 + 待辦事項 -->
      <NGrid :x-gap="16" :y-gap="16" cols="1 l:3" responsive="screen">
        <NGi span="1 l:2">
          <div class="flex flex-col gap-16px">
            <NCard :title="`商品支付金額 Top5 · ${rangeLabel}`" :bordered="false">
              <NDataTable
                :columns="topColumns"
                :data="topProducts"
                size="small"
                :bordered="false"
                :max-height="540"
                :scroll-x="400"
              />
            </NCard>
          </div>
        </NGi>
        <NGi>
          <NCard title="待辦事項" :bordered="false">
            <div class="flex flex-col">
              <button
                type="button"
                v-for="t in todos"
                :key="t.label"
                class="flex bg-transparent text-left cursor-pointer items-center justify-between rounded-4px border-b border-gray-100 px-4px py-10px last:border-none hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-800"
                @click="router.push(t.path)"
              >
                <span class="text-13px">{{ t.label }}</span>
                <span
                  v-if="t.value > 0"
                  :class="t.warn ? 'bg-red-500' : 'bg-blue-500'"
                  class="rounded-full px-8px py-2px text-12px text-white"
                  >{{ t.value }}</span
                >
                <span v-else class="text-12px text-gray-400">無</span>
              </button>
            </div>
          </NCard>
        </NGi>
      </NGrid>
    </div>
  </NSpin>
</template>
