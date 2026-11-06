<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from "vue";
import { onBeforeRouteLeave } from "vue-router";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NInput,
  NPagination,
  NSpin,
  NTag,
} from "naive-ui";
import {
  fetchCategoryPlacements,
  saveCategoryPlacements,
  fetchProducts,
  type CategoryPlacementRow,
  type PlacementProduct,
} from "@/service/api/catalog";
import { checkAuth } from "@/directives";
import { formatMoney } from "@/utils/money";

const props = defineProps<{
  show: boolean;
  categoryId: number;
  categoryName: string;
  categories?: { id: number; name: string; parent_id?: number }[];
}>();
const emit = defineEmits<{ (e: "update:show", v: boolean): void; (e: "saved"): void }>();
const rows = ref<CategoryPlacementRow[]>([]);
const candidates = ref<PlacementProduct[]>([]);
const version = ref(0),
  initial = ref(""),
  loading = ref(false),
  searching = ref(false),
  saving = ref(false);
const keyword = ref(""),
  page = ref(1),
  total = ref(0),
  errorText = ref(""),
  searchError = ref("");
const loaded = ref(false),
  saved = ref(false),
  dragIndex = ref<number | null>(null);
const canWrite = computed(() => checkAuth("catalog:category_write"));
const payload = computed(() => rows.value.map((r) => r.placement));
const dirty = computed(() => loaded.value && JSON.stringify(payload.value) !== initial.value);
const selected = computed(() => new Set(rows.value.map((r) => r.placement.product_id)));
const pinnedCount = computed(() => rows.value.filter((r) => r.placement.is_pinned).length);
let generation = 0,
  searchSequence = 0;
