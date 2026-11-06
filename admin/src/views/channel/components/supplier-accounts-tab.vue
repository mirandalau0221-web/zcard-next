<script setup lang="ts">
// 供貨賬號管理（supplier:read / supplier:write）：下游客戶賬戶 + 對接申請審核工作台。
// protocol 三選一：ZCard 自有協議 / dujiao-next 兼容 / acg-faka 兼容（ B/C）——
// 兼容賬號讓對應平台不改代碼即可對接本站；創建後展示「對接配置説明」。
// 前台個人中心提交的申請（owner_user_id>0）在「待審核」篩選下集中處理：
// 審核彈窗展示完整申請資料，「通過並開通」即生效（用戶前台可查憑據），駁回須填意見。
import { h, computed, onMounted, reactive, ref } from "vue";
import { useRoute } from "vue-router";
import {
  NButton, NDataTable, NDropdown, NInput, NInputNumber, NModal, NForm, NFormItem,
  NPopconfirm, NSelect, NSpace, NTag, NAlert, NDescriptions, NDescriptionsItem,
} from "naive-ui";
import type { DataTableColumns, DropdownOption } from "naive-ui";
import {
  fetchSupplierAccounts, createSupplierAccount, reviewSupplierAccount, toggleSupplierAccount,
  resetSupplierSecret, rechargeSupplierAccount, fetchSupplierLedger,
  fetchSupplierCallbacks, resendSupplierCallback,
  setSupplierIPWhitelist,
} from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, formatTransactionRemark, formatMoney, yuanToFen, fenToYuan } from "@/utils/money";
import SupplierPricingDialog from "./supplier-pricing-dialog.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";
import { useResponsiveTier, type TableTier } from "./use-responsive-tier";

defineOptions({ name: "SupplierAccountsTab" });

const route = useRoute();
const loading = ref(false);
const allAccounts = ref<any[]>([]);
const statusFilter = ref<"" | "applying" | "approved" | "rejected" | "disabled">(
  route.query.status === "applying" ? "applying" : "",
);

// 客戶端過濾（全量 ≤200 拉取，內存篩選）
const accounts = computed(() =>
  statusFilter.value === "" ? allAccounts.value : allAccounts.value.filter((r: any) => r.status === statusFilter.value),
);
// 待審核數（橫幅提醒——有待審申請時置頂提示，一鍵切到審核視圖）
const applyingCount = computed(() => allAccounts.value.filter((r: any) => r.status === "applying").length);

const statusTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "待審核", value: "applying", type: "warning" as const },
  { label: "已通過", value: "approved", type: "success" as const },
  { label: "已駁回", value: "rejected", type: "error" as const },
  { label: "已禁用", value: "disabled", type: "default" as const },
];

const protocolMeta: Record<string, { label: string; tag: "success" | "info" | "warning"; hint: string }> = {
  zcard: { label: "ZCard", tag: "success", hint: "對接另一套 ZCard：填本站地址 + api_key/api_secret（X-Supply-* 四頭簽名）" },
  dujiao_next: { label: "dujiao 兼容", tag: "info", hint: "對方 dujiao-next 後台「站點連接」填本站地址 + 下發的是 api_key/api_secret（Dujiao-Next-* 三頭）" },
  acg_faka: { label: "acg 兼容", tag: "warning", hint: "對方 acg-faka 後台「共享店鋪(異次元)」填本站地址 + 商戶ID=api_key、密鑰=api_secret" },
};

const canWrite = () => checkAuth("supplier:write");

// ── 容器寬分檔（full ≥1080 / mid ≥720 / compact）：任意屏寬下操作列完整可見，不依賴橫向滾動 ──
const wrapRef = ref<HTMLElement | null>(null);
const { tier } = useResponsiveTier(wrapRef);

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchSupplierAccounts({ page: 1, page_size: 200 });
    if (!error && data) {
      allAccounts.value = (data as any).accounts || [];
    }
  } finally {
    loading.value = false;
  }
}

// ── 新增 ──
const showForm = ref(false);
const saving = ref(false);
const form = reactive({
  name: "",
  protocol: "zcard",
  api_key: "",
  api_secret: "",
  display_name: "",
  contact: "",
});

function openCreate() {
  form.name = "";
  form.protocol = "zcard";
  form.api_key = "";
  form.api_secret = "";
  form.display_name = "";
  form.contact = "";
  showForm.value = true;
}

