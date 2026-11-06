<script setup lang="ts">
// 秒殺 + 促銷管理（coupon:write 超管專屬；與卡密/購物車同鎖防超賣）。
import { onMounted, ref, h } from "vue";
import { NButton, NDataTable, NDatePicker, NInput, NInputNumber, NModal, NForm, NFormItem, NPopconfirm, NSelect } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchFlashSales, createFlashSale, deleteFlashSale, fetchPromotions, upsertPromotion } from "@/service/api";
import { checkAuth } from "@/directives";
import { currencyUnit, formatMoney } from "@/utils/money";

defineOptions({ name: "FlashTab" });

// ── 秒殺 ──
const flashLoading = ref(false);
const flashes = ref<any[]>([]);
const showFlash = ref(false);
const flashSaving = ref(false);
const flashForm = ref({ product_id: null as number | null, flash_priceYuan: 0, range: [Date.now(), Date.now() + 86400000] as [number, number], limit_qty: 10, per_user_limit: 1 });

const flashColumns: DataTableColumns<any> = [
  { title: "商品ID", key: "product_id", width: 76 },
  { title: "秒殺價", key: "flash_price", width: 90, render: (row) => formatMoney(row.flash_price) },
  { title: "開始", key: "start_at", width: 150, render: (row) => new Date(row.start_at * 1000).toLocaleString() },
  { title: "結束", key: "end_at", width: 150, render: (row) => new Date(row.end_at * 1000).toLocaleString() },
  { title: "限量", key: "limit_qty", width: 60 },
  { title: "已付款", key: "sold_qty", width: 76, render: (row) => row.sold_qty || 0 },
  { title: "待付款預佔", key: "reserved_qty", width: 96, render: (row) => row.reserved_qty || 0 },
  { title: "每人限購", key: "per_user_limit", width: 76 },
  {
    title: "操作",
    key: "actions",
    width: 70,
    render: (row) =>
      checkAuth("coupon:write")
        ? h(NPopconfirm, { onPositiveClick: () => handleDeleteFlash(row.id) }, { trigger: () => h(NButton, { size: "tiny", type: "error", quaternary: true }, { default: () => "刪除" }), default: () => "確定刪除該秒殺？" })
        : null,
  },
];

// ── 促銷 ──
const promoLoading = ref(false);
const promos = ref<any[]>([]);
const showPromo = ref(false);
const promoSaving = ref(false);
const scopeMode = ref('all');
const scopeIDs = ref('');
const scopeError = ref('');
function promotionScope(): string {
  if (scopeMode.value === 'all') return '{}';
  let scope: Record<string, unknown>;
  if (scopeMode.value === 'advanced') {
    scope = JSON.parse(promoForm.value.scope_json || '{}');
  } else {
    const values = scopeIDs.value.trim().split(/[\s,，、]+/);
    if (!values.length || values.some((v) => !/^\d+$/.test(v) || !Number.isSafeInteger(Number(v)) || Number(v) <= 0)) {
      throw new Error('請填寫有效的正整數編號，多個編號用逗號分隔，例如 318, 297');
    }
    scope = { [scopeMode.value]: [...new Set(values.map(Number))] };
  }
  if (!scope || Array.isArray(scope) || typeof scope !== 'object') throw new Error('範圍必須是 JSON 對象，例如 {"product_ids":[318,297]}');
  for (const [key, ids] of Object.entries(scope)) {
    if (!['product_ids', 'category_ids'].includes(key)) throw new Error('範圍僅支持 product_ids（商品）和 category_ids（分類）');
    if (!Array.isArray(ids) || !ids.length || ids.some((id) => !Number.isSafeInteger(id) || id <= 0)) throw new Error('編號列表不能為空，且只能填寫正整數；全場請使用 {}');
  }
  return JSON.stringify(scope);
}
function scopeText(raw?: string): string {
  try {
    const scope = JSON.parse(raw || '{}');
    const parts = [];
    if (scope.product_ids?.length) parts.push(`商品：${scope.product_ids.join('、')}`);
    if (scope.category_ids?.length) parts.push(`分類：${scope.category_ids.join('、')}`);
    return parts.join('；或 ') || '全場商品';
  } catch { return '範圍格式有誤'; }
}
const promoForm = ref({ name: "", scope_json: "", type: "percent", thresholdYuan: 0, discount: 9500, special_priceYuan: 0, range: [Date.now(), Date.now() + 86400000 * 7] as [number, number], enabled: true });

