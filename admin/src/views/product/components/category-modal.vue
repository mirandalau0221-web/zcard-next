<script setup lang="ts">
/**
 * 分類管理共用視圖：獨立頁面與商品頁彈窗複用，保留同一套分類操作。
 * 新建/變更後向父組件拋 refresh 事件（表單下拉聯動刷新）。
 */
import { ref, reactive, computed, watch } from "vue";
import MediaField from "@/components/common/media-picker/media-field.vue";
import CategoryPlacements from "./category-placements.vue";
import CategoryIcon from "@/components/common/category-icon.vue";
import { checkAuth } from "@/directives";
import { NCard, NButton, NTag, NInput, NInputNumber, NSelect, NModal, NDropdown, NTooltip, NCheckbox, NPopconfirm, NPopover } from "naive-ui";
import type { DropdownOption } from "naive-ui";
import {
  fetchCategories,
  createCategory,
  updateCategory,
  deleteCategory,
  reorderCategories,
  mergeCategories,
} from "@/service/api";

const props = withDefaults(defineProps<{ show?: boolean; embedded?: boolean }>(), { show: false, embedded: false });
const canWrite = computed(() => checkAuth("catalog:category_write"));
const canDelete = computed(() => checkAuth("catalog:category_delete"));
const search = ref("");
const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "refresh"): void;
  (e: "created", id: number): void;
}>();

const placementCategory=ref<any | null>(null);
const loading = ref(false);
const categories = ref<any[]>([]);

// 新建
const showCreate = ref(false);
const newName = ref("");
const newParent = ref<number | null>(null);
const newIcon = ref("");
const creating = ref(false);

// 重命名 / 刪除確認
const renaming = ref<any | null>(null);
const renameText = ref("");
const deleteConfirm = reactive({ show: false, cat: null as any });

// ── 圖標（本地圖標庫：內置 emoji 分組，即選即用；前台分類樹/膠囊展示）──
const ICON_GROUPS: { label: string; icons: string[] }[] = [
  { label: "常用", icons: ["🏷️", "🛒", "🎁", "⭐", "🔥", "💼", "📦", "💰", "🆕", "💡"] },
  { label: "遊戲", icons: ["🎮", "🕹️", "🎰", "🏆", "🎯", "🃏", "🎲", "👾", "🤖", "⚔️"] },
  { label: "影音", icons: ["🎬", "🎵", "📺", "🎧", "📷", "🎤", "🎨", "📚", "🎹", "🎬"] },
  { label: "軟件", icons: ["💻", "🖥️", "⌨️", "⚙️", "🔧", "🧩", "💾", "📀", "🧠", "🛡️"] },
  { label: "通訊", icons: ["📱", "☎️", "📞", "💬", "📧", "📡", "🌐", "✉️", "📮", "📡"] },
  { label: "生活", icons: ["🏠", "🚗", "✈️", "🍔", "👕", "💊", "🎓", "⚽", "🐾", "🌸"] },
  { label: "金融", icons: ["💳", "💵", "🏦", "📈", "🪙", "💎", "🧾", "💹", "🤑", "📉"] },
];
const iconPicking = ref<any | null>(null); // 正在選圖標的分類（null=面板關閉）

// 自定義圖片圖標（MediaField 數組值；選定即寫入 newIcon=URL——icon 字段 emoji/URL 同存，前台按形態渲染）
const customIconImage = ref<string[]>([]);
// 行內快改面板的自定義圖片（應用到 iconPicking 當前分類）
const rowIconImage = ref<string[]>([]);
watch(rowIconImage, (v) => {
  const url = (v || [])[0];
  if (url && iconPicking.value && typeof iconPicking.value === "number") {
    const cat = flatTree.value.find((c: any) => c.id === iconPicking.value);
    if (cat) {
      applyIcon(cat, url);
      rowIconImage.value = [];
    }
  }
});
watch(customIconImage, (v) => {
  const url = (v || [])[0];
  if (url) {
    newIcon.value = url;
    iconPicking.value = null;
  }
});

async function applyIcon(cat: any, icon: string) {
  const { error } = await updateCategory(cat.id, { icon }); // 空串=清除（服務端 optional 語義）
  if (!error) {
    const source = categories.value.find(c => c.id === cat.id);
    if (source) source.icon = icon;
    window.$message?.success(icon ? "圖標已更新" : "圖標已清除");
    iconPicking.value = null;
    emit("refresh");
  }
}

const visible = computed({
  get: () => props.embedded || props.show,
  set: (v: boolean) => emit("update:show", v),
});