const secretOnce = ref(""); // 創建/重置時一次性回顯
async function submitForm() {
  if (!form.name || !form.api_key || !form.api_secret) {
    window.$message?.warning("名稱、api_key、api_secret 必填");
    return;
  }
  saving.value = true;
  try {
    const { data, error } = await createSupplierAccount({ ...form });
    if (!error && data) {
      secretOnce.value = (data as any).api_secret || "";
      window.$message?.success("賬號已創建（待審核後生效）");
      showForm.value = false;
      load();
    }
  } finally {
    saving.value = false;
  }
}

// ── 審核工作台（統一彈窗：申請資料 + 意見 + 通過並開通/駁回）──
const reviewModal = ref(false);
const reviewTarget = ref<any>(null);
const reviewNote = ref("");
const reviewSubmitting = ref(false);

function openReview(row: any) {
  reviewTarget.value = row;
  reviewNote.value = "";
  reviewModal.value = true;
}

function fmtTime(ts: number) {
  return ts ? new Date(ts * 1000).toLocaleString() : "-";
}

async function submitReview(approve: boolean) {
  if (!approve && !reviewNote.value.trim()) {
    window.$message?.warning("駁回時請填寫審核意見（將展示給申請人）");
    return;
  }
  reviewSubmitting.value = true;
  try {
    const { error } = await reviewSupplierAccount(reviewTarget.value.id, approve, reviewNote.value.trim() || undefined);
    if (!error) {
      window.$message?.success(approve ? "已通過並開通（申請人可在前台查看憑據）" : "已駁回（申請人可查看意見並重新申請）");
      reviewModal.value = false;
      load();
    }
  } finally {
    reviewSubmitting.value = false;
  }
}

async function handleToggle(row: any) {
  const { error } = await toggleSupplierAccount(row.id, row.status === "disabled");
  if (!error) {
    window.$message?.success(row.status === "disabled" ? "已啓用" : "已禁用");
    load();
  }
}

// ── 賬戶詳情（對接信息查看 + IP 白名單管理；api_key 僅在此展示）──
const detailModal = ref(false);
const detailTarget = ref<any>(null);
const detailWhitelist = ref<string[]>([]);
const detailWhitelistInput = ref("");
const detailWhitelistSaving = ref(false);

function openDetail(row: any) {
  detailTarget.value = row;
  detailWhitelist.value = [...(row.ip_whitelist || [])];
  detailWhitelistInput.value = "";
  detailModal.value = true;
}

function addDetailWhitelistIP() {
  const ip = detailWhitelistInput.value.trim();
  if (!ip) return;
  if (detailWhitelist.value.includes(ip)) {
    window.$message?.warning("該 IP 已在白名單中");
    return;
  }
  if (detailWhitelist.value.length >= 20) {
    window.$message?.warning("白名單最多 20 條");
    return;
  }
  detailWhitelist.value.push(ip);
  detailWhitelistInput.value = "";
}

async function saveDetailWhitelist() {
  detailWhitelistSaving.value = true;
  try {
    const { data, error } = await setSupplierIPWhitelist(detailTarget.value.id, detailWhitelist.value);
    if (!error) {
      detailWhitelist.value = [...((data as any)?.ip_whitelist || detailWhitelist.value)];
      window.$message?.success(detailWhitelist.value.length ? `白名單已更新（${detailWhitelist.value.length} 條）` : "白名單已清空（所有 IP 放行）");
      load();
    }
  } finally {
    detailWhitelistSaving.value = false;
  }
}

async function handleResetSecret(row: any) {
  const { data, error } = await resetSupplierSecret(row.id);
  if (!error && data) {
    secretOnce.value = (data as any).api_secret || "";
    window.$message?.success("密鑰已重置（舊密鑰立即失效）");
  }
}

