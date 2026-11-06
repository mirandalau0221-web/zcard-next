<script setup lang="ts">
// 貨幣管理（settings:currency_read / currency_write / currency_delete 超管專屬）。
import { onMounted, ref, h, computed, watch } from "vue";
import { NButton, NDataTable, NInput, NInputNumber, NModal, NForm, NFormItem, NPopconfirm, NRadio, NRadioGroup, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { listCurrencies, createCurrency, updateCurrency, deleteCurrency, fetchSettings, updateSetting } from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, getCurrency, initCurrency } from "@/utils/money";
import { formatCents } from "../../../../../packages/money/index";
import FilterTabs from "@/components/common/filter-tabs.vue";

defineOptions({ name: "CurrencyTab" });
const emit = defineEmits<{ updated: [] }>();

const loading = ref(false);
const currencies = ref<any[]>([]);
const showModal = ref(false);
const saving = ref(false);
// editing 非空 = 編輯已有貨幣（code 不可改）；空 = 新增。
const editing = ref<string | null>(null);
const form = ref({ code: "", symbol: "", position: "prefix", precision: 2, rate_json: "1" });

// 啓用狀態快捷篩選（客戶端過濾——貨幣全量加載，帶實時計數）
const enabledFilter = ref<"" | "on" | "off">("");
const enabledTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "已啓用", value: "on", type: "success" as const },
  { label: "已停用", value: "off", type: "default" as const },
];
const enabledCounts = computed(() => ({
  "": currencies.value.length,
  on: currencies.value.filter((c) => c.enabled).length,
  off: currencies.value.filter((c) => !c.enabled).length,
}));
const filteredCurrencies = computed(() =>
  enabledFilter.value === "" ? currencies.value : currencies.value.filter((c) => (enabledFilter.value === "on" ? c.enabled : !c.enabled)),
);

// 基礎貨幣就是默認結算貨幣；讀取失敗保持未知並提供重試。
const baseCurrency = ref("");
const baseLoadError = ref(false);
const settingDefault = ref("");
const isBaseCurrency = computed(() => form.value.code !== "" && form.value.code === baseCurrency.value);
const rateHint = computed(() =>
  isBaseCurrency.value
    ? form.value.code === "CNY"
      ? "全站統一按人民幣（CNY）結算，基礎貨幣匯率固定為 1"
      : `全站統一按基礎貨幣 ${form.value.code} 結算，匯率固定為 1`
    : `匯率 = 1 ${baseCurrency.value || "基礎貨幣"} 可兌換的本幣數量`,
);

// 展示效果預覽（符號 + 位置 + 小數位實時聯動，以 10 元為例）
const moneyPreview = computed(() => {
  const sym = form.value.symbol || getCurrency().symbol;
  const num = formatCents(1000, form.value.precision ?? 2);
  return form.value.position === "suffix" ? `${num}${sym}` : `${sym}${num}`;
});

const canWrite = () => checkAuth("settings:currency_write");

// 輸入基礎貨幣代碼時匯率自動固定為 1（與後端 settings.CURRENCY_BASE_RATE 約束一致）。
watch(
  () => form.value.code,
  (code) => {
    if (code && code === baseCurrency.value) form.value.rate_json = "1";
  },
);

async function loadBaseCurrency() {
  baseLoadError.value = false;
  try {
    const { data, error } = await fetchSettings("i18n");
    const it = ((data as any)?.items || []).find((x: any) => x.key === "base_currency");
    if (error || !it) throw new Error("讀取基礎貨幣失敗");
    const code = JSON.parse(it.value_json);
    if (typeof code !== "string" || !code) throw new Error("基礎貨幣配置無效");
    baseCurrency.value = code;
  } catch {
    baseCurrency.value = "";
    baseLoadError.value = true;
  }
}

async function setDefaultCurrency(row: any) {
  if (!row.enabled || !baseCurrency.value || settingDefault.value) return;
  settingDefault.value = row.code;
  try {
    const { error } = await updateSetting("i18n", "base_currency", JSON.stringify(row.code));
    if (!error) {
      await Promise.all([loadBaseCurrency(), initCurrency(true)]);
      await load();
      window.$message?.success(`默認結算貨幣已設為 ${row.code}`);
      emit("updated");
    }
  } finally {
    settingDefault.value = "";
  }
}

