<script setup lang="ts">
/**
 * SKU 規格管理（大廠電商規格模式重寫，2026-08-17）：
 *   ① 規格定義區——動態「規格名：值1, 值2」行（如 時長：月卡,季卡,年卡）
 *   ② 生成組合——規格值笛卡爾積 → 未存在的組合自動補行（標記「新」）
 *   ③ 組合表格全行內編輯——售價/成本/庫存位單元格直接輸入（Enter/失焦暫存），
 *      新行逐條創建、已有行逐條更新、行內刪除；不再用獨立表單，杜絕溢出。
 */
import { ref, computed, watch, h } from "vue";
import { useRouter } from "vue-router";
import {
  NAlert,
  NButton,
  NTag,
  NSpace,
  NPopconfirm,
  NInput,
  NInputNumber,
  NEmpty,
  NSelect,
} from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { checkAuth } from "@/directives";
import { fetchSkus, createSku, updateSku, deleteSku } from "@/service/api";
import { centsToYuan, currencyUnit, yuanToFen } from "@/utils/money";

const props = defineProps<{ productId: number; readonly?: boolean; physical?: boolean; inventoryTracked?: boolean; basePhysicalStock?: number }>();
const router = useRouter();
const emit = defineEmits<{ (e: "persisted"): void }>();

interface SkuRow {
  physical_stock: number;
  original_physical_stock: number;
  fulfillment_mode: string;
  id: number; // 0 = 新行
  name: string;
  spec_values: Record<string, string>;
  price_yuan: number;
  cost_yuan: number;
  stock_offset: number;
  dirty: boolean;
}

const loading = ref(false);
const rows = ref<SkuRow[]>([]);
const saving = ref(false);
const definitionsDirty = ref(false);

// 規格定義（如 [{name:'時長', values:'月卡,季卡'}]）
const specs = ref<{ name: string; values: string }[]>([]);

const pendingRows = computed(() => rows.value.filter((r) => r.id === 0 || r.dirty));
const baseStockBlocksFirstSku = computed(() =>
  props.physical && (props.basePhysicalStock ?? 0) > 0 && !rows.value.some((r) => r.id > 0),
);