// ── 充值 ──
const showRecharge = ref(false);
const rechargeTarget = ref<any>(null);
const rechargeYuan = ref<number | null>(null);
const rechargeRemark = ref("");
const rechargeReference = ref("");
const recharging = ref(false);
function openRecharge(row: any) {
  if (recharging.value) return;
  rechargeTarget.value = row;
  rechargeYuan.value = null;
  rechargeRemark.value = "";
  rechargeReference.value = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
  showRecharge.value = true;
}
async function submitRecharge() {
  if (recharging.value || !rechargeTarget.value) return;
  if (rechargeYuan.value === null || !Number.isFinite(rechargeYuan.value) || rechargeYuan.value <= 0) {
    window.$message?.warning(`請填寫大於零的充值金額（${currencyUnit()}）`);
    return;
  }
  let amountCents: number;
  try { amountCents = yuanToFen(rechargeYuan.value); }
  catch { window.$message?.warning("充值金額必須精確到分"); return; }
  if (!Number.isSafeInteger(amountCents) || amountCents <= 0 || amountCents > 1_000_000_000) {
    window.$message?.warning(`充值金額須在 0.01 至 10,000,000 ${currencyUnit()} 之間`);
    return;
  }
  recharging.value = true;
  try {
    const { error } = await rechargeSupplierAccount(
      rechargeTarget.value.id,
      amountCents,
      rechargeReference.value,
      rechargeRemark.value || "管理員充值",
    );
    if (!error) {
      window.$message?.success("已充值");
      showRecharge.value = false;
      load();
    }
  } finally {
    recharging.value = false;
  }
}

// ── 賬本抽屜 ──
const showLedger = ref(false);
const ledgerRows = ref<any[]>([]);
const ledgerTarget = ref<any>(null);
const ledgerLoading = ref(false);
async function openLedger(row: any) {
  ledgerTarget.value = row;
  showLedger.value = true;
  ledgerLoading.value = true;
  try {
    const { data, error } = await fetchSupplierLedger({ account_id: row.id, page: 1, page_size: 50 });
    if (!error && data) ledgerRows.value = (data as any).entries || [];
  } finally {
    ledgerLoading.value = false;
  }
}

// ── 響應式列集：compact 只留 名稱/狀態/操作；mid 去掉 ID/餘額/申請信息保留核心；full 全列 ──
// 操作原為 7~8 個平鋪按鈕（360px），收納為「詳情/審核/充值 + 更多▾」；compact 單「操作▾」下拉（功能不減）。
function nameCol(t: TableTier) {
  return {
    title: "名稱",
    key: "name",
    minWidth: t === "compact" ? 96 : 110,
    maxWidth: t === "compact" ? 200 : 260,
    render: (row: any) => {
      // 非全檔時把協議/餘額併入懸停提示
      const tip =
        t === "full"
          ? row.name
          : `${row.name}\n${protocolMeta[row.protocol]?.label || row.protocol} · 餘額 ${row.balance_cache == null || row.balance_cache < 0 ? "—" : formatMoney(row.balance_cache)}`;
      return h("span", { class: "truncate", title: tip }, row.name);
    },
  };
}

function protocolCol() {
  return {
    title: "協議",
    key: "protocol",
    width: 92,
    render: (row: any) => h(NTag, { size: "small", type: protocolMeta[row.protocol]?.tag || "default", bordered: false }, { default: () => protocolMeta[row.protocol]?.label || row.protocol }),
  };
}

function statusCol() {
  return {
    title: "狀態",
    key: "status",
    width: 78,
    render: (row: any) =>
      h(NTag, { size: "small", type: row.status === "approved" ? "success" : row.status === "applying" ? "warning" : row.status === "rejected" ? "error" : "default", bordered: false }, { default: () => ({ approved: "已通過", applying: "待審核", rejected: "已駁回", disabled: "已禁用" } as any)[row.status] || row.status }),
  };
}

function applyInfoCol() {
  return {
    title: "申請/審核信息",
    key: "apply_info",
    minWidth: 150,
    maxWidth: 420,
    ellipsis: { tooltip: true },
    render: (row: any) => {
      const parts: string[] = [];
      if (row.owner_user_id) parts.push(`申請人 #${row.owner_user_id}`);
      if (row.apply_reason) parts.push(`理由：${row.apply_reason}`);
      if (row.review_note) parts.push(`意見：${row.review_note}`);
      return parts.length ? parts.join(" ｜ ") : "-";
    },
  };
}

function confirmResetSecret(row: any) {
  window.$dialog?.warning({
    title: "重置密鑰",
    content: "重置後舊密鑰立即失效，確定？",
    positiveText: "重置",
    negativeText: "取消",
    onPositiveClick: () => handleResetSecret(row),
  });
}