function openCreate() {
  editing.value = null;
  form.value = { code: "", symbol: "", position: "prefix", precision: 2, rate_json: "1" };
  showModal.value = true;
}

function openEdit(row: any) {
  editing.value = row.code;
  form.value = {
    code: row.code,
    symbol: row.symbol ?? "",
    position: row.position ?? "prefix",
    precision: row.precision ?? 0,
    rate_json: row.rate_json ?? "1",
  };
  showModal.value = true;
}

const columns: DataTableColumns<any> = [
  { title: "代碼", key: "code", width: 175, render: (row) => h("div", { class: "flex flex-wrap items-center gap-6px" }, [
    h("span", row.code),
    row.code === baseCurrency.value ? h(NTag, { size: "small", type: "info" }, { default: () => "默認結算" }) : null,
  ]) },
  { title: "符號", key: "symbol", width: 70 },
  { title: "符號位置", key: "position", width: 84 },
  { title: "小數位", key: "precision", width: 70, render: (row) => row.precision ?? 0 },
  { title: "匯率(JSON)", key: "rate_json", width: 140, ellipsis: true },
  { title: "排序", key: "sort", width: 56 },
  {
    title: "狀態",
    key: "enabled",
    width: 70,
    render: (row) => h(NTag, { size: "small", type: row.enabled ? "success" : "default" }, { default: () => (row.enabled ? "啓用" : "停用") }),
  },
  {
    title: "操作",
    key: "actions",
    width: 285,
    render: (row) =>
      h("div", { class: "flex flex-wrap gap-4px" }, [
        checkAuth("settings:update") && row.code !== baseCurrency.value
          ? h(NButton, { size: "tiny", disabled: !row.enabled || !baseCurrency.value || !!settingDefault.value, loading: settingDefault.value === row.code, onClick: () => setDefaultCurrency(row) }, { default: () => "設為默認結算" })
          : null,
        canWrite()
          ? h(NButton, { size: "tiny", onClick: () => openEdit(row) }, { default: () => "編輯" })
          : null,
        canWrite()
          ? h(NButton, { size: "tiny", disabled: !baseCurrency.value || row.code === baseCurrency.value, onClick: () => handleToggle(row) }, { default: () => (row.enabled ? "停用" : "啓用") })
          : null,
        checkAuth("settings:currency_delete")
          ? h(NPopconfirm, { onPositiveClick: () => handleDelete(row.code) }, { trigger: () => h(NButton, { size: "tiny", type: "error", quaternary: true, disabled: !baseCurrency.value || row.code === baseCurrency.value }, { default: () => "刪除" }), default: () => "無引用時才可刪除，確定？" })
          : null,
      ]),
  },
];

async function load() {
  loading.value = true;
  try {
    const { data, error } = await listCurrencies();
    if (!error && data) currencies.value = (data as any).currencies || [];
  } finally {
    loading.value = false;
  }
}

async function handleToggle(row: any) {
  const { error } = await updateCurrency(row.code, { enabled: !row.enabled });
  if (!error) {
    window.$message?.success(!row.enabled ? "已啓用" : "已停用");
    load();
    emit("updated");
  }
}

async function handleDelete(code: string) {
  const { error } = await deleteCurrency(code);
  if (!error) {
    window.$message?.success("已刪除");
    load();
    emit("updated");
  } else {
    window.$message?.error("刪除失敗（可能仍被引用）");
  }
}

async function handleSave() {
  if (!form.value.code || !form.value.symbol) return;
  saving.value = true;
  try {
    if (editing.value) {
      const { error } = await updateCurrency(editing.value, { ...form.value });
      if (!error) {
        await initCurrency(true);
        window.$message?.success("貨幣已更新");
        showModal.value = false;
        load();
        emit("updated");
      }
    } else {
      const { error } = await createCurrency({ ...form.value });
      if (!error) {
        window.$message?.success("貨幣已創建");
        showModal.value = false;
        load();
        emit("updated");
      }
    }
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  load();
  loadBaseCurrency();
});
</script>