function specKey(spec: Record<string, string>) {
  return Object.entries(spec || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}:${v}`)
    .join("|");
}

async function load() {
  if (!props.productId) return;
  loading.value = true;
  try {
    const { data, error } = await fetchSkus(props.productId);
    if (!error && data) {
      rows.value = ((data as any).skus || []).map((s: any) => ({
        id: s.id,
        name: s.name,
        spec_values: s.spec_values || {},
        price_yuan: s.price_cents ? Number(centsToYuan(s.price_cents)) : 0,
        cost_yuan: s.cost_cents ? Number(centsToYuan(s.cost_cents)) : 0,
        physical_stock: Number(s.physical_stock || 0),
        original_physical_stock: Number(s.physical_stock || 0),
        stock_offset: s.stock_offset || 0,
        fulfillment_mode: s.fulfillment_mode || "follow",
        dirty: false,
      }));
      // 規格定義從已有 SKU 反推（繼續加值/加維度）
      const dims = new Map<string, Set<string>>();
      for (const r of rows.value) {
        for (const [k, v] of Object.entries(r.spec_values)) {
          if (!dims.has(k)) dims.set(k, new Set());
          dims.get(k)!.add(v);
        }
      }
      specs.value = [...dims.entries()].map(([name, set]) => ({
        name,
        values: [...set].join(","),
      }));
    }
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.productId,
  (v) => {
    if (v) {
      rows.value = [];
      specs.value = [];
      load();
    }
  },
  { immediate: true },
);

function addSpec() {
  definitionsDirty.value = true;
  specs.value.push({ name: "", values: "" });
}

function removeSpec(i: number) {
  definitionsDirty.value = true;
  specs.value.splice(i, 1);
}

// 生成組合：笛卡爾積 → 跳過已存在（按 spec_values 判重），新行標記 id=0
function generate(quiet = false) {
  const dims: { name: string; values: string[] }[] = specs.value
    .map((s) => ({
      name: s.name.trim(),
      values: s.values
        .split(/[,，]/)
        .map((v) => v.trim())
        .filter(Boolean),
    }))
    .filter((d) => d.name && d.values.length);
  if (!dims.length) {
    window.$message?.warning("請先填寫規格名與規格值（如 時長：月卡,季卡）");
    return;
  }
  const existing = new Set(rows.value.map((r) => specKey(r.spec_values)));
  let combos: Record<string, string>[] = [{}];
  for (const d of dims) {
    const next: Record<string, string>[] = [];
    for (const base of combos) {
      for (const v of d.values) next.push({ ...base, [d.name]: v });
    }
    combos = next;
  }
  let added = 0;
  for (const combo of combos) {
    if (existing.has(specKey(combo))) continue;
    existing.add(specKey(combo));
    rows.value.push({
      id: 0,
      name: Object.values(combo).join(" · "),
      spec_values: combo,
      price_yuan: 0,
      cost_yuan: 0,
      physical_stock: 0,
      original_physical_stock: 0,
      stock_offset: 0,
      fulfillment_mode: "follow",
      dirty: true,
    });
    added++;
  }
  definitionsDirty.value = false;
  if (!quiet && !added) window.$message?.info("組合均已存在，無新增");
  else if (!quiet) window.$message?.success(`已生成 ${added} 個新組合，填寫價格後保存`);
}

// Parent and row buttons share the same persistence path; failed rows stay dirty.
async function persistRow(row: SkuRow): Promise<boolean> {
  if (!row.name.trim()) {
    window.$message?.warning("規格名不能為空");
    return false;
  }
  const payload = {
    ...(props.physical && props.inventoryTracked !== false
      ? { physical_stock: row.physical_stock, expected_physical_stock: row.original_physical_stock }
      : {}),
    name: row.name.trim(),
    spec_values: row.spec_values,
    price_cents: yuanToFen(row.price_yuan || 0),
    cost_cents: yuanToFen(row.cost_yuan || 0),
    stock_offset: props.physical ? 0 : row.stock_offset || 0,
    fulfillment_mode: props.physical ? "follow" : row.fulfillment_mode,
  };
  const { data, error } = row.id
    ? await updateSku(row.id, payload)
    : await createSku(props.productId, payload);
  if (error) return false;
  if (!row.id) {
    if (!(data as any)?.id) {
      window.$message?.error("規格保存結果不完整，請刷新核對");
      return false;
    }
    row.id = Number((data as any).id);
  }
  row.original_physical_stock = row.physical_stock;
  row.dirty = false;
  emit("persisted");
  return true;
}
async function saveRow(row: SkuRow) {
  if (saving.value) return;
  saving.value = true;
  try {
    if (await persistRow(row)) window.$message?.success("規格已保存");
  } finally {
    saving.value = false;
  }
}
async function savePending(): Promise<boolean> {
  if (saving.value) return false;
  // A populated definition is also a draft: the main Save button persists its combinations.
  if (definitionsDirty.value) {
    if (specs.value.some((s) => !s.name.trim() || !s.values.trim())) {
      window.$message?.warning("請補全規格名和規格值");
      return false;
    }
    generate(true);
  }
  if (!pendingRows.value.length) return true;
  saving.value = true;
  try {
    for (const row of [...pendingRows.value]) if (!(await persistRow(row))) return false;
    return true;
  } finally {
    saving.value = false;
  }
}
async function saveAll() {
  if (await savePending()) window.$message?.success("規格已全部保存");
}
const hasPending = computed(() => definitionsDirty.value || pendingRows.value.length > 0);
// A separately saved delivery policy updates only that field, preserving price/name drafts.
async function refreshDeliveryModes() {
  const { data, error } = await fetchSkus(props.productId);
  if (!error && data) {
    const fresh = new Map(
      ((data as any).skus || []).map((s: any) => [Number(s.id), s.fulfillment_mode || "follow"]),
    );
    for (const row of rows.value) {
      if (fresh.has(Number(row.id))) row.fulfillment_mode = String(fresh.get(Number(row.id)));
    }
  }
}
defineExpose({ savePending, hasPending, saving, refreshDeliveryModes });

async function handleDelete(row: SkuRow) {
  if (saving.value) return;
  saving.value = true;
  try {
    if (row.id) {
      const { error } = await deleteSku(row.id);
      if (error) return;
      emit("persisted");
    }
    rows.value = rows.value.filter((r) => r !== row);
    window.$message?.success("已刪除");
  } finally {
    saving.value = false;
  }
}

// 行內數字單元格（Enter/失焦暫存並標髒；寬度自適應列寬）
function numCell(
  row: SkuRow,
  field: "price_yuan" | "cost_yuan" | "stock_offset" | "physical_stock",
  placeholder: string,
) {
  return h(NInputNumber, {
    disabled: props.readonly,
    value: row[field],
    size: "small",
    min: 0,
    precision: ["stock_offset", "physical_stock"].includes(field) ? 0 : 2,
    placeholder,
    showButton: false,
    style: "width: 100%",
    onUpdateValue: (v: number | null) => {
      row[field] = v || 0;
      row.dirty = true;
    },
  });
}

const allColumns: DataTableColumns<SkuRow> = [
  {
    title: "規格組合",
    key: "spec_values",
    minWidth: 150,
    render: (row) =>
      h(
        NSpace,
        { size: 4 },
        {
          default: () =>
            Object.entries(row.spec_values || {}).length
              ? Object.entries(row.spec_values).map(([k, v]) =>
                  h(NTag, { size: "small", bordered: false }, { default: () => `${k}:${v}` }),
                )
              : h("span", { class: "text-gray-400" }, "單規格"),
        },
      ),
  },
  {
    title: "規格名",
    key: "name",
    minWidth: 110,
    render: (row) =>
      h(NInput, {
        disabled: props.readonly,
        value: row.name,
        size: "small",
        placeholder: "如：月卡",
        onUpdateValue: (v: string) => {
          row.name = v;
          row.dirty = true;
        },
      }),
  },
  {
    title: "交付方式",
    key: "fulfillment_mode",
    width: 135,
    render: (row) =>
      h(NSelect, {
        disabled:
          props.readonly || props.physical || ["reuse", "local"].includes(row.fulfillment_mode),
        value: row.fulfillment_mode,
        options: [
          { label: "我的卡密（發貨設置）", value: "local", disabled: true },
          { label: "重複發貨（發貨設置）", value: "reuse", disabled: true },
          { label: "跟隨商品", value: "follow" },
          { label: "自動", value: "auto" },
          { label: "人工", value: "manual" },
        ],
        onUpdateValue: (v: string) => {
          row.fulfillment_mode = v;
          row.dirty = true;
        },
      }),
  },
  {
    title: () => `售價(${currencyUnit()})`,
    key: "price",
    width: 104,
    // 售價 0 = 繼承商品價（列頭過窄截斷，口徑移入 placeholder 提示）
    render: (row) => numCell(row, "price_yuan", "0=繼承"),
  },
  {
    title: () => `成本(${currencyUnit()})`,
    key: "cost",
    width: 96,
    render: (row) => numCell(row, "cost_yuan", "成本"),
  },
  {
    title: props.physical ? "可售庫存" : "庫存位",
    key: "stock_offset",
    width: 80,
    render: (row) => numCell(row, props.physical ? "physical_stock" : "stock_offset", "0"),
  },
  {
    title: "狀態",
    key: "state",
    width: 52,
    render: (row) =>
      row.id === 0
        ? h(NTag, { size: "small", type: "info" }, { default: () => "新" })
        : row.dirty
          ? h(NTag, { size: "small", type: "warning" }, { default: () => "改" })
          : h(NTag, { size: "small", type: "success", bordered: false }, { default: () => "存" }),
  },
  {
    title: "操作",
    key: "actions",
    width: 215,
    render: (row) =>
      h(
        NSpace,
        { size: "small" },
        {
          default: () => [
            !props.physical && row.id > 0 && checkAuth("inventory:import")
              ? h(NButton, { size: "small", disabled: props.readonly || row.dirty || saving.value, onClick: () => router.push({ name: "inventory", query: { product_id: props.productId, sku_id: row.id, open_import: "1" } }) }, { default: () => "導入卡密" })
              : null,
            !props.readonly && checkAuth("catalog:sku_write")
              ? h(
                  NButton,
                  {
                    size: "small",
                    type: "primary",
                    ghost: true,
                    disabled: !row.dirty,
                    loading: saving.value,
                    onClick: () => saveRow(row),
                  },
                  { default: () => "保存" },
                )
              : null,
            !props.readonly && checkAuth("catalog:sku_write")
              ? h(
                  NPopconfirm,
                  { onPositiveClick: () => handleDelete(row) },
                  {
                    trigger: () =>
                      h(
                        NButton,
                        { size: "small", type: "error", quaternary: true },
                        { default: () => "刪除" },
                      ),
                    default: () => "確定刪除該規格？",
                  },
                )
              : null,
          ],
        },
      ),
  },
];
const columns = computed(() => allColumns.filter((column) => !("key" in column) || (
    (column.key !== "fulfillment_mode" || !props.physical) &&
    (column.key !== "stock_offset" || !props.physical || props.inventoryTracked !== false)
)));
</script>

<template>
  <div>
    <!-- ① 規格定義區（頂部標籤對齊控件面板風格） -->
    <NCard size="small" class="mb-12px" title="規格設置">
      <template #header-extra>
        <NButton
          :disabled="props.readonly || loading || baseStockBlocksFirstSku"
          v-auth="'catalog:sku_write'"
          size="tiny"
          quaternary
          type="primary"
          @click="addSpec"
          >+ 添加規格</NButton
        >
      </template>
      <NAlert v-if="!loading && baseStockBlocksFirstSku" type="warning" class="mb-8px">
        <template v-if="props.inventoryTracked === false">請在「基礎信息」開啓庫存管理，將商品級可售庫存設為 0 後保存商品，再添加規格。</template>
        <template v-else>請在「基礎信息」將商品級可售庫存設為 0 並保存商品，再添加規格和分配庫存。</template>
      </NAlert>
      <NEmpty
        v-else-if="!specs.length"
        size="small"
        description="尚無規格維度——添加「規格名 + 規格值」後生成組合，如 時長：月卡,季卡,年卡"
      />
      <div v-for="(spec, i) in specs" :key="i" class="mb-8px flex flex-wrap items-center gap-8px">
        <span class="text-12px text-gray-400">規格{{ i + 1 }}</span>
        <NInput
          :disabled="props.readonly"
          v-model:value="spec.name"
          @update:value="definitionsDirty = true"
          size="small"
          placeholder="規格名（如 時長）"
          class="w-130px shrink-0"
        />
        <NInput
          :disabled="props.readonly"
          v-model:value="spec.values"
          @update:value="definitionsDirty = true"
          size="small"
          placeholder="規格值，逗號分隔（如 月卡,季卡,年卡）"
          class="min-w-220px flex-1"
        />
        <NButton
          :disabled="props.readonly"
          v-auth="'catalog:sku_write'"
          size="small"
          quaternary
          type="error"
          @click="removeSpec(i)"
          >刪除</NButton
        >
      </div>
      <div class="mt-8px flex flex-wrap items-center gap-8px">
        <NButton
          :disabled="props.readonly || loading || baseStockBlocksFirstSku"
          v-auth="'catalog:sku_write'"
          size="small"
          type="primary"
          ghost
          @click="generate(false)"
          >生成規格組合</NButton
        >
        <NButton
          v-auth="'catalog:sku_write'"
          size="small"
          type="primary"
          :disabled="props.readonly || (!pendingRows.length && !definitionsDirty)"
          :loading="saving"
          @click="saveAll"
        >
          保存全部規格{{ pendingRows.length ? `（${pendingRows.length}）` : "" }}
        </NButton>
        <span class="text-12px text-gray-400">售價填 0 = 繼承商品價；已有組合自動跳過</span>
      </div>
    </NCard>

    <!-- ② 組合表格（行內編輯；scroll-x 兜底不截斷） -->
    <NDataTable
      :max-height="540"
      :columns="columns"
      :data="rows"
      :loading="loading"
      :bordered="false"
      size="small"
      :scroll-x="700"
    />
    <NEmpty
      v-if="!rows.length && !loading"
      size="small"
      class="mt-16px"
      description="暫無規格組合——定義規格後點擊「生成規格組合」"
    />
  </div>
</template>