const promoColumns: DataTableColumns<any> = [
  { title: "名稱", key: "name", width: 140, ellipsis: { tooltip: true } },
  { title: "範圍", key: "scope_json", width: 160, ellipsis: true, render: (row) => scopeText(row.scope_json) },
  {
    title: "規則",
    key: "rule",
    width: 180,
    render: (row) =>
      row.type === "fixed"
        ? `滿 ${formatMoney(row.threshold)} 減 ${formatMoney(row.discount)}`
        : row.type === "percent"
          ? `滿 ${formatMoney(row.threshold)} 打 ${(row.discount / 1000).toFixed(2)}折`
          : `特價 ${formatMoney(row.special_price)}`,
  },
  { title: "開始", key: "start_at", width: 150, render: (row) => new Date(row.start_at * 1000).toLocaleString() },
  { title: "結束", key: "end_at", width: 150, render: (row) => new Date(row.end_at * 1000).toLocaleString() },
  {
    title: "狀態",
    key: "enabled",
    width: 70,
    render: (row) => (row.enabled ? "啓用" : "停用"),
  },
];

async function loadFlash() {
  flashLoading.value = true;
  try {
    const { data, error } = await fetchFlashSales();
    if (!error && data) flashes.value = (data as any).items || [];
  } finally {
    flashLoading.value = false;
  }
}

async function loadPromos() {
  promoLoading.value = true;
  try {
    const { data, error } = await fetchPromotions();
    if (!error && data) promos.value = (data as any).promotions || (data as any).items || [];
  } finally {
    promoLoading.value = false;
  }
}

async function handleFlash() {
  if (!flashForm.value.product_id) return;
  flashSaving.value = true;
  try {
    const { error } = await createFlashSale({
      product_id: flashForm.value.product_id,
      flash_price: Math.round(flashForm.value.flash_priceYuan * 100),
      start_at: Math.floor(flashForm.value.range[0] / 1000),
      end_at: Math.floor(flashForm.value.range[1] / 1000),
      limit_qty: flashForm.value.limit_qty,
      per_user_limit: flashForm.value.per_user_limit,
    });
    if (!error) {
      window.$message?.success("秒殺已創建（與卡密庫存同鎖防超賣）");
      showFlash.value = false;
      loadFlash();
    }
  } finally {
    flashSaving.value = false;
  }
}

async function handleDeleteFlash(id: number) {
  const { error } = await deleteFlashSale(id);
  if (!error) {
    window.$message?.success("已刪除");
    loadFlash();
  }
}

async function handlePromo() {
  if (!promoForm.value.name) return;
  scopeError.value = '';
  let scope: string;
  try { scope = promotionScope(); } catch (error) {
    scopeError.value = error instanceof SyntaxError ? 'JSON 格式不正確，請參考下方示例，使用英文雙引號和逗號' : (error as Error).message;
    return;
  }
  if (!promoForm.value.range || promoForm.value.range[1] <= promoForm.value.range[0]) {
    window.$message?.error('請選擇有效的開始和結束時間'); return;
  }
  promoSaving.value = true;
  try {
    const { error } = await upsertPromotion({
      name: promoForm.value.name,
      scope_json: scope,
      type: promoForm.value.type,
      threshold: Math.round(promoForm.value.thresholdYuan * 100),
      discount: promoForm.value.type === "percent" ? promoForm.value.discount : Math.round(promoForm.value.discount),
      special_price: Math.round(promoForm.value.special_priceYuan * 100),
      start_at: Math.floor(promoForm.value.range[0] / 1000),
      end_at: Math.floor(promoForm.value.range[1] / 1000),
      enabled: promoForm.value.enabled,
    });
    if (!error) {
      window.$message?.success("促銷已保存");
      showPromo.value = false;
      loadPromos();
    }
  } finally {
    promoSaving.value = false;
  }
}

onMounted(() => {
  loadFlash();
  loadPromos();
});
</script>

