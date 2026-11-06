<script setup lang="ts">
import { newRequestId } from "../../../../../packages/request-id";
import { ref, reactive, computed, onMounted, h } from "vue";
import { NButton, NTag, NSpace, NPopconfirm } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { useRouter } from "vue-router";
import { checkAuth } from "@/directives";
import {
  fetchProducts,
  fetchProduct,
  fetchSkus,
  fetchUsers,
  listLotteryActivities,
  getLotteryActivity,
  saveLotteryActivity,
  setLotteryStatus,
  grantLotteryChances,
  listLotteryDraws,
  revealLotteryDraw,
  deliverLotteryDraw,
  listLotteryHistory,
  lotteryState,
  lotteryMode,
} from "@/service/api";
import type { LotteryActivity, LotteryPrize, LotteryDraw } from "@/service/api";
import { formatMoney } from "@/utils/money";
import TablePager from "@/components/common/table-pager.vue";
import MediaField from "@/components/common/media-picker/media-field.vue";
const router = useRouter();
const tab = ref("activities"),
  loading = ref(false),
  saving = ref(false),
  page = ref(1),
  pageSize = ref(20),
  total = ref(0),
  activities = ref<LotteryActivity[]>([]),
  keyword = ref("");
const showEdit = ref(false),
  step = ref(1),
  formError = ref("");
const fresh = (): LotteryActivity => ({
  name: "",
  description: "",
  image: "",
  status: "draft",
  start_at: Math.floor(Date.now() / 1000),
  end_at: Math.floor(Date.now() / 1000) + 7 * 86400,
  timezone: "Asia/Shanghai",
  chance_mode: "once",
  chance_count: 3,
  revision: 0,
  prizes: [],
});
const form = ref<LotteryActivity>(fresh());
const dateRange = ref<[number, number]>([
  Date.now(),
  Date.now() + 7 * 86400000,
]);
const modes = Object.entries(lotteryMode).map(([value, label]) => ({
  value,
  label,
}));
const chanceModes = [
  { label: "活動期間贈送一次", value: "once" },
  { label: "每日贈送（不累計）", value: "daily" },
  { label: "關閉自動贈送，僅後台補發", value: "manual" },
];
const timezones = [
  { label: "北京時間（UTC+8）", value: "Asia/Shanghai" },
  { label: "越南時間（UTC+7）", value: "Asia/Ho_Chi_Minh" },
  { label: "協調世界時（UTC）", value: "UTC" },
];
const probability = computed(() =>
  form.value.prizes.reduce((n, p) => n + p.probability, 0),
);
const time = (n?: number) => (n ? new Date(n * 1000).toLocaleString() : "—");
const stateTag = (v: string) =>
  h(
    NTag,
    {
      size: "small",
      type:
        v === "live" || v === "delivered"
          ? "success"
          : v === "pending" || v === "paused"
            ? "warning"
            : "default",
    },
    { default: () => lotteryState[v] || "未知狀態" },
  );
async function load() {
  loading.value = true;
  try {
    const { data, error } = await listLotteryActivities({
      page: page.value,
      page_size: pageSize.value,
      keyword: keyword.value,
    });
    if (!error && data) {
      activities.value = data.items || [];
      total.value = Number(data.total || 0);
    }
  } finally {
    loading.value = false;
  }
}
function addPrize() {
  if (form.value.prizes.length < 12)
    form.value.prizes.push({
      name: "",
      image: "",
      mode: "manual",
      product_id: 0,
      sku_id: 0,
      probability: 0,
      quantity: 10,
      content: "",
    });
}
const productOptions = ref<{ label: string; value: number; cover?: string }[]>(
    [],
  ),
  productLoading = ref(false),
  skuOptions = reactive<Record<number, { label: string; value: number }[]>>({});