<template>
  <div>
    <div class="mb-8px flex flex-wrap items-center justify-between gap-8px">
      <FilterTabs v-model:value="enabledFilter" :options="enabledTabs" :counts="enabledCounts" size="small" />
      <NButton v-if="canWrite()" size="small" type="primary" @click="openCreate">新增貨幣</NButton>
    </div>
    <div class="mb-8px rounded-4px bg-gray-50 px-10px py-6px text-12px text-gray-500 dark:bg-gray-800 dark:text-gray-400">
      <template v-if="baseCurrency">默認結算貨幣：<b>{{ baseCurrency }}</b>（基礎匯率為 1）。其他貨幣的匯率表示「1 {{ baseCurrency }} 可兌換的本幣數量」。已有商品或資金記錄時不能直接切換基礎貨幣。</template>
      <div v-else-if="baseLoadError" role="alert" class="flex items-center gap-8px text-red-600">默認結算貨幣讀取失敗<NButton size="tiny" @click="loadBaseCurrency">重試</NButton></div>
      <span v-else>正在讀取默認結算貨幣…</span>
    </div>
    <NDataTable :columns="columns" :data="filteredCurrencies" :loading="loading" size="small"  :max-height="540" />

    <NModal v-model:show="showModal" preset="dialog" :title="editing ? `編輯貨幣 ${editing}` : '新增貨幣'" style="width: 540px; max-width: 96vw">
      <NForm :model="form" label-placement="left" label-width="72">
        <!-- 等寬兩列柵格（大廠表單模式：代碼/符號、位置/小數位各佔一列，控件不再長短不齊） -->
        <div class="grid grid-cols-2 gap-x-16px">
          <NFormItem label="代碼" required>
            <NInput
              :value="form.code"
              :disabled="!!editing"
              :maxlength="3"
              placeholder="如 AUD"
              @update:value="(v: string) => (form.code = v.toUpperCase())"
            />
          </NFormItem>
          <NFormItem label="符號" required>
            <NInput v-model:value="form.symbol" :maxlength="8" placeholder="如 $" />
          </NFormItem>
          <NFormItem label="符號位置">
            <NRadioGroup v-model:value="form.position">
              <div class="flex items-center gap-16px whitespace-nowrap">
                <NRadio value="prefix">前綴 $10</NRadio>
                <NRadio value="suffix">後綴 10$</NRadio>
              </div>
            </NRadioGroup>
          </NFormItem>
          <NFormItem label="小數位">
            <NInputNumber v-model:value="form.precision" :min="0" :max="8" :precision="0" class="w-full" />
          </NFormItem>
        </div>
        <p class="mb-12px text-12px text-gray-500">小數位為顯示上限：設置 3～8 位時，自動省略兩位之後多餘的末尾零，例如 2.000000 顯示為 2.00、2.123400 顯示為 2.1234；設置 0 或 1 位時按所設位數顯示。此設置不改變價格、餘額或支付渠道單位；金額輸入以基礎貨幣 {{ currencyUnit() }} 為單位，最多精確到 0.01 {{ currencyUnit() }}。</p>
        <NFormItem label="匯率" required>
          <NInput v-model:value="form.rate_json" :disabled="isBaseCurrency" :placeholder="isBaseCurrency ? '基礎貨幣，固定為 1' : '如 0.14（1 基礎貨幣 = 0.14 本幣）'" />
        </NFormItem>
        <!-- 提示區：上下兩行（展示效果 / 匯率説明），避免左右兩塊基線錯亂 -->
        <div class="mb-4px rounded-4px bg-gray-50 px-10px py-8px text-12px text-gray-500 dark:bg-gray-800 dark:text-gray-400">
          <div class="flex items-center gap-8px">
            <span>展示效果（10 {{ form.code || "本幣" }}）</span>
            <b class="text-13px text-gray-900 dark:text-gray-100">{{ moneyPreview }}</b>
          </div>
          <div class="mt-2px leading-16px">{{ rateHint }}</div>
        </div>
      </NForm>
      <template #action>
        <NButton @click="showModal = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="handleSave">{{ editing ? "保存" : "創建" }}</NButton>
      </template>
    </NModal>
  </div>
</template>