function moreMenu(row: any): DropdownOption[] {
  const opts: DropdownOption[] = [];
  if (canWrite()) opts.push({ label: "充值", key: "recharge" });
  opts.push({ label: "賬本", key: "ledger" });
  if (canWrite()) opts.push({ label: "供貨定價", key: "price" }, { label: "重置密鑰", key: "reset" });
  if (row.status !== "applying" && canWrite())
    opts.push({ type: "divider", key: "dv" }, { label: row.status === "disabled" ? "啓用" : "禁用", key: "toggle" });
  return opts;
}

function compactMenu(row: any): DropdownOption[] {
  const opts: DropdownOption[] = [{ label: "詳情", key: "detail" }];
  if (row.status === "applying" && canWrite()) opts.push({ label: "審核", key: "review" });
  opts.push(...moreMenu(row));
  return opts;
}

function onRowAction(row: any, key: string) {
  switch (key) {
    case "detail":
      openDetail(row);
      break;
    case "review":
      openReview(row);
      break;
    case "recharge":
      openRecharge(row);
      break;
    case "ledger":
      openLedger(row);
      break;
    case "price":
      openPrice(row);
      break;
    case "reset":
      confirmResetSecret(row);
      break;
    case "toggle":
      handleToggle(row);
      break;
  }
}

function actionsCol(t: TableTier) {
  return {
    title: "操作",
    key: "actions",
    width: t === "compact" ? 92 : 200,
    render: (row: any) =>
      t === "compact"
        ? h(
            NDropdown,
            { options: compactMenu(row), trigger: "click", onSelect: (key: string) => onRowAction(row, key) },
            { default: () => h(NButton, { size: "tiny", secondary: true }, { default: () => "操作 ▾" }) },
          )
        : h("div", { class: "flex flex-wrap items-center gap-4px" }, [
            h(NButton, { size: "tiny", quaternary: true, onClick: () => openDetail(row) }, { default: () => "詳情" }),
            row.status === "applying" && canWrite()
              ? h(NButton, { size: "tiny", type: "warning", secondary: true, onClick: () => openReview(row) }, { default: () => "審核" })
              : null,
            canWrite()
              ? h(NButton, { size: "tiny", type: "primary", quaternary: true, onClick: () => openRecharge(row) }, { default: () => "充值" })
              : null,
            h(
              NDropdown,
              { options: moreMenu(row), trigger: "click", onSelect: (key: string) => onRowAction(row, key) },
              { default: () => h(NButton, { size: "tiny", quaternary: true }, { default: () => "更多 ▾" }) },
            ),
          ]),
  };
}

const columns = computed<DataTableColumns<any>>(() => {
  const t = tier.value;
  const cols: DataTableColumns<any> = [];
  if (t === "full") cols.push({ title: "ID", key: "id", width: 48 });
  cols.push(nameCol(t));
  if (t !== "compact") {
    cols.push(protocolCol());
    if (t === "full") cols.push({ title: "餘額", key: "balance_cache", width: 88, render: (row: any) => (row.balance_cache == null || row.balance_cache < 0 ? "—" : formatMoney(row.balance_cache)) });
  }
  cols.push(statusCol());
  if (t !== "compact") cols.push(applyInfoCol());
  cols.push(actionsCol(t));
  return cols;
});

// ── 回調記錄（死信重發）──
const showCallbacks = ref(false);
const callbacks = ref<any[]>([]);
const callbacksLoading = ref(false);
async function openCallbacks() {
  showCallbacks.value = true;
  callbacksLoading.value = true;
  try {
    const { data, error } = await fetchSupplierCallbacks({ page: 1, page_size: 50 });
    if (!error && data) callbacks.value = (data as any).callbacks || [];
  } finally {
    callbacksLoading.value = false;
  }
}
async function handleResend(row: any) {
  const { error } = await resendSupplierCallback(row.id);
  if (!error) {
    window.$message?.success("已重發");
    openCallbacks();
  }
}

const showPrice = ref(false);
const priceTarget = ref<any>(null);
function openPrice(row: any) { priceTarget.value = row; showPrice.value = true; }

onMounted(load);
</script>