let productSearchSeq = 0;
async function searchProducts(q = "") {
  const seq = ++productSearchSeq;
  productLoading.value = true;
  try {
    const { data, error } = await fetchProducts({
      keyword: q,
      status: 1,
      local_only: true,
      stock_type: "card",
      page_size: 50,
    });
    if (seq !== productSearchSeq) return;
    if (!error && data) {
      const rows = (data as any).products || [];
      const next = rows
        .filter(
          (p: any) =>
            p.stock_type === "card" &&
            !p.upstream_source_id &&
            p.delivery_mode !== "delete",
        )
        .map((p: any) => ({
          label: `${p.name}（#${p.id}）`,
          value: p.id,
          cover: p.cover,
        }));
      productOptions.value = [
        ...next,
        ...productOptions.value.filter(
          (p) => !next.some((n: any) => n.value === p.value),
        ),
      ].slice(0, 200);
    }
  } finally {
    if (seq === productSearchSeq) productLoading.value = false;
  }
}
async function loadSkus(id: number) {
  if (!id || skuOptions[id]) return;
  const { data, error } = await fetchSkus(id);
  if (!error && data)
    skuOptions[id] = ((data as any).skus || []).map((s: any) => ({
      label: s.name,
      value: s.id,
    }));
}
async function selectProduct(p: LotteryPrize, id: number | null) {
  p.product_id = id || 0;
  p.sku_id = 0;
  const o = productOptions.value.find((x) => x.value === id);
  if (!p.name && o) p.name = o.label.replace(/（#\d+）$/, "");
  if (!p.image && o?.cover) p.image = o.cover;
  if (id) await loadSkus(id);
}
async function edit(row?: LotteryActivity, copy = false) {
  formError.value = "";
  step.value = 1;
  if (row?.id) {
    const { data, error } = await getLotteryActivity(row.id);
    if (error || !data) return;
    form.value = {
      ...data,
      prizes: (data.prizes || []).map((p) => ({
        ...p,
        probability: p.probability || 0,
        issued: p.issued || 0,
        product_id: p.product_id || 0,
        sku_id: p.sku_id || 0,
        content: p.content || "",
      })),
    };
    if (copy) {
      form.value = {
        ...form.value,
        id: undefined,
        name: form.value.name + "（副本）",
        status: "draft",
        published: false,
        revision: 0,
        start_at: Math.floor(Date.now() / 1000),
        end_at: Math.floor(Date.now() / 1000) + 7 * 86400,
        prizes: form.value.prizes.map((p) => ({
          ...p,
          id: undefined,
          issued: 0,
        })),
      };
    }
    for (const p of form.value.prizes) {
      if (p.product_id) {
        const { data } = await fetchProduct(p.product_id);
        if (data && !productOptions.value.some((o) => o.value === p.product_id))
          productOptions.value.push({
            label: `${(data as any).name}（#${p.product_id}）`,
            value: p.product_id,
          });
        await loadSkus(p.product_id);
      }
    }
  } else {
    form.value = fresh();
    addPrize();
  }
  dateRange.value = [form.value.start_at * 1000, form.value.end_at * 1000];
  showEdit.value = true;
  void searchProducts();
}
function validate() {
  if (!form.value.name.trim()) return "請填寫活動名稱";
  if (!dateRange.value || dateRange.value[1] <= dateRange.value[0])
    return "請選擇正確的活動時間";
  if (!form.value.prizes.length) return "請添加獎品";
  if (probability.value <= 0 || probability.value > 10000)
    return "獎品概率合計須大於 0 且不超過 100%";
  for (const p of form.value.prizes) {
    if (!p.name.trim()) return "請填寫每種獎品的名稱";
    if (p.mode === "card" && !p.product_id) return "自動髮卡需要選擇商品";
    if (
      p.mode === "card" &&
      (skuOptions[p.product_id]?.length || 0) > 0 &&
      !p.sku_id
    )
      return "請選擇獎品具體規格";
    if (p.mode !== "card" && !p.content.trim())
      return "請填寫發放內容或領取説明";
    if (!p.quantity || p.quantity < (p.issued || 0))
      return "獎品數量不能少於已發數量";
  }
  return "";
}
async function save() {
  formError.value = validate();
  if (formError.value) return;
  saving.value = true;
  try {
    const { error } = await saveLotteryActivity({
      ...form.value,
      start_at: Math.floor(dateRange.value[0] / 1000),
      end_at: Math.floor(dateRange.value[1] / 1000),
    });
    if (!error) {
      showEdit.value = false;
      window.$message?.success("活動配置已保存");
      await load();
    }
  } finally {
    saving.value = false;
  }
}
const changing = ref(false);
async function change(row: LotteryActivity, status: string) {
  if (!row.id || changing.value) return;
  changing.value = true;
  try {
    const { error } = await setLotteryStatus(row.id, status, row.revision);
    if (!error) {
      window.$message?.success("活動狀態已更新");
      await load();
    }
  } finally {
    changing.value = false;
  }
}
const columns: DataTableColumns<LotteryActivity> = [
  {
    title: "活動",
    key: "name",
    minWidth: 180,
    render: (r) =>
      h("div", [
        h("div", r.name),
        h(
          "small",
          { class: "text-gray-500" },
          `#${r.id} · 規則版本 ${r.revision}`,
        ),
      ]),
  },
  {
    title: "活動時間",
    key: "time",
    minWidth: 190,
    render: (r) =>
      h("div", [h("div", time(r.start_at)), h("div", time(r.end_at))]),
  },
  {
    title: "次數規則",
    key: "chance",
    minWidth: 170,
    render: (r) =>
      `${chanceModes.find((m) => m.value === r.chance_mode)?.label || "—"}${r.chance_mode === "manual" ? "" : ` · ${r.chance_count} 次`}`,
  },
  {
    title: "狀態",
    key: "status",
    width: 95,
    render: (r) => stateTag(r.status),
  },
  {
    title: "操作",
    key: "actions",
    minWidth: 370,
    render: (r) =>
      h(
        NSpace,
        { size: 6, wrap: true },
        {
          default: () => [
            ...(checkAuth("lottery:write")
              ? [
                  h(
                    NButton,
                    {
                      size: "tiny",
                      disabled: [
                        "live",
                        "scheduled",
                        "ended",
                        "archived",
                      ].includes(r.status),
                      onClick: () => edit(r),
                    },
                    { default: () => "編輯" },
                  ),
                  h(
                    NButton,
                    { size: "tiny", onClick: () => edit(r, true) },
                    { default: () => "複製" },
                  ),
                  ...(["draft", "paused"].includes(r.status)
                    ? [
                        h(
                          NPopconfirm,
                          { onPositiveClick: () => change(r, "live") },
                          {
                            trigger: () =>
                              h(
                                NButton,
                                {
                                  size: "tiny",
                                  type: "success",
                                  disabled: changing.value,
                                },
                                { default: () => "發佈/恢復" },
                              ),
                            default: () =>
                              "發佈後按規則發放次數和獎品，確認發佈？",
                          },
                        ),
                      ]
                    : []),
                  ...(["live", "scheduled"].includes(r.status)
                    ? [
                        h(
                          NButton,
                          {
                            size: "tiny",
                            disabled: changing.value,
                            onClick: () => change(r, "paused"),
                          },
                          { default: () => "暫停" },
                        ),
                      ]
                    : []),
                  ...(["live", "scheduled", "paused"].includes(r.status)
                    ? [
                        h(
                          NPopconfirm,
                          { onPositiveClick: () => change(r, "ended") },
                          {
                            trigger: () =>
                              h(
                                NButton,
                                { size: "tiny", disabled: changing.value },
                                { default: () => "結束" },
                              ),
                            default: () => "結束後停止新增抽獎，保留歷史獎品。",
                          },
                        ),
                      ]
                    : []),
                  ...(r.status !== "archived"
                    ? [
                        h(
                          NPopconfirm,
                          { onPositiveClick: () => change(r, "archived") },
                          {
                            trigger: () =>
                              h(
                                NButton,
                                { size: "tiny", disabled: changing.value },
                                { default: () => "歸檔" },
                              ),
                            default: () =>
                              "歸檔活動並隱藏入口，保留歷史領獎記錄？",
                          },
                        ),
                      ]
                    : []),
                  h(
                    NButton,
                    { size: "tiny", onClick: () => preview(r) },
                    { default: () => "預覽" },
                  ),
                ]
              : []),
            ...(checkAuth("lottery:grant") &&
            r.published &&
            !["ended", "archived"].includes(r.status)
              ? [
                  h(
                    NButton,
                    { size: "tiny", onClick: () => openGrant(r) },
                    { default: () => "補次數" },
                  ),
                ]
              : []),
            h(
              NButton,
              { size: "tiny", onClick: () => history(r) },
              { default: () => "規則歷史" },
            ),
            h(
              NButton,
              {
                size: "tiny",
                onClick: () => {
                  recordActivity.value = r.id || null;
                  tab.value = "records";
                  loadRecords();
                },
              },
              { default: () => "中獎記錄" },
            ),
          ],
        },
      ),
  },
];
const showPreview = ref(false),
  previewData = ref<LotteryActivity | null>(null),
  demo = ref(false);
async function preview(r: LotteryActivity) {
  const { data, error } = await getLotteryActivity(r.id!);
  if (!error && data) {
    previewData.value = data;
    demo.value = false;
    showPreview.value = true;
  }
}
const showHistory = ref(false),
  historyItems = ref<any[]>([]);
async function history(r: LotteryActivity) {
  const { data, error } = await listLotteryHistory(r.id!);
  if (!error && data) {
    historyItems.value = (data.items || []).map((h) => ({
      ...h,
      config: JSON.parse(h.snapshot_json),
    }));
    showHistory.value = true;
  }
}
const showGrant = ref(false),
  grantActivity = ref<LotteryActivity | null>(null),
  grantUser = ref<number | null>(null),
  grantCount = ref(1),
  grantRemark = ref(""),
  grantKey = ref(""),
  userOptions = ref<{ label: string; value: number }[]>([]);
function newKey() {
  return newRequestId().replaceAll("-", "");
}
async function searchUsers(keyword = "") {
  const { data, error } = await fetchUsers({ keyword, page_size: 30 });
  if (!error && data)
    userOptions.value = ((data as any).users || []).map((u: any) => ({
      label: `${u.username}（#${u.id}）`,
      value: u.id,
    }));
}
function openGrant(r: LotteryActivity) {
  grantActivity.value = r;
  grantUser.value = null;
  grantCount.value = 1;
  grantRemark.value = "";
  grantKey.value = newKey();
  showGrant.value = true;
  void searchUsers();
}
async function grant() {
  if (!grantActivity.value?.id || !grantUser.value || !grantRemark.value.trim())
    return;
  saving.value = true;
  try {
    const { error } = await grantLotteryChances(grantActivity.value.id, {
      user_id: grantUser.value,
      count: grantCount.value,
      remark: grantRemark.value,
      request_key: grantKey.value,
    });
    if (!error) {
      showGrant.value = false;
      window.$message?.success("次數已補發");
    }
  } finally {
    saving.value = false;
  }
}
const records = ref<LotteryDraw[]>([]),
  recordTotal = ref(0),
  recordPage = ref(1),
  recordSize = ref(20),
  recordActivity = ref<number | null>(null),
  recordKeyword = ref(""),
  recordStatus = ref<string | null>(null),
  includeMissed = ref(false),
  recordRange = ref<[number, number] | null>(null),
  recordLoading = ref(false);
async function loadRecords() {
  recordLoading.value = true;
  try {
    const { data, error } = await listLotteryDraws({
      page: recordPage.value,
      page_size: recordSize.value,
      activity_id: recordActivity.value || undefined,
      keyword: recordKeyword.value,
      status: recordStatus.value || undefined,
      include_missed: includeMissed.value,
      start_at: recordRange.value
        ? Math.floor(recordRange.value[0] / 1000)
        : undefined,
      end_at: recordRange.value
        ? Math.floor(recordRange.value[1] / 1000)
        : undefined,
    });
    if (!error && data) {
      records.value = data.items || [];
      recordTotal.value = Number(data.total || 0);
    }
  } finally {
    recordLoading.value = false;
  }
}
const selectedDraw = ref<LotteryDraw | null>(null),
  drawContent = ref(""),
  deliveryRemark = ref("");
async function viewRecord(r: LotteryDraw) {
  selectedDraw.value = r;
  drawContent.value = "";
  deliveryRemark.value = "";
}
async function reveal() {
  if (!selectedDraw.value) return;
  const { data, error } = await revealLotteryDraw(selectedDraw.value.draw_no);
  if (!error && data) drawContent.value = data.content || "無發放內容";
}
async function deliver() {
  if (!selectedDraw.value) return;
  saving.value = true;
  try {
    const { error } = await deliverLotteryDraw(
      selectedDraw.value.draw_no,
      deliveryRemark.value,
    );
    if (!error) {
      selectedDraw.value = null;
      window.$message?.success("已確認發放");
      await loadRecords();
    }
  } finally {
    saving.value = false;
  }
}
const recordColumns: DataTableColumns<LotteryDraw> = [
  { title: "中獎編號", key: "draw_no", width: 280 },
  {
    title: "賬號",
    key: "username",
    width: 150,
    render: (r) =>
      h(
        NButton,
        {
          text: true,
          type: "primary",
          onClick: () =>
            router.push({
              path: "/user",
              query: { keyword: r.username || String(r.user_id) },
            }),
        },
        { default: () => `${r.username || "賬號"} #${r.user_id}` },
      ),
  },
  {
    title: "活動 / 獎品",
    key: "prize",
    minWidth: 180,
    render: (r) =>
      h("div", [
        h("div", r.activity_name),
        h("div", r.prize_name || "謝謝參與"),
      ]),
  },
  {
    title: "發放方式",
    key: "mode",
    width: 140,
    render: (r) => lotteryMode[r.mode] || "—",
  },
  {
    title: "狀態",
    key: "status",
    width: 110,
    render: (r) => stateTag(r.status),
  },
  {
    title: "時間",
    key: "created_at",
    width: 170,
    render: (r) => time(r.created_at),
  },
  {
    title: "操作",
    key: "action",
    width: 90,
    render: (r) =>
      h(
        NButton,
        { size: "small", onClick: () => viewRecord(r) },
        { default: () => "詳情" },
      ),
  },
];
onMounted(load);
</script>

<template>
  <div class="lottery-admin">
    <NAlert type="info" class="mb-16px"
      >免費抽獎按賬號記錄次數。系統髮卡共用商品庫存；人工獎品在中獎記錄中確認發放。</NAlert
    >
    <NTabs
      v-model:value="tab"
      type="segment"
      @update:value="(v) => v === 'records' && loadRecords()"
    >
      <NTabPane name="activities" tab="活動管理">
        <div class="lottery-toolbar">
          <NButton
            v-if="checkAuth('lottery:write')"
            type="primary"
            @click="edit()"
            >新建抽獎活動</NButton
          ><NInput
            v-model:value="keyword"
            placeholder="搜索活動名稱"
            clearable
            style="max-width: 280px"
            @keyup.enter="
              page = 1;
              load();
            "
          /><NButton
            @click="
              page = 1;
              load();
            "
            >搜索</NButton
          >
        </div>
        <NDataTable
          :columns="columns"
          :data="activities"
          :loading="loading"
          :scroll-x="1100"
          :row-key="(r) => r.id"
        />
        <TablePager
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="total"
          @change="load"
        />
      </NTabPane>
      <NTabPane name="records" tab="中獎記錄">
        <div class="lottery-toolbar">
          <NSelect
            v-model:value="recordActivity"
            :options="activities.map((a) => ({ label: a.name, value: a.id! }))"
            placeholder="全部活動"
            clearable
            filterable
            style="width: 210px"
          /><NInput
            v-model:value="recordKeyword"
            placeholder="用户名或完整中獎編號"
            style="max-width: 240px"
            clearable
          /><NSelect
            v-model:value="recordStatus"
            :options="
              ['pending', 'delivered', 'missed'].map((value) => ({
                value,
                label: lotteryState[value],
              }))
            "
            placeholder="全部狀態"
            clearable
            style="width: 150px"
          /><NDatePicker
            v-model:value="recordRange"
            type="datetimerange"
            clearable
          /><NCheckbox v-model:checked="includeMissed">包括未中獎</NCheckbox
          ><NButton
            @click="
              recordPage = 1;
              loadRecords();
            "
            >查詢</NButton
          >
        </div>
        <NDataTable
          :columns="recordColumns"
          :data="records"
          :loading="recordLoading"
          :scroll-x="1140"
          :row-key="(r) => r.draw_no"
        />
        <TablePager
          v-model:page="recordPage"
          v-model:page-size="recordSize"
          :total="recordTotal"
          @change="loadRecords"
        />
      </NTabPane>
    </NTabs>
    <NModal
      v-model:show="showEdit"
      preset="card"
      :title="form.id ? '編輯抽獎活動' : '新建抽獎活動'"
      class="lottery-modal"
      :mask-closable="!saving"
      :closable="!saving"
      :close-on-esc="!saving"
    >
      <NSteps :current="step" size="small" class="mb-24px"
        ><NStep title="基本信息" /><NStep title="獎品設置" /><NStep
          title="次數規則"
      /></NSteps>
      <NForm label-placement="top">
        <div v-show="step === 1">
          <NFormItem label="活動名稱" required
            ><NInput
              v-model:value="form.name"
              :maxlength="100"
              placeholder="例如：會員幸運抽獎" /></NFormItem
          ><NFormItem label="活動時間" required
            ><NDatePicker
              v-model:value="dateRange"
              type="datetimerange"
              :is-date-disabled="() => false"
              class="w-full" /></NFormItem
          ><NFormItem label="活動時區"
            ><NSelect
              v-model:value="form.timezone"
              :options="timezones"
              :disabled="form.published" /></NFormItem
          ><NFormItem label="活動圖片（可選）"
            ><MediaField :value="form.image ? [form.image] : []" @update:value="urls => form.image = urls[0] || ''" /></NFormItem
          ><NFormItem label="活動説明"
            ><NInput
              v-model:value="form.description"
              type="textarea"
              :autosize="{ minRows: 4, maxRows: 10 }"
              :maxlength="5000"
              placeholder="參與條件、活動時間及領取説明"
          /></NFormItem>
        </div>
        <div v-show="step === 2">
          <NAlert
            :type="
              probability > 10000 || probability <= 0 ? 'warning' : 'success'
            "
            class="mb-16px"
            >獎品概率合計 {{ (probability / 100).toFixed(2) }}%；謝謝參與
            {{
              (Math.max(0, 10000 - probability) / 100).toFixed(2)
            }}%。任一有概率的獎品耗盡後暫停抽獎，補足後恢復。</NAlert
          >
          <div
            v-for="(p, i) in form.prizes"
            :key="i"
            class="lottery-prize-editor"
          >
            <div class="flex items-center justify-between mb-12px">
              <b
                >獎品 {{ i + 1
                }}<span v-if="p.issued"> · 已發 {{ p.issued }} 份</span></b
              ><NButton
                size="small"
                quaternary
                type="error"
                @click="form.prizes.splice(i, 1)"
                >移除獎項</NButton
              >
            </div>
            <div class="lottery-form-grid">
              <NFormItem label="獎品名稱" required
                ><NInput
                  v-model:value="p.name"
                  :maxlength="100"
                  placeholder="中獎時顯示的名稱" /></NFormItem
              ><NFormItem label="發放方式" required
                ><NSelect
                  v-model:value="p.mode"
                  :options="modes"
                  :disabled="!!p.issued"
                  @update:value="
                    () => {
                      p.product_id = 0;
                      p.sku_id = 0;
                    }
                  "
              /></NFormItem>
            </div>
            <template v-if="p.mode === 'card'"
              ><NFormItem label="自動髮卡商品" required
                ><NSelect
                  :value="p.product_id || null"
                  :options="productOptions"
                  :loading="productLoading"
                  remote
                  filterable
                  :disabled="!!p.issued"
                  placeholder="搜索在售自營卡密商品（上游商品不自動發獎）"
                  @search="searchProducts"
                  @update:value="(id) => selectProduct(p, id)" /></NFormItem
              ><NFormItem
                v-if="skuOptions[p.product_id]?.length"
                label="發獎規格"
                required
                ><NSelect
                  v-model:value="p.sku_id"
                  :options="skuOptions[p.product_id]"
                  :disabled="!!p.issued"
                  placeholder="請選擇具體規格" /></NFormItem
              ><NAlert type="warning" class="mb-12px"
                >中獎會消耗該商品一張可用卡密，影響店鋪可售庫存。不支持上游代發、必填下單控件、靚號和發後即刪商品。</NAlert
              ></template
            >
            <NFormItem
              v-else
              :label="
                p.mode === 'manual'
                  ? '中獎後的領取説明'
                  : '中獎後自動顯示的文字或鏈接'
              "
              required
              ><NInput
                v-model:value="p.content"
                type="textarea"
                :autosize="{ minRows: 3, maxRows: 8 }"
                :maxlength="10000"
                :placeholder="
                  p.mode === 'manual'
                    ? '例如：請聯繫客服，提供中獎編號領取。'
                    : '每位中獎者獲得同一份內容；不同卡密請選擇系統髮卡。'
                "
            /></NFormItem>
            <div class="lottery-form-grid">
              <NFormItem label="中獎概率（%）" required
                ><NInputNumber
                  :value="p.probability / 100"
                  :min="0"
                  :max="100"
                  :precision="2"
                  :show-button="false"
                  class="w-full"
                  @update:value="
                    (v) => (p.probability = Math.round((v || 0) * 100))
                  " /></NFormItem
              ><NFormItem label="活動可發總量（份）" required
                ><NInputNumber
                  v-model:value="p.quantity"
                  :min="Math.max(1, p.issued || 0)"
                  :max="1000000"
                  :precision="0"
                  :show-button="false"
                  class="w-full"
              /></NFormItem>
            </div>
            <NFormItem label="獎品圖片（可選）"
              ><MediaField :value="p.image ? [p.image] : []" @update:value="urls => p.image = urls[0] || ''"
            /></NFormItem>
          </div>
          <NButton
            dashed
            block
            :disabled="form.prizes.length >= 12"
            @click="addPrize"
            >添加獎品（{{ form.prizes.length }}/12）</NButton
          >
        </div>
        <div v-show="step === 3">
          <NAlert type="info" class="mb-16px"
            >遊客可查看，登錄後才可參與。贈送次數按賬號去重，重複登錄或換設備不會重複領取。</NAlert
          ><NFormItem label="自動贈送規則"
            ><NSelect
              v-model:value="form.chance_mode"
              :options="chanceModes"
              :disabled="form.published" /></NFormItem
          ><NFormItem
            v-if="form.chance_mode !== 'manual'"
            label="每次贈送次數"
            required
            ><NInputNumber
              v-model:value="form.chance_count"
              :min="1"
              :max="1000"
              :precision="0"
              :disabled="form.published"
          /></NFormItem>
          <p>
            {{
              form.chance_mode === "daily"
                ? "每日按活動時區重新領取，當日次數不累計到次日。"
                : form.chance_mode === "manual"
                  ? "關閉自動贈送後，需在活動列表中給指定賬號補發次數。"
                  : "每個賬號在整個活動期間只領取一次，暫停或重新登錄不會重置。"
            }}
          </p>
          <p class="mt-12px text-gray-500">
            發佈後次數規則固定，需要更改請複製新活動。每次獨立抽獎，允許重複中獎。
          </p>
        </div>
      </NForm>
      <NAlert v-if="formError" type="error" class="mt-12px">{{
        formError
      }}</NAlert>
      <template #footer
        ><div class="flex flex-wrap justify-end gap-8px">
          <NButton :disabled="saving" @click="showEdit = false">取消</NButton
          ><NButton v-if="step > 1" :disabled="saving" @click="step--"
            >上一步</NButton
          ><NButton v-if="step < 3" type="primary" @click="step++"
            >下一步</NButton
          ><NButton v-else type="primary" :loading="saving" @click="save"
            >保存配置</NButton
          >
        </div></template
      >
    </NModal>
    <NModal
      v-model:show="showPreview"
      preset="card"
      title="活動預覽"
      class="lottery-small-modal"
      ><NAlert type="info" class="mb-16px"
        >演示預覽，不扣次數，不發真實獎品。</NAlert
      >
      <h3>{{ previewData?.name }}</h3>
      <p class="lottery-pre">{{ previewData?.description }}</p>
      <div class="lottery-preview-grid">
        <div
          v-for="p in previewData?.prizes"
          :key="p.id"
          class="lottery-preview-prize"
        >
          <img v-if="p.image" :src="p.image" alt="" /><b>{{ p.name }}</b
          ><span>概率 {{ (p.probability / 100).toFixed(2) }}%</span>
        </div>
      </div>
      <NButton type="primary" block class="mt-16px" @click="demo = true"
        >模擬抽獎</NButton
      ><NAlert v-if="demo" type="success" class="mt-12px"
        >演示結果：獲得“{{
          previewData?.prizes[0]?.name
        }}”。這裏僅展示效果，未真實發獎。</NAlert
      ></NModal
    >
    <NModal
      v-model:show="showGrant"
      preset="card"
      title="補發抽獎次數"
      class="lottery-small-modal"
      :mask-closable="!saving"
      ><p class="mb-12px">
        {{ grantActivity?.name }} ·
        {{
          grantActivity?.chance_mode === "daily"
            ? "補發次數當日有效"
            : "補發次數活動期間有效"
        }}
      </p>
      <NForm label-placement="top"
        ><NFormItem label="用户賬號" required
          ><NSelect
            v-model:value="grantUser"
            :options="userOptions"
            filterable
            remote
            placeholder="搜索用户名"
            @search="searchUsers" /></NFormItem
        ><NFormItem label="補發次數" required
          ><NInputNumber
            v-model:value="grantCount"
            :min="1"
            :max="1000"
            :precision="0" /></NFormItem
        ><NFormItem label="補發原因" required
          ><NInput
            v-model:value="grantRemark"
            type="textarea"
            :maxlength="500" /></NFormItem></NForm
      ><template #footer
        ><div class="flex justify-end gap-8px">
          <NButton :disabled="saving" @click="showGrant = false">取消</NButton
          ><NButton
            type="primary"
            :loading="saving"
            :disabled="!grantUser || !grantRemark.trim()"
            @click="grant"
            >確認補發</NButton
          >
        </div></template
      ></NModal
    >
    <NModal
      v-model:show="showHistory"
      preset="card"
      title="活動規則歷史"
      class="lottery-small-modal"
      ><div
        v-for="row in historyItems"
        :key="row.revision"
        class="lottery-prize-editor"
      >
        <b>版本 {{ row.revision }} · {{ lotteryState[row.config.status] }}</b>
        <p>{{ time(row.created_at) }} · 操作人 #{{ row.admin_id }}</p>
        <p>{{ row.config.name }} · {{ row.config.chance_count }} 次</p>
        <p v-for="p in row.config.prizes" :key="p.id">
          {{ p.name }}：{{ (p.probability / 100).toFixed(2) }}% /
          {{ p.quantity }} 份
        </p>
      </div>
      <NEmpty v-if="!historyItems.length" description="暫無規則歷史"
    /></NModal>
    <NModal
      :show="!!selectedDraw"
      preset="card"
      title="中獎記錄詳情"
      class="lottery-small-modal"
      @update:show="(v) => !v && (selectedDraw = null)"
      ><template v-if="selectedDraw"
        ><NDescriptions :column="1" bordered label-placement="left"
          ><NDescriptionsItem label="中獎編號">{{
            selectedDraw.draw_no
          }}</NDescriptionsItem
          ><NDescriptionsItem label="用户"
            >{{ selectedDraw.username }} #{{
              selectedDraw.user_id
            }}</NDescriptionsItem
          ><NDescriptionsItem label="活動 / 獎品"
            >{{ selectedDraw.activity_name }} /
            {{ selectedDraw.prize_name || "謝謝參與" }}</NDescriptionsItem
          ><NDescriptionsItem label="狀態">{{
            lotteryState[selectedDraw.status]
          }}</NDescriptionsItem
          ><NDescriptionsItem label="中獎時間">{{
            time(selectedDraw.created_at)
          }}</NDescriptionsItem
          ><NDescriptionsItem label="發放時間">{{
            time(selectedDraw.delivered_at)
          }}</NDescriptionsItem
          ><NDescriptionsItem label="獎品成本">{{
            formatMoney(selectedDraw.cost_cents || 0)
          }}</NDescriptionsItem
          ><NDescriptionsItem label="發放備註">{{
            selectedDraw.remark || "—"
          }}</NDescriptionsItem
          ><NDescriptionsItem label="發放操作人">{{
            selectedDraw.admin_id ? `#${selectedDraw.admin_id}` : "系統"
          }}</NDescriptionsItem></NDescriptions
        ><NButton
          v-if="selectedDraw.status !== 'missed' && checkAuth('lottery:reveal')"
          class="mt-16px"
          @click="reveal"
          >查看發放內容（記錄審計）</NButton
        >
        <pre v-if="drawContent" class="lottery-pre mt-12px">{{
          drawContent
        }}</pre>
        <template
          v-if="
            selectedDraw.mode === 'manual' &&
            selectedDraw.status === 'pending' &&
            checkAuth('lottery:deliver')
          "
          ><NAlert type="warning" class="mt-16px mb-12px"
            >請實際交付獎品後再確認，此操作不會自動給用户發送商品。</NAlert
          ><NInput
            v-model:value="deliveryRemark"
            placeholder="發放備註（可選）"
            :maxlength="500"
          /><NPopconfirm @positive-click="deliver"
            ><template #trigger
              ><NButton type="primary" class="mt-12px" :loading="saving"
                >確認已發放</NButton
              ></template
            >確認已經把獎品交付給此用户？</NPopconfirm
          ></template
        ></template
      ></NModal
    >
  </div>