function categoryPath(id?: number) {
  const parts: string[] = [];
  const seen = new Set<number>();
  let node = props.categories?.find((c) => c.id === id);
  while (node && !seen.has(node.id)) {
    parts.unshift(node.name);
    seen.add(node.id);
    node = props.categories?.find((c) => c.id === node!.parent_id);
  }
  return parts.join(" / ") || "未分類";
}
function accept(data: { version: number; items: CategoryPlacementRow[] }) {
  version.value = Number(data.version || 0);
  rows.value = (data.items || []).map((row) => ({
    ...row,
    placement: {
      product_id: row.placement.product_id,
      is_pinned: !!row.placement.is_pinned,
      is_recommended: !!row.placement.is_recommended,
      position: row.placement.position || 0,
    },
  }));
  initial.value = JSON.stringify(payload.value);
  loaded.value = true;
}
async function load() {
  const seq = ++generation;
  loading.value = true;
  errorText.value = "";
  try {
    const { data, error } = await fetchCategoryPlacements(props.categoryId);
    if (seq !== generation || !props.show) return;
    if (error || !data) {
      errorText.value = "無法讀取分類設置，請重試。";
      return;
    }
    accept(data);
  } finally {
    if (seq === generation) loading.value = false;
  }
}
async function search(reset = true) {
  if (reset) page.value = 1;
  const seq = ++searchSequence;
  searching.value = true;
  searchError.value = "";
  try {
    const { data, error } = await fetchProducts({
      category_id: props.categoryId,
      keyword: keyword.value || undefined,
      options_only: true,
      page: page.value,
      page_size: 8,
    });
    if (seq !== searchSequence || !props.show) return;
    if (error || !data) {
      searchError.value = "商品加載失敗，請重試。";
      return;
    }
    candidates.value = (data as any).products || [];
    total.value = (data as any).total || 0;
  } finally {
    if (seq === searchSequence) searching.value = false;
  }
}
watch(
  () => [props.show, props.categoryId],
  () => {
    generation++;
    searchSequence++;
    if (!props.show || !props.categoryId) return;
    loaded.value = false;
    rows.value = [];
    candidates.value = [];
    keyword.value = "";
    saved.value = false;
    initial.value = "";
    load();
    search();
  },
  { immediate: true },
);
function add(p: PlacementProduct) {
  if (!loaded.value || saving.value || p.is_locked || selected.value.has(p.id) || !canWrite.value)
    return;
  const position =
    Math.max(
      -1,
      ...rows.value.filter((r) => r.placement.is_pinned).map((r) => r.placement.position),
    ) + 1;
  rows.value.push({
    product: p,
    placement: { product_id: p.id, is_pinned: true, is_recommended: true, position },
    unavailable_reason: p.status !== 1 ? "商品未上架，恢復上架後展示" : "",
  });
  rows.value.sort(
    (a, b) =>
      Number(b.placement.is_pinned) - Number(a.placement.is_pinned) ||
      a.placement.position - b.placement.position,
  );
  saved.value = false;
}
function remove(index: number) {
  if (rows.value[index].product.is_locked || saving.value) return;
  rows.value.splice(index, 1);
  saved.value = false;
}
function toggle(index: number, field: "is_pinned" | "is_recommended", value: boolean) {
  const row = rows.value[index];
  if (row.product.is_locked || saving.value) return;
  row.placement[field] = value;
  if (!row.placement.is_pinned && !row.placement.is_recommended) {
    rows.value.splice(index, 1);
    return;
  }
  if (field === "is_pinned" && value)
    row.placement.position =
      Math.max(
        -1,
        ...rows.value
          .filter((r) => r !== row && r.placement.is_pinned)
          .map((r) => r.placement.position),
      ) + 1;
  rows.value.sort(
    (a, b) =>
      Number(b.placement.is_pinned) - Number(a.placement.is_pinned) ||
      a.placement.position - b.placement.position,
  );
  saved.value = false;
}
function move(from: number, to: number) {
  if (saving.value || from === to || to < 0 || to >= pinnedCount.value || !canWrite.value) return;
  const lo = Math.min(from, to),
    hi = Math.max(from, to);
  if (rows.value.slice(lo, hi + 1).some((r) => r.product.is_locked)) {
    window.$message?.info("此調整會改變鎖定商品的順序，請先解鎖該商品");
    return;
  }
  // Reuse existing rank slots: unrelated locked rows retain their positions.
  const slots = rows.value
    .slice(lo, hi + 1)
    .map((r) => r.placement.position)
    .sort((a, b) => a - b);
  const [row] = rows.value.splice(from, 1);
  rows.value.splice(to, 0, row);
  rows.value.slice(lo, hi + 1).forEach((r, i) => (r.placement.position = slots[i]));
  saved.value = false;
}
function drop(index: number) {
  if (dragIndex.value !== null) move(dragIndex.value, index);
  dragIndex.value = null;
}
function close() {
  if (saving.value) return;
  if (dirty.value) {
    window.$dialog?.warning({
      title: "放棄未保存的設置？",
      content: "本次調整尚未生效，關閉將放棄這些修改。",
      positiveText: "放棄修改",
      negativeText: "繼續編輯",
      onPositiveClick: () => emit("update:show", false),
    });
    return;
  }
  emit("update:show", false);
}
async function save() {
  if (!dirty.value || saving.value || !canWrite.value) return;
  saving.value = true;
  errorText.value = "";
  try {
    const { data, error } = await saveCategoryPlacements(
      props.categoryId,
      version.value,
      payload.value,
    );
    if (error || !data) {
      errorText.value =
        (error as any)?.response?.data?.message ||
        "保存未成功，當前選擇和順序已保留。請檢查網絡後重試。";
      return;
    }
    accept(data);
    saved.value = true;
    emit("saved");
    window.$message?.success(`已保存「${props.categoryName}」的置頂與推薦`);
  } finally {
    saving.value = false;
  }
}
function reload() {
  if (dirty.value) {
    window.$dialog?.warning({
      title: "重新加載最新設置？",
      content: "重新加載將放棄當前草稿，讀取其他管理員保存的最新設置。",
      positiveText: "重新加載",
      negativeText: "保留草稿",
      onPositiveClick: load,
    });
  } else load();
}
onBeforeRouteLeave(() => {
  if (!props.show || (!dirty.value && !saving.value)) return true;
  if (saving.value) return false;
  return new Promise<boolean>((resolve) => {
    window.$dialog?.warning({
      title: "離開並放棄修改？",
      content: "當前分類設置尚未保存。",
      positiveText: "放棄並離開",
      negativeText: "繼續編輯",
      onPositiveClick: () => resolve(true),
      onNegativeClick: () => resolve(false),
      onClose: () => resolve(false),
      onMaskClick: () => resolve(false),
    });
  });
});
function beforeUnload(e: BeforeUnloadEvent) {
  if (props.show && (dirty.value || saving.value)) {
    e.preventDefault();
    e.returnValue = "";
  }
}
window.addEventListener("beforeunload", beforeUnload);
onBeforeUnmount(() => {
  generation++;
  searchSequence++;
  window.removeEventListener("beforeunload", beforeUnload);
});
</script>