// 樹形化（含深度）
const tree = computed(() => {
  const map = new Map<number, any>();
  for (const c of categories.value) map.set(c.id, { ...c, depth: 0, children: [] });
  const roots: any[] = [];
  for (const node of map.values()) {
    if (node.parent_id && map.has(node.parent_id)) {
      const parent = map.get(node.parent_id)!;
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
});

const flatTree = computed(() => {
  const out: any[] = [];
  const walk = (nodes: any[], depth = 0, parentPath = "", ancestorHidden = false) => {
    for (const n of nodes) {
      const path = [parentPath, n.name].filter(Boolean).join(" / ");
      out.push({ ...n, depth, path, ancestorHidden });
      walk(n.children, depth + 1, path, ancestorHidden || n.hide);
    }
  };
  walk(tree.value);
  return out;
});

const filteredTree = computed(() => {
  const query = search.value.trim().toLocaleLowerCase();
  return query ? flatTree.value.filter(c => c.path.toLocaleLowerCase().includes(query)) : flatTree.value;
});

const parentOptions = computed(() => [
  { label: "頂級分類", value: 0 },
  ...flatTree.value.map((c) => ({ label: c.path, value: c.id })),
]);

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchCategories();
    if (!error && data) {
      categories.value = (data as any).categories || [];
      const existing = new Set(categories.value.map(cat => cat.id));
      batchChecked.value = new Set([...batchChecked.value].filter(id => existing.has(id)));
      return true;
    }
    return false;
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.embedded || props.show,
  (v) => {
    if (v) {
      load();
      resetCreate();
    }
  },
  { immediate: true },
);

function resetCreate() {
  newName.value = "";
  newParent.value = null;
  newIcon.value = "";
  customIconImage.value = [];
  iconPicking.value = null;
  showCreate.value = false;
}

async function handleCreate() {
  if (!newName.value.trim() || creating.value) return;
  if (Array.from(newName.value.trim()).length > 100) { window.$message?.warning("分類名稱不能超過 100 個字符"); return; }
  creating.value = true;
  try {
    const parentId = newParent.value || 0;
    // 默認排到同級最後（sort = 同級最大 + 1）
    const maxSort = categories.value
      .filter((c) => (c.parent_id || 0) === parentId)
      .reduce((m, c) => Math.max(m, c.sort || 0), 0);
    const { data, error } = await createCategory({
      name: newName.value.trim(),
      parent_id: newParent.value || undefined,
      icon: newIcon.value || undefined,
      sort: maxSort + 1,
    });
    if (!error) {
      window.$message?.success("分類已創建");
      const id = (data as any)?.id || 0;
      resetCreate();
      await load();
      emit("refresh");
      if (id) emit("created", id); // 新建即選中（「建完就用」）
    }
  } finally {
    creating.value = false;
  }
}

async function handleRename() {
  if (!renaming.value || !renameText.value.trim()) return;
  if (Array.from(renameText.value.trim()).length > 100) { window.$message?.warning("分類名稱不能超過 100 個字符"); return; }
  const { error } = await updateCategory(renaming.value.id, { name: renameText.value.trim() });
  if (!error) {
    window.$message?.success("已重命名");
    renaming.value = null;
    load();
    emit("refresh");
  }
}

const deletingCategory = ref(false);
async function handleDelete() {
  const cat = deleteConfirm.cat;
  if (!cat || deletingCategory.value) return;
  deletingCategory.value = true;
  try {
    const { error } = await deleteCategory(cat.id);
    if (!error) {
      window.$message?.success("分類、子分類及其中商品已刪除，訂單記錄已保留");
      deleteConfirm.show = false;
      await load();
      emit("refresh");
    }
  } finally { deletingCategory.value = false; }
}

const visibilityBusy = ref<number | null>(null);
async function toggleVisibility(cat: any) {
  if (visibilityBusy.value !== null) return;
  visibilityBusy.value = cat.id;
  try {
    const { error } = await updateCategory(cat.id, { hide: !cat.hide });
    if (!error) {
      await load();
      emit("refresh");
      window.$message?.success(cat.hide ? "已恢復分類顯示，商品沿用原上下架狀態" : "已隱藏分類、子分類及其商品");
    }
  } finally { visibilityBusy.value = null; }
}

const menuOptions = computed<DropdownOption[]>(() => [
  {label:"置頂與推薦",key:"placements"},
  ...(canWrite.value ? [
    { label: "添加子分類", key: "child" },
    { label: "重命名", key: "rename" },
    { label: "調整上級分類", key: "parent" },
  ] : []),
  ...(canDelete.value ? [{ label: "刪除分類", key: "delete", divided: true, props: { style: "color: var(--error-color, #d03050)" } }] : []),
]);
const moving = ref<any | null>(null);
const moveParent = ref<number | null>(0);
const moveSaving = ref(false);
const moveOptions = computed(() => {
  const blocked = new Set<number>();
  const children = new Map<number, number[]>();
  for (const cat of categories.value) {
    const parent = cat.parent_id || 0;
    if (!children.has(parent)) children.set(parent, []);
    children.get(parent)!.push(cat.id);
  }
  const pending = moving.value ? [moving.value.id] : [];
  while (pending.length) {
    const id = pending.pop()!;
    if (blocked.has(id)) continue;
    blocked.add(id);
    pending.push(...(children.get(id) || []));
  }
  return parentOptions.value.map(option => ({ ...option, disabled: blocked.has(option.value) }));
});
async function saveParent() {
  if (!moving.value || moveSaving.value) return;
  moveSaving.value = true;
  try {
    const { error } = await updateCategory(moving.value.id, { parent_id: moveParent.value || 0 });
    if (!error) {
      moving.value = null;
      await load();
      emit("refresh");
      window.$message?.success("上級分類已更新");
    }
  } finally { moveSaving.value = false; }
}

function handleMenu(key: string | number, cat: any) {
  if(key === "placements"){placementCategory.value=cat;return}
  if (key === "child") {
    resetCreate();
    newParent.value = cat.id;
    showCreate.value = true;
  } else if (key === "parent") {
    moving.value = cat;
    moveParent.value = cat.parent_id || 0;
  } else if (key === "rename") {
    renaming.value = cat;
    renameText.value = cat.name;
  } else if (key === "delete") {
    deleteConfirm.cat = cat;
    deleteConfirm.show = true;
  }
}

// ── 拖拽（HTML5 DnD）：拖到行上/下邊緣 = 排序插入；拖到行中間 = 設為該分類子級；拖到頂部釋放區 = 頂級 ──
const dragId = ref<number | null>(null);
const dragOverId = ref<number | null>(null);
const dropPos = ref<"before" | "after" | "child" | null>(null);
const dropToRoot = ref(false);

function onDragStart(cat: any) {
  dragId.value = cat.id;
}

function onDragOver(cat: any, e: DragEvent) {
  dragOverId.value = cat.id;
  const el = e.currentTarget as HTMLElement;
  const ratio = e.offsetY / el.offsetHeight;
  if (ratio < 0.3) dropPos.value = "before";
  else if (ratio > 0.7) dropPos.value = "after";
  else dropPos.value = "child";
}

function onDragLeave() {
  dragOverId.value = null;
  dropPos.value = null;
}

function resetDragState() {
  dragId.value = null;
  dragOverId.value = null;
  dropPos.value = null;
  dropToRoot.value = false;
}

// 目標是否非法（自身/自身後代）
function invalidTarget(draggedId: number, targetId: number) {
  if (draggedId === targetId) return true;
  const byId = new Map<number, any>();
  for (const c of categories.value) byId.set(c.id, { ...c, children: [] as any[] });
  for (const node of byId.values()) {
    if (node.parent_id && byId.has(node.parent_id)) byId.get(node.parent_id)!.children.push(node);
  }
  const stack = [draggedId];
  while (stack.length) {
    const cur = stack.pop()!;
    if (cur === targetId) return true;
    for (const child of byId.get(cur)?.children || []) stack.push(child.id);
  }
  return false;
}

// 行樣式：拖拽懸停時上/下邊緣顯示藍色插入線（box-shadow 不改變佈局），中間藍底
function rowClass(cat: any) {
  if (dragId.value === cat.id) return "opacity-40";
  if (dragOverId.value !== cat.id || !dropPos.value) return "";
  return dropPos.value === "child" ? "bg-blue-50 dark:bg-blue-900/30" : "";
}

function rowStyle(cat: any) {
  const shadow =
    dragOverId.value === cat.id && dropPos.value === "before"
      ? "inset 0 2px 0 0 #4098ff"
      : dragOverId.value === cat.id && dropPos.value === "after"
        ? "inset 0 -2px 0 0 #4098ff"
        : "";
  return {
    paddingLeft: `${10 + cat.depth * 16}px`,
    ...(shadow ? { boxShadow: shadow } : {}),
  };
}

// 拖拽排序：把被拖分類插入到目標行之前/之後（同層級 = 目標行的父級）
async function reorderByDrop(draggedId: number, target: any, pos: "before" | "after") {
  const parentId = target.parent_id || 0;
  // 目標層級不能是被拖分類自身或其子孫（會成環）
  if (invalidTarget(draggedId, parentId)) {
    window.$message?.warning("不能把分類移到自身或它的子分類下");
    return;
  }
  const dragged = categories.value.find((c) => c.id === draggedId);
  if (!dragged) return;
  // 目標層級全部兄弟（不含被拖分類；服務端順序即 sort 升序）
  const siblings = categories.value.filter((c) => (c.parent_id || 0) === parentId && c.id !== draggedId);
  const targetIdx = siblings.findIndex((c) => c.id === target.id);
  if (targetIdx < 0) return;
  siblings.splice(pos === "before" ? targetIdx : targetIdx + 1, 0, dragged);
  const ids = siblings.map((c) => c.id);
  // 順序無變化（拖回原位）則跳過
  const curIds = categories.value
    .filter((c) => (c.parent_id || 0) === parentId)
    .map((c) => c.id);
  if (ids.join(",") === curIds.join(",")) return;
  const { error } = await reorderCategories(parentId, ids);
  if (!error) {
    window.$message?.success("排序已保存");
    load();
    emit("refresh");
  }
}

async function onDrop(target: any) {
  const from = dragId.value;
  const pos = dropPos.value;
  resetDragState();
  if (!from || !target) return;
  if (pos === "before" || pos === "after") {
    await reorderByDrop(from, target, pos);
    return;
  }
  if (invalidTarget(from, target.id)) {
    window.$message?.warning("不能把分類移到自身或它的子分類下");
    return;
  }
  const { error } = await updateCategory(from, { parent_id: target.id });
  if (!error) {
    window.$message?.success(`已移到「${target.name}」下`);
    load();
    emit("refresh");
  }
}

async function onDropToRoot() {
  const from = dragId.value;
  resetDragState();
  if (!from) return;
  const { error } = await updateCategory(from, { parent_id: 0 });
  if (!error) {
    window.$message?.success("已設為頂級分類");
    load();
    emit("refresh");
  }
}

// 多選直接在列表中顯示，搜索僅改變可見範圍，不取消已勾選的分類。
const batchChecked = ref<Set<number>>(new Set());
const batchDeleting = ref(false);
const batchMode = computed(() => batchChecked.value.size > 0 || batchDeleting.value);
const batchProgress = reactive({ done: 0, total: 0 });
const batchFailures = ref<{ id: number; name: string; message: string }[]>([]);
const allFilteredChecked = computed(() => filteredTree.value.length > 0 && filteredTree.value.every(cat => batchChecked.value.has(cat.id)));
const someFilteredChecked = computed(() => filteredTree.value.some(cat => batchChecked.value.has(cat.id)) && !allFilteredChecked.value);

function clearBatchSelection() {
  if (batchDeleting.value) return;
  batchChecked.value = new Set();
  batchFailures.value = [];
}
function toggleBatchCheck(id: number, on: boolean) {
  if (!canDelete.value || batchDeleting.value) return;
  const next = new Set(batchChecked.value);
  if (on) next.add(id);
  else next.delete(id);
  batchChecked.value = next;
}
function selectFiltered(on: boolean) {
  if (!canDelete.value || batchDeleting.value) return;
  const next = new Set(batchChecked.value);
  for (const cat of filteredTree.value) {
    if (on) next.add(cat.id);
    else next.delete(cat.id);
  }
  batchChecked.value = next;
}

async function handleBatchDelete() {
  if (!canDelete.value || batchDeleting.value || !batchChecked.value.size) return;
  // A selected parent already includes its entire subtree. Submit each root once.
  const byID = new Map(flatTree.value.map(cat => [cat.id, cat]));
  const selected = flatTree.value.filter(cat => {
    if (!batchChecked.value.has(cat.id)) return false;
    const seen = new Set<number>([cat.id]);
    let parent = cat.parent_id;
    while (parent && !seen.has(parent)) {
      if (batchChecked.value.has(parent)) return false;
      seen.add(parent); parent = byID.get(parent)?.parent_id || 0;
    }
    return true;
  });
  const initialIDs = new Set(categories.value.map(cat => cat.id));
  batchDeleting.value = true;
  batchFailures.value = [];
  batchProgress.done = 0;
  batchProgress.total = selected.length;
  let deleted = 0;
  try {
    for (const cat of selected) {
      if (!canDelete.value) break;
      const { error } = await deleteCategory(cat.id, true);
      batchProgress.done++;
      if (!error) {
        const removed = new Set<number>([cat.id]);
        for (const node of flatTree.value) if (removed.has(node.parent_id)) removed.add(node.id);
        batchChecked.value = new Set([...batchChecked.value].filter(id => !removed.has(id)));
      } else {
        const response = (error as any).response;
        batchFailures.value.push({ id: cat.id, name: cat.path, message: response?.data?.message || error.message || "刪除失敗，請刷新後重試" });
        if (response?.status === 401 || response?.status === 403) break;
      }
    }
    if (await load()) {
      // A lost DELETE reply can still mean the server deleted the category.
      // Confirm against the refreshed list instead of offering a second deletion.
      const existing = new Set(categories.value.map(cat => cat.id));
      const remaining = batchFailures.value.filter(item => existing.has(item.id));
      deleted = [...initialIDs].filter(id => !existing.has(id)).length;
      batchFailures.value = remaining;
    }
    if (deleted) {
      window.$message?.success(`已刪除 ${deleted} 個分類`);
      emit("refresh");
    }
    if (batchFailures.value.length) window.$message?.warning(`${batchFailures.value.length} 個分類未刪除，已保留勾選，請查看原因`);
  } finally {
    batchDeleting.value = false;
  }
}
watch(() => props.embedded || props.show, () => { if (!batchDeleting.value) clearBatchSelection(); });


const showMerge = ref(false);
const mergeTarget = ref<number | null>(null);
const merging = ref(false);
const mergePreview = ref<{ categories: number; products: number; children: number } | null>(null);
const mergeSources = ref<number[]>([]);
const mergeOptions = computed(() => {
  const excluded = new Set(mergeSources.value);
  for (const cat of flatTree.value) if (excluded.has(cat.parent_id)) excluded.add(cat.id);
  const names = new Map<number, string>();
  return flatTree.value.filter(cat => {
    names.set(cat.id, [names.get(cat.parent_id), cat.name].filter(Boolean).join(' / '));
    return !excluded.has(cat.id);
  }).map(cat => ({ value: cat.id, label: names.get(cat.id)! }));
});
watch(mergeTarget, () => { mergePreview.value = null; });
function openMerge() {
  mergeSources.value = [...batchChecked.value];
  mergeTarget.value = null;
  mergePreview.value = null;
  showMerge.value = true;
}
async function runMerge(preview: boolean) {
  if (!mergeTarget.value || merging.value) return;
  merging.value = true;
  try {
    const { data, error } = await mergeCategories({ source_ids: mergeSources.value, target_id: mergeTarget.value, preview });
    if (error || !data) return;
    if (preview) mergePreview.value = data;
    else {
      window.$message?.success(`已合併 ${data.categories} 個分類，遷移 ${data.products} 件商品`);
      showMerge.value = false;
      batchChecked.value = new Set();
      await load();
      emit("refresh");
    }
  } finally { merging.value = false; }
}

// 排序輸入框：失焦/回車保存（hide 未傳保持原狀）
async function onSortBlur(cat: any) {
  const v = cat.sort;
  if (typeof v !== "number" || v < 0) {
    cat.sort = 0;
    return;
  }
  const { error } = await updateCategory(cat.id, { sort: v });
  if (!error) {
    window.$message?.success("排序已保存");
  }
  load();
  emit("refresh");
}
</script>

<template>
  <component :is="embedded ? NCard : NModal"
    v-bind="embedded ? { title: '商品分類' } : { show: visible, preset: 'card', title: '分類管理', style: 'width: 960px; max-width: 94vw', closable: !batchDeleting, maskClosable: !batchDeleting, closeOnEsc: !batchDeleting }"
    class="category-manager" :class="{ 'category-manager-page': embedded }"
    @update:show="visible = $event">
    <div class="mb-12px flex flex-wrap items-center gap-8px">
      <NInput v-model:value="search" :disabled="batchDeleting" clearable placeholder="搜索分類名稱或完整路徑" :input-props="{ 'aria-label': '搜索分類' }" class="min-w-180px flex-1" />
      <NButton :loading="loading" :disabled="batchDeleting" @click="load">刷新</NButton>
    </div>
    <div class="mb-12px flex flex-wrap items-center justify-between gap-8px">
      <div class="flex flex-wrap items-center gap-8px">
        <span class="text-13px text-gray-500">共 {{ flatTree.length }} 個分類<template v-if="search.trim()">，找到 {{ filteredTree.length }} 個</template></span>
        <template v-if="canDelete">
          <NCheckbox aria-label="全選當前篩選結果" :aria-disabled="batchDeleting || loading || !filteredTree.length" :checked="allFilteredChecked" :indeterminate="someFilteredChecked" :disabled="batchDeleting || loading || !filteredTree.length" @update:checked="selectFiltered">全選當前篩選結果</NCheckbox>
          <NTag size="small" :bordered="false">已選 {{ batchChecked.size }}</NTag>
          <NButton size="small" :disabled="!batchChecked.size || batchDeleting" @click="clearBatchSelection">清空選擇</NButton>
          <NButton size="small" :disabled="!batchChecked.size || batchDeleting" @click="openMerge">合併所選</NButton>
          <NPopconfirm positive-text="確認刪除" negative-text="取消" :disabled="!batchChecked.size || batchDeleting" @positive-click="handleBatchDelete">
            <template #trigger>
              <NButton size="small" type="error" :loading="batchDeleting" :disabled="!batchChecked.size || batchDeleting">
                批量刪除（{{ batchChecked.size }}）
              </NButton>
            </template>
            刪除勾選的 {{ batchChecked.size }} 個分類？同時刪除全部子分類和其中商品（包括隱藏、下架及鎖定商品）；保留訂單快照、交付和退款記錄。
          </NPopconfirm>
        </template>
      </div>
      <div class="flex flex-wrap items-center gap-8px">
        <NButton v-auth="'catalog:category_write'" size="small" type="primary" :disabled="batchDeleting" @click="showCreate = !showCreate">
          {{ showCreate ? "收起" : "新建分類" }}
        </NButton>
      </div>
    </div>

    <p v-if="batchDeleting" role="status" class="mb-12px text-13px">正在刪除分類：{{ batchProgress.done }} / {{ batchProgress.total }}</p>
    <div v-if="batchFailures.length" role="status" class="category-delete-failures mb-12px rounded-4px border border-orange-200 p-12px">
      <p class="mb-6px">以下分類未刪除，仍保持勾選：</p>
      <ul class="m-0 pl-20px"><li v-for="item in batchFailures" :key="item.id">{{ item.name }}：{{ item.message }}</li></ul>
    </div>

    <!-- 新建 -->
    <div v-if="showCreate" class="category-create mb-12px flex flex-wrap items-center gap-8px">
      <NPopover trigger="manual" :show="iconPicking === 'new'" placement="bottom-start" style="max-width: 360px">
        <template #trigger>
          <button
            type="button"
            class="cat-icon-btn shrink-0"
            title="選擇圖標（可選）"
            @click="iconPicking = iconPicking === 'new' ? null : 'new'"
          >
            <CategoryIcon :icon="newIcon" fallback="➕" />
          </button>
        </template>
        <div class="w-320px">
          <div v-for="g in ICON_GROUPS" :key="g.label" class="mb-6px">
            <div class="mb-2px text-11px text-gray-400">{{ g.label }}</div>
            <div class="flex flex-wrap gap-4px">
              <button
                v-for="ic in g.icons"
                :key="ic"
                type="button"
                class="icon-cell"
                :class="{ active: newIcon === ic }"
                @click="newIcon = ic; iconPicking = null"
              >
                {{ ic }}
              </button>
            </div>
          </div>
          <div class="mt-6px border-t border-gray-100 pt-6px dark:border-gray-700">
            <div class="mb-4px text-11px text-gray-400">自定義圖片（上傳後優先於 emoji）</div>
            <MediaField v-model:value="customIconImage" />
          </div>
          <div class="flex justify-end border-t border-gray-100 pt-6px dark:border-gray-700">
            <NButton size="tiny" quaternary @click="newIcon = ''; iconPicking = null">不使用圖標</NButton>
          </div>
        </div>
      </NPopover>
      <NInput
        v-model:value="newName"
        size="small"
        placeholder="分類名稱（最多 100 字）"
        class="flex-1"
        @keyup.enter="handleCreate"
      />
      <NSelect
        v-model:value="newParent"
        size="small"
        placeholder="父分類"
        class="w-280px"
        filterable
        :options="parentOptions"
      />
      <NButton size="small" type="primary" :loading="creating" @click="handleCreate">創建</NButton>
    </div>

    <!-- 樹列表（行可拖拽：上/下邊緣=排序插入，行中間=設為子級；頂部釋放區=頂級） -->
    <div v-if="canWrite" class="mb-4px text-12px text-gray-400">搜索時暫停拖拽；也可通過操作菜單調整上級。拖到行上/下邊緣 = 排序；拖到行中間 = 設為子級；拖到頂部虛線區 = 設為頂級</div>
    <div
      v-if="canWrite && !batchMode && !search.trim()"
      class="mb-4px rounded-4px border border-dashed px-10px py-6px text-center text-12px"
      :class="dropToRoot ? 'border-blue-400 bg-blue-50 text-blue-500' : 'border-gray-300 text-gray-400 dark:border-gray-600'"
      @dragover.prevent="dropToRoot = true"
      @dragleave="dropToRoot = false"
      @drop.prevent="onDropToRoot"
    >
      {{ dropToRoot ? '鬆開設為頂級分類' : '拖拽分類到此處 = 設為頂級' }}
    </div>
    <NScrollbar x-scrollable :style="{ maxHeight: embedded ? 'calc(100vh - 330px)' : '340px', minHeight: '160px' }" class="rounded-4px border border-gray-200 dark:border-gray-700">
      <NEmpty
        v-if="!filteredTree.length && !loading"
        size="small"
        class="mt-40px"
        :description="search.trim() ? '未找到分類，請換個關鍵詞或清空搜索' : '暫無分類'"
      />
      <div
        v-for="cat in filteredTree"
        :key="cat.id"
        :draggable="canWrite && !batchMode && !search.trim()"
        class="category-manage-row group flex cursor-grab items-center gap-8px rounded-4px py-7px pr-8px text-13px hover:bg-gray-100 dark:hover:bg-gray-800 active:cursor-grabbing"
        :class="rowClass(cat)"
        :style="rowStyle(cat)"
        @dragstart="onDragStart(cat)"
        @dragend="resetDragState"
        @dragover.prevent="onDragOver(cat, $event)"
        @dragleave="onDragLeave"
        @drop.prevent="onDrop(cat)"
      >
        <!-- 批量模式勾選框 -->
        <NCheckbox
          v-if="canDelete"
          :aria-label="`選擇分類 ${cat.path}`"
          :aria-disabled="batchDeleting"
          :disabled="batchDeleting"
          :checked="batchChecked.has(cat.id)"
          class="shrink-0"
          @update:checked="(v: boolean) => toggleBatchCheck(cat.id, v)"
        />
        <!-- 圖標槽：點擊開本地圖標庫（選擇即存；支持清除）；前台分類樹/膠囊同源展示 -->
        <NPopover v-if="!batchMode && canWrite" trigger="manual" :show="iconPicking === cat.id" placement="right" style="max-width: 360px">
          <template #trigger>
            <button
              type="button"
              class="cat-icon-btn shrink-0"
              :title="cat.icon ? '更換圖標' : '設置圖標'"
              @click.stop="iconPicking = iconPicking === cat.id ? null : cat.id"
            >
              <CategoryIcon :icon="cat.icon" fallback="➕" />
            </button>
          </template>
          <div class="w-320px">
            <div v-for="g in ICON_GROUPS" :key="g.label" class="mb-6px">
              <div class="mb-2px text-11px text-gray-400">{{ g.label }}</div>
              <div class="flex flex-wrap gap-4px">
                <button
                  v-for="ic in g.icons"
                  :key="ic"
                  type="button"
                  class="icon-cell"
                  :class="{ active: cat.icon === ic }"
                  @click="applyIcon(cat, ic)"
                >
                  {{ ic }}
                </button>
              </div>
            </div>
            <div class="mt-6px border-t border-gray-100 pt-6px dark:border-gray-700">
              <div class="mb-4px text-11px text-gray-400">自定義圖片</div>
              <MediaField v-model:value="rowIconImage" />
            </div>
            <div class="flex items-center justify-between border-t border-gray-100 pt-6px dark:border-gray-700">
              <NButton v-auth="'catalog:category_write'" size="tiny" quaternary type="error" @click="applyIcon(cat, '')">
                🗑 清除圖標
              </NButton>
              <NButton size="tiny" quaternary @click="iconPicking = null">關閉</NButton>
            </div>
          </div>
        </NPopover>
        <CategoryIcon v-else :icon="cat.icon" />
        <!-- 名稱列：佔滿剩餘寬度，超長截斷不撐破行；懸浮顯示全名 -->
        <div class="category-name flex min-w-0 flex-1 items-center gap-6px">
          <NTooltip  placement="top" :show-arrow="false">
            <template #trigger>
              <span class="category-full-name" :title="cat.path">{{ cat.name }}</span>
            </template>
            {{ cat.name }}
          </NTooltip>
          <span v-if="cat.depth > 0" class="category-path">{{ cat.path }}</span>
        </div>
        <!-- 商品數/排序/操作：固定列寬，不隨名稱長度漂移 -->
        <NTag size="small" :type="cat.hide || cat.ancestorHidden ? 'warning' : 'success'" :bordered="false">{{ cat.hide ? '已隱藏' : cat.ancestorHidden ? '隨父級隱藏' : '顯示中' }}</NTag>
        <NPopconfirm v-if="!batchMode" @positive-click="toggleVisibility(cat)">
          <template #trigger><NButton v-auth="'catalog:category_write'" size="small" secondary :loading="visibilityBusy === cat.id" :disabled="visibilityBusy !== null || (cat.ancestorHidden && !cat.hide)">{{ cat.hide ? '恢復顯示' : '隱藏下架' }}</NButton></template>
          {{ cat.hide ? `恢復「${cat.name}」的顯示？若父級仍隱藏，本分類仍不會展示。商品保留原來的上下架狀態。` : `隱藏「${cat.name}」及全部子分類？其中商品將從商城和供貨目錄隱藏，無法新下單。已有訂單繼續處理。` }}
        </NPopconfirm>
        <span title="該分類及下級分類的未刪除商品總數（含隱藏、下架商品）" class="w-76px shrink-0 text-right text-12px text-gray-500">{{ cat.product_count || 0 }} 件</span>
        <NInputNumber
          v-model:value="cat.sort"
          :disabled="!canWrite || batchDeleting"
          :input-props="{ 'aria-label': `${cat.name}排序` }"
          size="tiny"
          :min="0"
          :show-button="false"
          class="w-56px shrink-0"
          placeholder="排序"
          @blur="onSortBlur(cat)"
          @keyup.enter="onSortBlur(cat)"
        />
        <NDropdown v-if="!batchDeleting && menuOptions.length"
          class="shrink-0"
          :options="menuOptions"
          trigger="click"
          @select="(key: string | number) => handleMenu(key, cat)"
        >
          <NButton size="small" quaternary :aria-label="`${cat.name}操作`">操作</NButton>
        </NDropdown>
      </div>
    </NScrollbar>

    <CategoryPlacements :show="!!placementCategory" :category-id="placementCategory?.id || 0" :category-name="placementCategory?.path || ''" :categories="categories" @update:show="!$event && (placementCategory=null)" />
    <NModal :show="!!moving" preset="card" title="調整上級分類" style="width: 480px; max-width: 94vw"
      :closable="!moveSaving" :mask-closable="!moveSaving" :close-on-esc="!moveSaving" @update:show="!$event && !moveSaving && (moving = null)">
      <p class="mb-12px">為「{{ moving?.name }}」選擇上級分類，子分類將隨其一起移動。</p>
      <NSelect v-model:value="moveParent" :options="moveOptions" filterable :disabled="moveSaving" placeholder="選擇上級分類" />
      <template #footer><div class="flex justify-end gap-8px">
        <NButton :disabled="moveSaving" @click="moving = null">取消</NButton>
        <NButton type="primary" :loading="moveSaving" @click="saveParent">保存</NButton>
      </div></template>
    </NModal>

    <!-- 重命名 -->
    <NModal
      :show="!!renaming"
      preset="dialog"
      title="分類重命名"
      style="width: 400px"
      @update:show="(v: boolean) => !v && (renaming = null)"
    >
      <NInput placeholder="分類名稱（最多 100 字）" v-model:value="renameText" @keyup.enter="handleRename" />
      <template #action>
        <NButton @click="renaming = null">取消</NButton>
        <NButton v-auth="'catalog:category_write'" type="primary" @click="handleRename">確定</NButton>
      </template>
    </NModal>

    <NModal :show="showMerge" preset="card" title="批量合併分類" style="width: 560px; max-width: 94vw" :closable="!merging" :mask-closable="false" :close-on-esc="!merging" @update:show="!merging && (showMerge = $event)">
      <div class="mb-12px">已選 {{ mergeSources.length }} 個來源分類。商品和上游映射遷移到目標分類，未選中的子分類移到目標下面，隨後刪除來源分類。</div>
      <NSelect v-model:value="mergeTarget" :options="mergeOptions" filterable :disabled="merging" placeholder="選擇合併到的本地分類" />
      <div v-if="mergePreview" class="mt-12px">將刪除 {{ mergePreview.categories }} 個來源分類、遷移 {{ mergePreview.products }} 件商品、移動 {{ mergePreview.children }} 個子分類。商品沿用目標分類的展示設置。</div>
      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton :disabled="merging" @click="showMerge = false">取消</NButton>
          <NButton v-if="!mergePreview" type="primary" :disabled="!mergeTarget" :loading="merging" @click="runMerge(true)">預覽影響範圍</NButton>
          <NButton v-else type="error" :loading="merging" @click="runMerge(false)">確認合併</NButton>
        </div>
      </template>
    </NModal>

    <!-- 刪除確認 -->
    <NModal
      :show="deleteConfirm.show"
      preset="dialog"
      title="刪除分類"
      style="width: 400px"
      :closable="!deletingCategory" :mask-closable="false" :close-on-esc="!deletingCategory"
      @update:show="(v: boolean) => !v && !deletingCategory && (deleteConfirm.show = false)"
    >
      確定刪除分類「{{ deleteConfirm.cat?.name }}」？將同時刪除全部子分類和其中商品（包括隱藏、下架及鎖定商品），保留訂單快照、交付和退款記錄。
      <template #action>
        <NButton :disabled="deletingCategory" @click="deleteConfirm.show = false">取消</NButton>
        <NButton v-auth="'catalog:category_delete'" type="error" :loading="deletingCategory" @click="handleDelete">刪除</NButton>
      </template>
    </NModal>
  </component>
</template>

<style scoped>
.category-manage-row { min-width: 830px; }
.category-path { display: none; }
@media (max-width: 640px) {
  .category-manage-row { min-width: 0; flex-wrap: wrap; padding: 12px 8px !important; gap: 8px; border-bottom: 1px solid var(--n-border-color); }
  .category-manage-row > .flex-1 { flex-basis: calc(100% - 70px); }
  .category-name { flex-direction: column; align-items: flex-start; }
  .category-path { display: block; color: var(--n-text-color-3); font-size: 12px; overflow-wrap: anywhere; }
  .category-create > .n-input { min-width: 180px; }
  .category-create > .n-select { width: 100%; }
}
.category-full-name { min-width: 0; white-space: normal; overflow-wrap: anywhere; line-height: 1.6; }
/* 圖標槽：行內小按鈕（空=虛框加號提示可設置） */
.cat-icon-btn {
  width: 26px;
  height: 26px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  border-radius: 6px;
  border: 1px dashed var(--n-border-color, #d9d9d9);
  background: transparent;
  cursor: pointer;
  transition: all 0.15s;
}

.cat-icon-btn:hover {
  border-color: #4098ff;
  background: rgba(64, 152, 255, 0.08);
}
/* 圖標庫網格格仔 */
.icon-cell {
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 17px;
  border-radius: 6px;
  border: 1px solid transparent;
  background: transparent;
  cursor: pointer;
  transition: all 0.12s;
}
.icon-cell:hover {
  background: rgba(64, 152, 255, 0.1);
}
.icon-cell.active {
  background: rgba(64, 152, 255, 0.16);
  border-color: #4098ff;
}
</style>