</template>
<style>
.lottery-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin: 8px 0 16px;
  align-items: center;
}
.lottery-modal {
  width: min(960px, calc(100vw - 32px));
  max-height: 90vh;
  overflow: auto;
}
.lottery-small-modal {
  width: min(620px, calc(100vw - 32px));
  max-height: 90vh;
  overflow: auto;
}
.lottery-form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.lottery-prize-editor {
  padding: 18px;
  margin-bottom: 16px;
  border: 1px solid var(--n-border-color, #e5e7eb);
  border-radius: 10px;
}
.lottery-pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: break-word;
  padding: 12px;
  background: var(--n-color, #f5f6fa);
  border-radius: 8px;
}
.lottery-preview-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.lottery-preview-prize {
  display: flex;
  align-items: center;
  flex-direction: column;
  gap: 8px;
  padding: 16px;
  border: 1px solid #e5e7eb;
  border-radius: 10px;
}
.lottery-preview-prize img {
  width: 64px;
  height: 64px;
  object-fit: contain;
}
@media (max-width: 600px) {
  .lottery-form-grid {
    grid-template-columns: 1fr;
    gap: 0;
  }
  .lottery-prize-editor {
    padding: 12px;
  }
  .lottery-toolbar > * {
    max-width: 100%;
  }
}
</style>