<template>
  <div ref="wrapRef">
    <!-- 待審提醒（有待審申請時置頂；一鍵切到審核視圖） -->
    <NAlert v-if="applyingCount > 0 && statusFilter !== 'applying'" type="warning" :bordered="false" class="mb-12px">
      <template #default>
        <div class="flex items-center justify-between gap-8px">
          <span>⏳ 有 <b>{{ applyingCount }}</b> 個對接申請待審核（前台用戶提交）</span>
          <NButton size="tiny" type="warning" secondary @click="statusFilter = 'applying'">立即審核</NButton>
        </div>
      </template>
    </NAlert>

    <div class="mb-12px flex flex-wrap items-center justify-between gap-8px">
      <FilterTabs v-model:value="statusFilter" :options="statusTabs" size="small" />
      <NSpace>
        <NButton size="small" quaternary @click="openCallbacks">回調記錄</NButton>
        <NButton v-if="canWrite()" size="small" type="primary" @click="openCreate">新增供貨賬號</NButton>
      </NSpace>
    </div>

    <NDataTable :columns="columns" :data="accounts" :loading="loading" size="small" :row-key="(r: any) => r.id" :max-height="540" :scroll-x="300" />

    <!-- 密鑰一次性回顯 -->
    <NModal :show="!!secretOnce" preset="dialog" title="密鑰（僅此一次展示，請立即保存）" style="width: 520px; max-width: 96vw" @update:show="secretOnce = ''">
      <NAlert type="warning" :bordered="false" class="mb-8px">關閉後無法再次查看，丟失只能重置。</NAlert>
      <NInput :value="secretOnce" readonly type="textarea" :rows="2" />
    </NModal>

    <!-- 審核工作台：申請資料 + 意見 + 通過並開通 / 駁回 -->
    <NModal v-model:show="reviewModal" preset="card" :title="`對接申請審核 #${reviewTarget?.id || ''}`" style="width: 560px; max-width: 96vw">
      <NDescriptions v-if="reviewTarget" :column="2" size="small" bordered label-placement="left" class="mb-12px">
        <NDescriptionsItem label="站點名" :span="2">{{ reviewTarget.display_name || reviewTarget.name }}</NDescriptionsItem>
        <NDescriptionsItem label="對接協議">
          <NTag size="small" :type="protocolMeta[reviewTarget.protocol]?.tag || 'default'" :bordered="false">
            {{ protocolMeta[reviewTarget.protocol]?.label || reviewTarget.protocol }}
          </NTag>
        </NDescriptionsItem>
        <NDescriptionsItem label="申請人">用戶 #{{ reviewTarget.owner_user_id || '—（後台建號）' }}</NDescriptionsItem>
        <NDescriptionsItem label="聯繫方式">{{ reviewTarget.contact || '—' }}</NDescriptionsItem>
        <NDescriptionsItem label="申請時間">{{ fmtTime(reviewTarget.created_at) }}</NDescriptionsItem>
        <NDescriptionsItem label="申請理由" :span="2">{{ reviewTarget.apply_reason || '—（未填寫）' }}</NDescriptionsItem>
      </NDescriptions>
      <NAlert type="info" :bordered="false" class="mb-12px">
        {{ protocolMeta[reviewTarget?.protocol]?.hint }}
      </NAlert>
      <NForm label-placement="top">
        <NFormItem label="審核意見（駁回時必填；申請人可在前台查看）">
          <NInput v-model:value="reviewNote" type="textarea" :rows="3" placeholder="如：資料不完整、站點無法訪問等；通過時可留空" />
        </NFormItem>
      </NForm>
      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton @click="reviewModal = false">取消</NButton>
          <NButton type="error" secondary :loading="reviewSubmitting" @click="submitReview(false)">駁回</NButton>
          <NButton type="success" :loading="reviewSubmitting" @click="submitReview(true)">通過並開通</NButton>
        </div>
      </template>
    </NModal>

    <!-- 賬戶詳情：對接信息（api_key 僅在此展示）+ IP 白名單管理 -->
    <NModal v-model:show="detailModal" preset="card" :title="`對接賬戶詳情 #${detailTarget?.id || ''}`" style="width: 620px; max-width: 96vw">
      <NDescriptions v-if="detailTarget" :column="2" size="small" bordered label-placement="left" class="mb-12px">
        <NDescriptionsItem label="站點名" :span="2">{{ detailTarget.display_name || detailTarget.name }}</NDescriptionsItem>
        <NDescriptionsItem label="對接協議">
          <NTag size="small" :type="protocolMeta[detailTarget.protocol]?.tag || 'default'" :bordered="false">
            {{ protocolMeta[detailTarget.protocol]?.label || detailTarget.protocol }}
          </NTag>
        </NDescriptionsItem>
        <NDescriptionsItem label="狀態">
          <NTag size="small" :type="detailTarget.status === 'approved' ? 'success' : detailTarget.status === 'applying' ? 'warning' : detailTarget.status === 'rejected' ? 'error' : 'default'" :bordered="false">
            {{ ({ approved: "已通過", applying: "待審核", rejected: "已駁回", disabled: "已禁用" } as any)[detailTarget.status] || detailTarget.status }}
          </NTag>
        </NDescriptionsItem>
        <NDescriptionsItem label="app_id（api_key）" :span="2">
          <code class="text-13px">{{ detailTarget.api_key }}</code>
        </NDescriptionsItem>
        <NDescriptionsItem label="api_secret" :span="2">
          <span class="text-gray-400 text-13px">加密存儲，不可查看（僅創建/重置時明文下發一次）</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="申請人">用戶 #{{ detailTarget.owner_user_id || '—（後台建號）' }}</NDescriptionsItem>
        <NDescriptionsItem label="聯繫方式">{{ detailTarget.contact || '—' }}</NDescriptionsItem>
        <NDescriptionsItem label="餘額">{{ detailTarget.balance_cache == null || detailTarget.balance_cache < 0 ? "—" : formatMoney(detailTarget.balance_cache) }}</NDescriptionsItem>
        <NDescriptionsItem label="創建時間">{{ fmtTime(detailTarget.created_at) }}</NDescriptionsItem>
        <NDescriptionsItem label="回調地址" :span="2">{{ detailTarget.notify_url || '—（未配置）' }}</NDescriptionsItem>
        <NDescriptionsItem v-if="detailTarget.apply_reason" label="申請理由" :span="2">{{ detailTarget.apply_reason }}</NDescriptionsItem>
        <NDescriptionsItem v-if="detailTarget.review_note" label="審核意見" :span="2">{{ detailTarget.review_note }}</NDescriptionsItem>
      </NDescriptions>

      <!-- IP 白名單（空 = 所有 IP 放行；接口鑑權層強制） -->
      <NAlert type="info" :bordered="false" class="mb-8px">
        IP 白名單：不填 = 所有 IP 都可以請求本賬戶接口；填寫後僅白名單內 IP 可調用（精確 IP 或 CIDR 網段，最多 20 條）。
      </NAlert>
      <div v-if="detailWhitelist.length" class="flex flex-wrap gap-6px mb-8px">
        <NTag v-for="ip in detailWhitelist" :key="ip" closable size="small" @close="detailWhitelist = detailWhitelist.filter((x: string) => x !== ip)">
          {{ ip }}
        </NTag>
      </div>
      <div v-else class="text-13px text-gray-400 mb-8px">（白名單為空：所有 IP 放行）</div>
      <div class="flex gap-8px">
        <NInput v-model:value="detailWhitelistInput" placeholder="添加 IP 或網段，如 1.2.3.4 / 10.0.0.0/24" @keyup.enter="addDetailWhitelistIP" />
        <NButton :disabled="!canWrite()" @click="addDetailWhitelistIP">添加</NButton>
        <NButton type="primary" :loading="detailWhitelistSaving" :disabled="!canWrite()" @click="saveDetailWhitelist">保存白名單</NButton>
      </div>
    </NModal>

    <!-- 新增 -->
    <NModal v-model:show="showForm" preset="card" title="新增供貨賬號" style="width: 540px; max-width: 96vw">
      <NForm label-placement="left" label-width="110">
        <NFormItem label="名稱" required>
          <NInput v-model:value="form.name" placeholder="下游客戶標識" />
        </NFormItem>
        <NFormItem label="對接協議" required>
          <NSelect
            v-model:value="form.protocol"
            :options="[
              { label: 'ZCard（自有協議）', value: 'zcard' },
              { label: 'dujiao-next 兼容（對方不改代碼）', value: 'dujiao_next' },
              { label: 'acg-faka 兼容（對方不改代碼）', value: 'acg_faka' },
            ]"
          />
        </NFormItem>
        <NAlert type="info" :bordered="false" class="mb-12px">
          {{ protocolMeta[form.protocol]?.hint }}
        </NAlert>
        <NFormItem :label="form.protocol === 'acg_faka' ? '商戶ID(app_id)' : 'api_key'" required>
          <NInput v-model:value="form.api_key" :placeholder="form.protocol === 'acg_faka' ? '數字 ID（對方後台填的商戶ID）' : '隨機字符串'" />
        </NFormItem>
        <NFormItem :label="form.protocol === 'acg_faka' ? '密鑰(app_key)' : 'api_secret'" required>
          <NInput v-model:value="form.api_secret" placeholder="簽名密鑰" />
        </NFormItem>
        <NFormItem label="店鋪名(回顯)">
          <NInput v-model:value="form.display_name" placeholder="connect/ping 返回的 shopName/site_name" />
        </NFormItem>
        <NFormItem label="聯繫方式">
          <NInput v-model:value="form.contact" />
        </NFormItem>
      </NForm>
      <template #footer>
        <NButton size="small" class="mr-8px" @click="showForm = false">取消</NButton>
        <NButton size="small" type="primary" :loading="saving" @click="submitForm">創建</NButton>
      </template>
    </NModal>

    <!-- 充值 -->
    <NModal v-model:show="showRecharge" preset="dialog" :title="`充值：${rechargeTarget?.name || ''}`" :closable="!recharging" :mask-closable="!recharging" :close-on-esc="!recharging" style="width: 420px; max-width: 96vw">
      <NForm label-placement="top">
        <NFormItem label="當前餘額">
          <span>{{ fenToYuan(rechargeTarget?.balance_cache || 0) }} {{ currencyUnit() }}</span>
        </NFormItem>
        <NFormItem :label="`充值金額（${currencyUnit()}）`" required>
          <NInputNumber v-model:value="rechargeYuan" :min="0.01" :max="10_000_000" :precision="2" :disabled="recharging" class="w-full" />
        </NFormItem>
        <NFormItem label="備註">
          <NInput v-model:value="rechargeRemark" :disabled="recharging" placeholder="將寫入賬本" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton :disabled="recharging" @click="showRecharge = false">取消</NButton>
        <NButton type="primary" :loading="recharging" @click="submitRecharge">充值</NButton>
      </template>
    </NModal>

    <!-- 回調記錄 -->
    <NModal v-model:show="showCallbacks" preset="card" title="下游回調記錄（失敗可重發）" style="width: 760px; max-width: 96vw">
      <NDataTable
        :max-height="540"
        size="small"
        :scroll-x="580"
        :loading="callbacksLoading"
        :data="callbacks"
        :columns="[
          { title: 'ID', key: 'id', width: 60 },
          { title: '訂單', key: 'supply_order_id', width: 80 },
          { title: '狀態', key: 'callback_status', width: 90, render: (r: any) => r.callback_status },
          { title: '重試', key: 'retry_count', width: 60 },
          { title: '最近錯誤', key: 'last_error', minWidth: 200, ellipsis: true, render: (r: any) => r.last_error || '-' },
          {
            title: '操作',
            key: 'actions',
            width: 80,
            render: (r: any) =>
              r.callback_status !== 'success' && canWrite()
                ? h(NButton, { size: 'tiny', quaternary: true, onClick: () => handleResend(r) }, { default: () => '重發' })
                : null,
          },
        ]"
      />
    </NModal>

    <SupplierPricingDialog v-model:show="showPrice" :account="priceTarget" />

    <!-- 賬本 -->
    <NModal v-model:show="showLedger" preset="card" :title="`賬本：${ledgerTarget?.name || ''}`" style="width: 720px; max-width: 96vw">
      <NDataTable
        :max-height="540"
        size="small"
        :scroll-x="460"
        :loading="ledgerLoading"
        :data="ledgerRows"
        :columns="[
          { title: '時間', key: 'created_at', width: 160, render: (r: any) => (r.created_at ? new Date(r.created_at * 1000).toLocaleString() : '-') },
          { title: '類型', key: 'type', width: 110, render: (r: any) => ({ recharge: '充值', supply_pay: '供貨扣款', supply_refund: '退款', adjust: '調賬' } as any)[r.type] || r.type },
          { title: '金額', key: 'amount', width: 110, render: (r: any) => (r.amount >= 0 ? '+' : '') + fenToYuan(r.amount) },
          { title: '備註', key: 'remark', ellipsis: true, render: (r: any) => formatTransactionRemark(r.remark) || '-' },
        ]"
      />
    </NModal>
  </div>
</template>