<template>
  <div class="flex flex-col gap-16px">
    <div>
      <div class="mb-8px flex items-center gap-8px">
        <span class="text-13px font-500">限時秒殺</span>
        <NButton v-if="checkAuth('coupon:write')" size="tiny" type="primary" @click="showFlash = true">新建秒殺</NButton>
      </div>
      <NDataTable :columns="flashColumns" :data="flashes" :loading="flashLoading" size="small"  :max-height="540" />
    </div>
    <div>
      <div class="mb-8px flex items-center gap-8px">
        <span class="text-13px font-500">滿減/折扣促銷（與會員折扣、券按管線順序疊加）</span>
        <NButton v-if="checkAuth('coupon:write')" size="tiny" type="primary" @click="showPromo = true">新建促銷</NButton>
      </div>
      <NDataTable :columns="promoColumns" :data="promos" :loading="promoLoading" size="small"  :max-height="540" />
    </div>

    <NModal v-model:show="showFlash" preset="dialog" title="新建秒殺" style="width: 480px">
      <NForm :model="flashForm" label-placement="left" label-width="88">
        <NFormItem label="商品ID" required>
          <NInputNumber v-model:value="flashForm.product_id" :min="1" class="w-full" />
        </NFormItem>
        <NFormItem :label="`秒殺價(${currencyUnit()})`" required>
          <NInputNumber v-model:value="flashForm.flash_priceYuan" :min="0.01" :precision="2" class="w-full" />
        </NFormItem>
        <NFormItem label="時間窗" required>
          <NDatePicker v-model:value="flashForm.range" type="datetimerange" clearable />
        </NFormItem>
        <NFormItem label="限量" required>
          <NInputNumber v-model:value="flashForm.limit_qty" :min="1" class="w-full" />
        </NFormItem>
        <NFormItem label="每人限購">
          <NInputNumber v-model:value="flashForm.per_user_limit" :min="1" class="w-full" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showFlash = false">取消</NButton>
        <NButton type="primary" :loading="flashSaving" @click="handleFlash">創建</NButton>
      </template>
    </NModal>

    <NModal v-model:show="showPromo" preset="dialog" title="新建促銷" style="width: min(620px, 94vw)">
      <NForm :model="promoForm" label-placement="left" label-width="88">
        <NFormItem label="名稱" required>
          <NInput v-model:value="promoForm.name" />
        </NFormItem>
        <NFormItem label="適用範圍">
          <NSelect v-model:value="scopeMode" :options="[{label:'全場商品',value:'all'}, {label:'指定商品',value:'product_ids'}, {label:'指定分類',value:'category_ids'}, {label:'高級 JSON',value:'advanced'}]" @update:value="scopeError = ''" />
        </NFormItem>
        <NFormItem v-if="scopeMode === 'product_ids' || scopeMode === 'category_ids'" :label="scopeMode === 'product_ids' ? '商品編號' : '分類編號'" :validation-status="scopeError ? 'error' : undefined" :feedback="scopeError">
          <div class="w-full">
            <NInput v-model:value="scopeIDs" placeholder="例如：318, 297（多個編號用逗號分隔）" />
            <p class="mt-8px text-13px">編號可在後台商品或分類列表的 ID 列查看。指定分類僅包含直接歸屬該分類的商品，子分類需另填編號。</p>
          </div>
        </NFormItem>
        <NFormItem v-if="scopeMode === 'advanced'" label="範圍 JSON" :validation-status="scopeError ? 'error' : undefined" :feedback="scopeError">
          <div class="w-full">
            <NInput v-model:value="promoForm.scope_json" type="textarea" :autosize="{minRows: 3, maxRows: 8}" placeholder='{"product_ids":[318,297]}' />
            <div class="mt-8px text-13px">
              <p>全場商品：<code>{}</code></p>
              <p>指定商品：<code>{"product_ids":[318,297]}</code></p>
              <p>指定分類：<code>{"category_ids":[3,5]}</code></p>
              <p>兩者可同時填寫，命中任一範圍即生效。數字為示例，請替換為實際 ID；分類不自動包含子分類。</p>
            </div>
          </div>
        </NFormItem>
        <NFormItem label="類型">
          <NSelect v-model:value="promoForm.type" :options="[{ label: '折扣（萬分比）', value: 'percent' }, { label: '滿減（分）', value: 'fixed' }, { label: '特價（分）', value: 'special_price' }]" />
        </NFormItem>
        <NFormItem :label="`單件門檻(${currencyUnit()})`">
          <div class="w-full">
            <NInputNumber v-model:value="promoForm.thresholdYuan" :min="0" class="w-full" />
            <p class="mt-8px text-13px">按會員價或階梯價計算後的單件商品金額判斷，不累計購物車金額，運費不計入門檻。</p>
          </div>
        </NFormItem>
        <NFormItem v-if="promoForm.type === 'percent'" label="折扣(萬分比)">
          <NInputNumber v-model:value="promoForm.discount" :min="1" :max="10000" class="w-full" />
        </NFormItem>
        <NFormItem v-else-if="promoForm.type === 'fixed'" :label="`減額(${currencyUnit()})`">
          <NInputNumber v-model:value="promoForm.special_priceYuan" :min="0" class="w-full" @update:value="(v: number | null) => (promoForm.discount = Math.round((v || 0) * 100))" />
        </NFormItem>
        <NFormItem v-else :label="`特價(${currencyUnit()})`">
          <NInputNumber v-model:value="promoForm.special_priceYuan" :min="0" class="w-full" />
        </NFormItem>
        <NFormItem label="時間窗" required>
          <NDatePicker v-model:value="promoForm.range" type="datetimerange" clearable />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showPromo = false">取消</NButton>
        <NButton type="primary" :loading="promoSaving" @click="handlePromo">保存</NButton>
      </template>
    </NModal>
  </div>
</template>