<template>
  <NDrawer
    :show="show"
    width="min(760px, 100vw)"
    :mask-closable="!saving"
    :close-on-esc="!saving"
    @update:show="!$event && close()"
  >
    <NDrawerContent title="置頂與推薦" :closable="!saving" class="category-placements">
      <div class="placement-context">
        <strong>當前分類：{{ categoryName }}</strong>
        <p>可選擇本分類及全部子分類的商品；設置僅在此分類生效。</p>
        <p>置頂隻影響商城的綜合排序；推薦標籤與首頁推薦獨立。</p>
      </div>
      <NAlert v-if="errorText" type="error" class="mb-12px" role="alert"
        >{{ errorText }} <NButton text @click="reload">重新加載</NButton></NAlert
      >
      <NAlert v-if="saved" type="success" class="mb-12px"
        >設置已保存。未上架或分類隱藏的商品暫不展示。<a
          :href="`/?category_id=${categoryId}&sort=default`"
          target="_blank"
          rel="noopener noreferrer"
          >查看商城效果 ↗</a
        ></NAlert
      >
      <NSpin :show="loading">
        <div class="placement-heading">
          <h3>已設置 {{ rows.length }} 件</h3>
          <span
            >{{ pinnedCount }} 件置頂 ·
            {{ rows.filter((r) => r.placement.is_recommended).length }} 件推薦</span
          >
        </div>
        <NEmpty
          v-if="loaded && !rows.length"
          description="還沒有置頂或推薦商品，從下方選擇商品即可開始。"
          class="py-20px"
        />
        <div
          v-for="(row, index) in rows"
          :key="row.placement.product_id"
          class="placement-row"
          :class="{ 'placement-row-locked': row.product.is_locked }"
          @dragover.prevent
          @drop.prevent="drop(index)"
        >
          <button
            v-if="row.placement.is_pinned"
            class="drag-handle"
            :draggable="canWrite && !saving && !row.product.is_locked"
            :disabled="!canWrite || saving || row.product.is_locked"
            :aria-label="`拖動調整${row.product.name}順序，也可使用上移下移按鈕`"
            @dragstart="dragIndex = index"
            @dragend="dragIndex = null"
          >
            ⠿
          </button>
          <div class="placement-product">
            <div class="placement-name">
              <NTag v-if="row.placement.is_pinned" size="small">第 {{ index + 1 }} 位</NTag
              ><strong>{{ row.product.name }}</strong
              ><NTag v-if="row.product.is_locked" size="small">已鎖定</NTag>
            </div>
            <p>{{ categoryPath(row.product.category_id) }}</p>
            <p v-if="row.unavailable_reason" class="placement-note">{{ row.unavailable_reason }}</p>
            <p v-if="row.product.is_locked">先在商品管理中解鎖，才能調整此商品的設置。</p>
            <div class="placement-actions">
              <NCheckbox
                :checked="row.placement.is_pinned"
                :disabled="!canWrite || saving || row.product.is_locked"
                @update:checked="toggle(index, 'is_pinned', $event)"
                >本分類置頂</NCheckbox
              >
              <NCheckbox
                :checked="row.placement.is_recommended"
                :disabled="!canWrite || saving || row.product.is_locked"
                @update:checked="toggle(index, 'is_recommended', $event)"
                >顯示推薦標籤</NCheckbox
              >
              <template v-if="row.placement.is_pinned">
                <NButton
                  size="small"
                  :disabled="!canWrite || saving || row.product.is_locked || index === 0"
                  @click="move(index, index - 1)"
                  >上移</NButton
                >
                <NButton
                  size="small"
                  :disabled="
                    !canWrite || saving || row.product.is_locked || index === pinnedCount - 1
                  "
                  @click="move(index, index + 1)"
                  >下移</NButton
                >
                <NButton
                  size="small"
                  :disabled="!canWrite || saving || row.product.is_locked || index === 0"
                  @click="move(index, 0)"
                  >移到最前</NButton
                >
              </template>
              <NButton
                text
                :disabled="!canWrite || saving || row.product.is_locked"
                @click="remove(index)"
                >移除設置</NButton
              >
            </div>
          </div>
        </div>
      </NSpin>
      <section class="placement-search" aria-label="選擇分類商品">
        <h3>添加商品</h3>
        <div class="placement-search-bar">
          <NInput
            v-model:value="keyword"
            placeholder="搜索本分類及子分類商品"
            clearable
            @keyup.enter="search()"
          /><NButton :loading="searching" @click="search()">搜索</NButton>
        </div>
        <NAlert v-if="searchError" type="error"
          >{{ searchError }}<NButton text @click="search(false)">重試</NButton></NAlert
        >
        <NSpin :show="searching">
          <NEmpty
            v-if="!searching && !searchError && !candidates.length"
            description="沒有找到商品，請換個關鍵詞或檢查商品所屬分類。"
            class="py-20px"
          />
          <div v-for="p in candidates" :key="p.id" class="placement-candidate">
            <img v-if="p.cover" :src="p.cover" alt="" />
            <div v-else class="cover-placeholder" aria-hidden="true">—</div>
            <div class="placement-product">
              <strong>{{ p.name }}</strong>
              <p>{{ categoryPath(p.category_id) }} · {{ formatMoney(p.price_cents || 0) }}</p>
              <p v-if="p.is_locked">已鎖定，請先在商品管理中解鎖</p>
              <p v-else-if="p.status !== 1">未上架 · 設置後暫不展示</p>
            </div>
            <NButton
              size="small"
              :disabled="!canWrite || !loaded || saving || p.is_locked || selected.has(p.id)"
              @click="add(p)"
              >{{ selected.has(p.id) ? "已添加" : p.is_locked ? "已鎖定" : "置頂並推薦" }}</NButton
            >
          </div>
        </NSpin>
        <NPagination
          v-model:page="page"
          :page-size="8"
          :item-count="total"
          simple
          :disabled="searching"
          @update:page="search(false)"
        />
      </section>
      <template #footer
        ><div class="placement-footer">
          <span role="status">{{
            saving
              ? "正在保存…"
              : dirty
                ? "有未保存的修改"
                : loaded
                  ? "設置已同步"
                  : "正在讀取設置…"
          }}</span
          ><NButton :disabled="saving" @click="close">關閉</NButton
          ><NButton
            v-if="canWrite"
            type="primary"
            :loading="saving"
            :disabled="!dirty || loading"
            @click="save"
            >保存設置</NButton
          >
        </div></template
      >
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.placement-context {
  padding: 14px 16px;
  border-radius: 8px;
  background: var(--n-color-modal);
  border: 1px solid var(--n-divider-color);
  margin-bottom: 16px;
}
.placement-context p,
.placement-product p {
  margin: 5px 0;
  font-size: 13px;
  opacity: 0.75;
  overflow-wrap: anywhere;
}
.placement-heading,
.placement-name,
.placement-actions,
.placement-search-bar,
.placement-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.placement-heading {
  justify-content: space-between;
}
.placement-heading span,
.placement-footer span {
  font-size: 13px;
  opacity: 0.75;
}
h3 {
  margin: 12px 0;
  font-size: 15px;
}
.placement-row,
.placement-candidate {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  padding: 14px 0;
  border-bottom: 1px solid var(--n-divider-color);
}
.placement-row-locked {
  background: var(--n-color-modal);
}
.placement-product {
  flex: 1;
  min-width: 0;
}
.placement-product strong {
  overflow-wrap: anywhere;
}
.placement-actions {
  margin-top: 10px;
}
.placement-note {
  font-weight: 500;
}
.drag-handle {
  border: 0;
  background: transparent;
  color: inherit;
  cursor: grab;
  font-size: 24px;
  min-width: 32px;
  min-height: 40px;
}
.drag-handle:disabled {
  cursor: not-allowed;
  opacity: 0.4;
}
.drag-handle:focus-visible {
  outline: 2px solid currentColor;
}
.placement-search {
  margin-top: 24px;
}
.placement-search-bar {
  flex-wrap: nowrap;
  margin-bottom: 8px;
}
.placement-candidate {
  align-items: center;
}
.placement-candidate img,
.cover-placeholder {
  width: 42px;
  height: 42px;
  object-fit: cover;
  border-radius: 5px;
  flex-shrink: 0;
}
.cover-placeholder {
  display: grid;
  place-items: center;
  background: var(--n-color-modal);
}
.placement-footer {
  width: 100%;
  justify-content: flex-end;
}
.placement-footer span {
  margin-right: auto;
}
.placement-search :deep(.n-pagination) {
  margin-top: 14px;
}
@media (max-width: 600px) {
  .placement-candidate {
    flex-wrap: wrap;
  }
  .placement-candidate > .n-button {
    margin-left: 54px;
    min-height: 44px;
  }
  .placement-actions :deep(.n-button) {
    min-height: 44px;
  }
  .placement-actions :deep(.n-checkbox) {
    min-height: 36px;
  }
  .placement-footer {
    gap: 8px;
  }
  .placement-footer span {
    width: 100%;
  }
  .placement-context {
    padding: 12px;
  }
  .drag-handle {
    min-width: 24px;
  }
}
</style>
