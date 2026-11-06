<script setup lang="ts">
/**
 * 素材管理選擇器（ 前端面核心）：
 * 左：素材網格（選擇/分頁/搜索/上傳/外鏈導入/批量移動/批量刪除[引用確認兩段式]）
 * 右：分類面板（全部/未分類/分類樹；新建/重命名/刪除[非空拒絕]）
 * 單例掛載（App.vue）；pickMedia() Promise API 呼出；管理操作就地生效。
 */
import { ref, reactive, computed, watch } from "vue";
import { NButton, NTag, NPopconfirm, NInput, NModal, NDropdown } from "naive-ui";
import TablePager from "@/components/common/table-pager.vue";
import type { UploadCustomRequestOptions, DropdownOption } from "naive-ui";
import { resolveMediaUrl } from "@/utils/media";
import {
  fetchMediaList,
  fetchMediaCategories,
  createMediaCategory,
  renameMediaCategory,
  moveMediaCategory,
  deleteMediaCategory,
  uploadMedia,
  importMediaFromURL,
  renameMedia,
  moveMedia,
  deleteMedia,
} from "@/service/api";
import type { MediaItem, MediaCategory } from "@/service/api";
import { mediaPickerState, settleMediaPicker } from "./index";

const loading = ref(false);
const items = ref<MediaItem[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(24);
const keyword = ref("");

// 當前視圖：all | uncategorized | category:<id>
type View = "all" | "uncategorized" | { category: number };
const view = ref<View>("all");
const viewKey = computed(() =>
  view.value === "all"
    ? "all"
    : view.value === "uncategorized"
      ? "uncategorized"
      : `cat:${view.value.category}`,
);
const currentCategoryId = computed(() =>
  typeof view.value === "object" ? view.value.category : 0,
);

// 分類
const categories = ref<MediaCategory[]>([]);
const showCatCreate = ref(false);
const newCatName = ref("");
const newCatParent = ref<number | null>(null);
const renamingCat = ref<MediaCategory | null>(null);
const renameCatText = ref("");
// 移動分類（大廠「移動到」樹選擇器模式：目標=根或任一非子孫分類）
const movingCat = ref<MediaCategory | null>(null);
const moveCatTarget = ref<number | null>(null); // null = 根分類
// 分類刪除確認（下拉菜單無法包 Popconfirm → NModal）
const deleteCatConfirm = reactive({ show: false, cat: null as MediaCategory | null });

// 選擇集（按 id；跨分類/跨頁保留）
const selectedIds = ref<Set<number>>(new Set());
const knownMedia = new Map<number, { url: string; name: string }>();
let listSequence = 0;
function rememberMedia(item: Pick<MediaItem, "id" | "url" | "name">) {
  knownMedia.set(item.id, item);
  const previous = [...selectedIds.value].find(id => id < 0 && knownMedia.get(id)?.url === item.url);
  if (previous !== undefined) {
    selectedIds.value = new Set([...selectedIds.value].map(id => id === previous ? item.id : id));
    knownMedia.delete(previous);
  }
}
const canManageSelection = computed(() => selectedIds.value.size > 0 && [...selectedIds.value].every(id => id > 0));
const selectedPreview = computed(() => [...selectedIds.value].map(id => ({ id, ...knownMedia.get(id) })));

// 本頁全選態（批量操作/全部刪除入口）
const pageAllSelected = computed(
  () => items.value.length > 0 && items.value.every((i) => selectedIds.value.has(i.id)),
);

function togglePageAll() {
  const next = new Set(selectedIds.value);
  if (pageAllSelected.value) {
    for (const i of items.value) next.delete(i.id);
  } else {
    for (const i of items.value) next.add(i.id);
  }
  selectedIds.value = next;
}

// 上傳 / 導入
const uploading = ref(0);
const showImport = ref(false);
const importURL = ref("");
const importing = ref(false);

// 刪除確認（兩段式：被引用清單）
const deleteConfirm = reactive({ show: false, ids: [] as number[], referenced: [] as MediaItem[] });

// 單素材改名
const renamingMedia = ref<MediaItem | null>(null);
const renameMediaText = ref("");

// 批量移動
const showMove = ref(false);
const moveTarget = ref<number | null>(null);

const treeCategories = computed(() => buildTree(categories.value));

function buildTree(
  rows: MediaCategory[],
): (MediaCategory & { children: MediaCategory[]; depth: number })[] {
  const map = new Map<number, MediaCategory & { children: MediaCategory[]; depth: number }>();
  for (const r of rows) map.set(r.id, { ...r, children: [], depth: 0 });
  const roots: (MediaCategory & { children: MediaCategory[]; depth: number })[] = [];
  for (const node of map.values()) {
    if (node.parent_id && map.has(node.parent_id)) {
      const parent = map.get(node.parent_id)!;
      node.depth = parent.depth + 1;
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
}

function flattenTree(nodes: { children: any[] }[], out: any[] = []) {
  for (const n of nodes) {
    out.push(n);
    if (n.children?.length) flattenTree(n.children, out);
  }
  return out;
}

async function loadList() {
  const sequence = ++listSequence;
  loading.value = true;
  try {
    const params: Record<string, any> = { page: page.value, page_size: pageSize.value, kind: mediaPickerState.kind };
    if (keyword.value) params.keyword = keyword.value;
    if (view.value === "uncategorized") params.uncategorized = true;
    else if (typeof view.value === "object") params.category_id = view.value.category;
    const { data, error } = await fetchMediaList(params);
    if (sequence !== listSequence) return;
    if (!error && data) {
      items.value = (data as any).items || [];
      total.value = (data as any).total || 0;
      items.value.forEach(rememberMedia);
    }
  } finally {
    if (sequence === listSequence) loading.value = false;
  }
}

async function loadCategories() {
  const { data, error } = await fetchMediaCategories();
  if (!error && data) categories.value = (data as any).categories || [];
}

function switchView(v: View) {
  view.value = v;
  page.value = 1;
  loadList();
}

watch(
  () => mediaPickerState.show,
  (show) => {
    if (show) {
      // 打開時復位視圖與選擇
      view.value = "all";
      page.value = 1;
      keyword.value = "";
      knownMedia.clear();
      const initial = mediaPickerState.multiple ? mediaPickerState.initialURLs : mediaPickerState.initialURLs.slice(0, 1);
      initial.forEach((url, index) => knownMedia.set(-index - 1, { url, name: "已選素材" }));
      selectedIds.value = new Set(initial.map((_, index) => -index - 1));
      loadCategories();
      loadList();
    }
  },
);

function toggleSelect(item: MediaItem, e: MouseEvent | KeyboardEvent) {
  e.stopPropagation();
  if (!mediaPickerState.multiple) {
    selectedIds.value = new Set([item.id]);
    return;
  }
  const next = new Set(selectedIds.value);
  if (next.has(item.id)) next.delete(item.id);
  else next.add(item.id);
  selectedIds.value = next;
}

function isSelected(item: MediaItem) {
  return selectedIds.value.has(item.id);
}

function handleConfirm() {
  const urls = [...new Set([...selectedIds.value].map(id => knownMedia.get(id)?.url).filter((url): url is string => Boolean(url)))];
  if (!urls.length) {
    window.$message?.warning("請先選擇素材");
    return;
  }
  settleMediaPicker(mediaPickerState.multiple ? urls : urls.slice(0, 1));
}

function handleCancel() {
  settleMediaPicker(null);
}

// ── 上傳（進當前分類）──

async function customRequest({ file, onFinish, onError }: UploadCustomRequestOptions) {
  uploading.value += 1;
  try {
    const raw = file.file as File;
    const base64 = await fileToBase64(raw);
    const { data, error } = await uploadMedia({
      name: raw.name,
      content_type: raw.type,
      data_base64: base64,
      category_id: currentCategoryId.value || undefined,
    });
    if (error || !data) {
      onError();
      return;
    }
    window.$message?.success("上傳成功");
    page.value = 1;
    await loadList();
    // 上傳即選中（單選覆蓋，多選追加）
    if (data.id) {
      rememberMedia(data);
      if (mediaPickerState.multiple) selectedIds.value = new Set([...selectedIds.value, data.id]);
      else selectedIds.value = new Set([data.id]);
    }
    onFinish();
  } finally {
    uploading.value -= 1;
  }
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result);
      resolve(result.slice(result.indexOf(",") + 1));
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}

function beforeUpload({ file }: { file: { file?: File | null } }) {
  if (file.file && file.file.size > 10 * 1024 * 1024) {
    window.$message?.error("圖片超過 10MB 上限，請壓縮後重試");
    return false;
  }
  // File extensions and browser MIME are unreliable; the server inspects bytes.
  return true;
}

// ── 外鏈導入 ──

async function handleImport() {
  if (!importURL.value.trim()) return;
  importing.value = true;
  try {
    const { data, error } = await importMediaFromURL({
      url: importURL.value.trim(),
      category_id: currentCategoryId.value || undefined,
    });
    if (!error) {
      window.$message?.success("導入成功");
      showImport.value = false;
      importURL.value = "";
      page.value = 1;
      loadList();
      if ((data as any)?.id) {
        rememberMedia(data as MediaItem);
        if (mediaPickerState.multiple) selectedIds.value = new Set([...selectedIds.value, (data as any).id]);
        else selectedIds.value = new Set([(data as any).id]);
      }
    }
  } finally {
    importing.value = false;
  }
}

// ── 批量移動 ──

async function handleMove() {
  if (!canManageSelection.value || moveTarget.value === null) return;
  const { error } = await moveMedia([...selectedIds.value].filter(id => id > 0), moveTarget.value);
  if (!error) {
    window.$message?.success("已移動");
    showMove.value = false;
    moveTarget.value = null;
    loadList();
  }
}

// ── 批量刪除（兩段式引用確認）──

async function handleDelete(force = false) {
  const ids = deleteConfirm.show ? deleteConfirm.ids : [...selectedIds.value].filter(id => id > 0);
  if (!ids.length) return;
  const { data, error } = await deleteMedia(ids, force);
  if (error) return;
  const reply = data as any;
  if (reply?.need_confirm) {
    deleteConfirm.ids = ids;
    deleteConfirm.referenced = reply.referenced || [];
    deleteConfirm.show = true;
    return;
  }
  window.$message?.success(`已刪除 ${reply?.deleted ?? ids.length} 項`);
  deleteConfirm.show = false;
  selectedIds.value = new Set();
  loadList();
}

// ── 分類管理 ──

async function handleCreateCategory() {
  if (!newCatName.value.trim()) return;
  const { error } = await createMediaCategory({
    name: newCatName.value.trim(),
    parent_id: newCatParent.value || undefined,
  });
  if (!error) {
    window.$message?.success("分類已創建");
    newCatName.value = "";
    newCatParent.value = null;
    showCatCreate.value = false;
    loadCategories();
  }
}

async function handleRenameCategory() {
  if (!renamingCat.value || !renameCatText.value.trim()) return;
  const { error } = await renameMediaCategory(renamingCat.value.id, renameCatText.value.trim());
  if (!error) {
    window.$message?.success("已重命名");
    renamingCat.value = null;
    loadCategories();
  }
}

async function handleDeleteCategory(cat: MediaCategory) {
  const { error } = await deleteMediaCategory(cat.id);
  if (!error) {
    window.$message?.success("分類已刪除");
    if (typeof view.value === "object" && view.value.category === cat.id) view.value = "all";
    deleteCatConfirm.show = false;
    loadCategories();
    loadList();
  }
}

// ── 分類行 ⋯ 菜單（重命名/移動/刪除）──

const catMenuOptions: DropdownOption[] = [
  { label: "重命名", key: "rename" },
  { label: "移動分類到…", key: "move" },
  {
    label: "刪除分類",
    key: "delete",
    divided: true,
    props: { style: "color: var(--error-color, #d03050)" },
  },
];

function handleCatMenu(key: string | number, cat: MediaCategory) {
  switch (key) {
    case "rename":
      renamingCat.value = cat;
      renameCatText.value = cat.name;
      break;
    case "move":
      movingCat.value = cat;
      moveCatTarget.value = cat.parent_id || null;
      break;
    case "delete":
      deleteCatConfirm.cat = cat;
      deleteCatConfirm.show = true;
      break;
  }
}

// descendantIds 自身 + 全部子孫（BFS）——移動目標的排除集（前端防環第一道；
// 服務端 MoveCategory 追溯父鏈 fail-closed 兜底）
function descendantIds(id: number): Set<number> {
  const out = new Set<number>([id]);
  const queue = [id];
  while (queue.length) {
    const cur = queue.shift()!;
    for (const c of categories.value) {
      if (c.parent_id === cur && !out.has(c.id)) {
        out.add(c.id);
        queue.push(c.id);
      }
    }
  }
  return out;
}

// moveCatTree 可選目標樹（排除移動分類自身子樹；含縮進深度）
const moveCatTree = computed(() => {
  if (!movingCat.value) return [];
  const excluded = descendantIds(movingCat.value.id);
  const filterTree = (nodes: any[]): any[] =>
    nodes
      .filter((n) => !excluded.has(n.id))
      .map((n) => ({ ...n, children: filterTree(n.children) }));
  return flattenTree(filterTree(treeCategories.value));
});

// 當前父分類名（移動彈窗位置提示）
const moveCatParentName = computed(() => {
  if (!movingCat.value) return "";
  if (!movingCat.value.parent_id) return "根分類";
  return categories.value.find((c) => c.id === movingCat.value!.parent_id)?.name || "根分類";
});

async function handleMoveCategory() {
  if (!movingCat.value) return;
  const target = moveCatTarget.value ?? 0; // null = 根
  if (target === movingCat.value.id) return;
  const { error } = await moveMediaCategory(movingCat.value.id, target);
  if (!error) {
    const to = target === 0 ? "根分類" : categories.value.find((c) => c.id === target)?.name || "";
    window.$message?.success(`已移動到「${to}」`);
    movingCat.value = null;
    loadCategories();
  }
}

// ── 單素材操作 ──

function copyURL(url: string) {
  navigator.clipboard?.writeText(url).then(
    () => window.$message?.success("鏈接已複製"),
    () => window.$message?.error("複製失敗"),
  );
}

async function handleRenameMedia() {
  if (!renamingMedia.value || !renameMediaText.value.trim()) return;
  const { error } = await renameMedia(renamingMedia.value.id, renameMediaText.value.trim());
  if (!error) {
    window.$message?.success("已重命名");
    renamingMedia.value = null;
    loadList();
  }
}

const categorySelectOptions = computed(() => [
  { label: "未分類", value: 0 },
  ...flattenTree(treeCategories.value).map((c) => ({
    label: `${"　".repeat(c.depth)}${c.name}`,
    value: c.id,
  })),
]);
</script>

<template>
  <NModal
    :show="mediaPickerState.show"
    preset="card"
    :title="mediaPickerState.kind === 'video' ? '選擇視頻' : mediaPickerState.multiple ? '選擇圖片（可多選）' : '選擇圖片'"
    class="w-960px max-w-[calc(100vw-24px)]"
    style="max-height: calc(100dvh - 24px); overflow-y: auto"
    :mask-closable="false"
    @update:show="(v: boolean) => !v && handleCancel()"
  >
    <p v-if="mediaPickerState.tip" class="mb-12px text-13px" style="color: var(--text-color-2); overflow-wrap: anywhere">{{ mediaPickerState.tip }}</p>
    <NSelect class="mb-12px sm:hidden" :value="viewKey" :options="[{ label: '全部素材', value: 'all' }, { label: '未分類', value: 'uncategorized' }, ...categories.map(c => ({ label: c.name, value: `cat:${c.id}` }))]" @update:value="(value: string) => switchView(value === 'all' || value === 'uncategorized' ? value : { category: Number(value.slice(4)) })" />
    <div class="flex gap-16px" style="height: min(560px, 62dvh)">
      <!-- 左：素材網格 -->
      <div class="flex min-w-0 flex-1 flex-col gap-12px">
        <div class="flex flex-wrap items-center gap-8px">
          <NUpload v-if="mediaPickerState.kind === 'image'"
            :custom-request="customRequest"
            :show-file-list="false"
            accept="image/*,.apng,.avif,.jfif,.tif,.tiff,.heic,.heif,.ico"
            :on-before-upload="beforeUpload"
          >
            <NButton v-auth="'media:upload'" size="small" type="primary" :loading="uploading > 0">上傳圖片</NButton>
          </NUpload>
          <NButton v-if="mediaPickerState.kind === 'image'" v-auth="'media:upload'" size="small" @click="showImport = true">外鏈導入</NButton>
          <NButton v-if="mediaPickerState.multiple" size="small" quaternary @click="togglePageAll">
            {{ pageAllSelected ? "取消全選" : "全選本頁" }}
          </NButton>
          <NButton v-auth="'media:write'" size="small" :disabled="!canManageSelection" title="請先在素材列表中選中要管理的素材" @click="showMove = true">
            移動到分類{{ selectedIds.size ? `（${selectedIds.size}）` : "" }}
          </NButton>
          <NPopconfirm @positive-click="handleDelete(false)">
            <template #trigger>
              <NButton v-auth="'media:delete'" size="small" type="error" ghost :disabled="!canManageSelection" title="請先在素材列表中選中要管理的素材">
                刪除{{ selectedIds.size ? `（${selectedIds.size}）` : "" }}
              </NButton>
            </template>
            確定刪除選中的 {{ selectedIds.size }} 項素材？（被商品引用的會再次確認）
          </NPopconfirm>
          <div class="ml-auto w-180px">
            <NInput
              v-model:value="keyword"
              size="small"
              placeholder="搜索素材名"
              clearable
              @keyup.enter="
                page = 1;
                loadList();
              "
            />
          </div>
        </div>

        <NScrollbar class="flex-1">
          <div v-if="loading" class="flex h-320px items-center justify-center text-gray-400">
            加載中…
          </div>
          <NEmpty
            v-else-if="!items.length"
            class="mt-80px"
            :description="mediaPickerState.kind === 'video' ? '暫無視頻，請返回插入視頻窗口上傳' : '暫無素材，點擊上傳圖片或外鏈導入'"
          />
          <div v-else class="grid grid-cols-2 gap-10px sm:grid-cols-5">
            <div
              v-for="item in items"
              :key="item.id"
              class="group relative cursor-pointer overflow-hidden rounded-6px border"
              :class="isSelected(item) ? 'border-primary' : 'border-gray-200 dark:border-gray-700'"
              :style="isSelected(item) ? 'box-shadow: 0 0 0 2px var(--primary-color)' : ''"
              role="button" tabindex="0" :aria-label="`選擇素材 ${item.name}`" :aria-pressed="isSelected(item)"
              @keydown.enter.prevent="toggleSelect(item, $event)" @keydown.space.prevent="toggleSelect(item, $event)"
              @click="toggleSelect(item, $event)"
            >
              <video v-if="item.mime.startsWith('video/')" :src="resolveMediaUrl(item.url)" preload="none" muted playsinline style="width:100%;height:110px;object-fit:contain;background:#111" />
              <img v-else :src="resolveMediaUrl(item.url)" :alt="item.name" loading="lazy" class="block w-full h-110px object-cover" />
              <div class="flex items-center justify-between px-4px py-2px text-11px text-gray-500">
                <span class="truncate">{{ item.name }}</span>
                <NTag v-if="item.ref_count > 0" size="tiny" type="warning" :bordered="false"
                  >引用{{ item.ref_count }}</NTag
                >
              </div>
              <!-- 選擇角標 -->
              <div
                class="absolute right-4px top-4px h-18px w-18px flex items-center justify-center rounded-full text-12px"
                :class="
                  isSelected(item)
                    ? 'bg-primary text-white'
                    : 'bg-black/30 text-transparent group-hover:bg-black/50'
                "
              >
                ✓
              </div>
              <!-- hover 操作 -->
              <div
                class="absolute inset-x-0 bottom-20px hidden justify-center gap-4px group-hover:flex"
              >
                <NButton size="tiny" quaternary @click.stop="copyURL(item.url)">複製鏈接</NButton>
                <NButton
                  v-auth="'media:write'"
                  size="tiny"
                  quaternary
                  @click.stop="((renamingMedia = item), (renameMediaText = item.name))"
                >
                  改名
                </NButton>
              </div>
            </div>
          </div>
        </NScrollbar>

        <div class="hidden sm:block"><TablePager
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[24, 48, 96]"
          @change="loadList"
        /></div>
        <div class="sm:hidden flex flex-wrap items-center justify-between gap-8px mt-12px">
          <span class="text-12px text-gray-400">共 {{ total }} 項</span>
          <NPagination v-model:page="page" :page-size="pageSize" :item-count="total" simple size="small" @update:page="loadList" />
        </div>
      </div>

      <!-- 右：分類面板 -->
      <div
        class="hidden sm:flex w-190px shrink-0 flex-col border-l border-gray-200 pl-12px dark:border-gray-700"
      >
        <div class="mb-8px flex items-center justify-between">
          <span class="text-13px font-medium">素材分類</span>
          <NButton
            v-auth="'media:write'"
            size="tiny"
            quaternary
            type="primary"
            @click="showCatCreate = !showCatCreate"
            >+ 新建</NButton
          >
        </div>

        <!-- 新建分類 -->
        <div v-if="showCatCreate" class="mb-8px flex flex-col gap-4px">
          <NInput
            v-model:value="newCatName"
            size="small"
            placeholder="分類名稱"
            @keyup.enter="handleCreateCategory"
          />
          <NSelect
            v-model:value="newCatParent"
            size="small"
            placeholder="父分類（可選）"
            clearable
            :options="categorySelectOptions.filter((o: any) => o.value !== 0)"
          />
          <NButton size="tiny" type="primary" @click="handleCreateCategory">創建</NButton>
        </div>

        <NScrollbar class="flex-1">
          <div
            class="flex cursor-pointer items-center justify-between rounded-4px px-8px py-5px text-13px"
            :class="
              viewKey === 'all'
                ? 'bg-primary-100 font-medium text-primary dark:bg-darkBorderDark'
                : 'hover:bg-gray-100 dark:hover:bg-gray-800'
            "
            @click="switchView('all')"
          >
            <span>全部素材</span>
            <span class="text-11px text-gray-400">{{
              total && viewKey === "all" ? total : ""
            }}</span>
          </div>
          <div
            class="flex cursor-pointer items-center justify-between rounded-4px px-8px py-5px text-13px"
            :class="
              viewKey === 'uncategorized'
                ? 'bg-primary-100 font-medium text-primary dark:bg-darkBorderDark'
                : 'hover:bg-gray-100 dark:hover:bg-gray-800'
            "
            @click="switchView('uncategorized')"
          >
            <span>未分類</span>
          </div>

          <template v-for="cat in flattenTree(treeCategories)" :key="cat.id">
            <div
              class="group flex cursor-pointer items-center justify-between rounded-4px px-8px py-5px text-13px"
              :class="
                viewKey === `cat:${cat.id}`
                  ? 'bg-primary-100 font-medium text-primary dark:bg-darkBorderDark'
                  : 'hover:bg-gray-100 dark:hover:bg-gray-800'
              "
              :style="{ paddingLeft: `${8 + cat.depth * 12}px` }"
              @click="switchView({ category: cat.id })"
            >
              <span class="truncate">{{ cat.name }}</span>
              <span class="hidden group-hover:flex">
                <NDropdown
                  :options="catMenuOptions"
                  trigger="click"
                  @select="(key: string | number) => handleCatMenu(key, cat)"
                >
                  <NButton v-auth="'media:write'" size="tiny" quaternary @click.stop>⋯</NButton>
                </NDropdown>
              </span>
            </div>
          </template>
          <NEmpty v-if="!categories.length" size="small" class="mt-24px" description="暫無分類" />
        </NScrollbar>
      </div>
    </div>

    <template #footer>
      <div class="flex flex-wrap justify-end gap-12px">
        <NButton @click="handleCancel">取消</NButton>
        <div v-if="selectedPreview.length" class="order-first w-full flex flex-wrap gap-8px" aria-label="已選素材">
          <NTag v-for="item in selectedPreview" :key="item.id" closable @close="selectedIds = new Set([...selectedIds].filter(id => id !== item.id))"><span class="inline-block max-w-180px truncate">{{ item.name }}</span></NTag>
        </div>
        <NButton type="primary" :disabled="!selectedIds.size || loading" @click="handleConfirm">
          確定{{ selectedIds.size ? `（已選 ${selectedIds.size} 張）` : "" }}
        </NButton>
      </div>
    </template>

    <!-- 外鏈導入 -->
    <NModal v-model:show="showImport" preset="dialog" title="外鏈導入" style="width: 480px">
      <NInput
        v-model:value="importURL"
        placeholder="圖片 URL（服務端 SSRF 防護 + 安全三件套校驗）"
      />
      <template #action>
        <NButton @click="showImport = false">取消</NButton>
        <NButton type="primary" :loading="importing" @click="handleImport">導入</NButton>
      </template>
    </NModal>

    <!-- 批量移動 -->
    <NModal v-model:show="showMove" preset="dialog" title="移動到分類" style="width: 420px">
      <NSelect
        v-model:value="moveTarget"
        :options="categorySelectOptions"
        placeholder="選擇目標分類"
      />
      <template #action>
        <NButton @click="showMove = false">取消</NButton>
        <NButton type="primary" @click="handleMove">移動</NButton>
      </template>
    </NModal>

    <!-- 引用確認刪除（兩段式第二段） -->
    <NModal v-model:show="deleteConfirm.show" preset="dialog" title="刪除確認" style="width: 520px">
      <div class="mb-8px">
        以下 <b>{{ deleteConfirm.referenced.length }}</b> 張素材正被商品引用（共選中
        {{ deleteConfirm.ids.length }} 張），刪除後相關商品圖片將裂圖：
      </div>
      <div class="max-h-200px overflow-auto rounded-4px bg-gray-50 p-8px dark:bg-gray-800">
        <div
          v-for="item in deleteConfirm.referenced"
          :key="item.id"
          class="flex items-center gap-8px py-2px text-12px"
        >
          <img :src="resolveMediaUrl(item.url)" class="h-24px w-24px rounded-2px object-cover" />
          <span class="truncate">{{ item.name }}</span>
          <NTag size="tiny" type="warning">引用 {{ item.ref_count }} 處</NTag>
        </div>
      </div>
      <template #action>
        <NButton @click="deleteConfirm.show = false">取消</NButton>
        <NButton type="error" @click="handleDelete(true)">仍要刪除</NButton>
      </template>
    </NModal>

    <!-- 素材改名 -->
    <NModal
      :show="!!renamingMedia"
      preset="dialog"
      title="素材改名"
      style="width: 420px"
      @update:show="(v: boolean) => !v && (renamingMedia = null)"
    >
      <NInput v-model:value="renameMediaText" @keyup.enter="handleRenameMedia" />
      <template #action>
        <NButton @click="renamingMedia = null">取消</NButton>
        <NButton type="primary" @click="handleRenameMedia">確定</NButton>
      </template>
    </NModal>

    <!-- 分類重命名 -->
    <NModal
      :show="!!renamingCat"
      preset="dialog"
      title="分類重命名"
      style="width: 420px"
      @update:show="(v: boolean) => !v && (renamingCat = null)"
    >
      <NInput v-model:value="renameCatText" @keyup.enter="handleRenameCategory" />
      <template #action>
        <NButton @click="renamingCat = null">取消</NButton>
        <NButton type="primary" @click="handleRenameCategory">確定</NButton>
      </template>
    </NModal>

    <!-- 移動分類（樹選擇器；排除自身子樹防環；根分類=移到頂層） -->
    <NModal
      :show="!!movingCat"
      preset="dialog"
      title="移動分類"
      style="width: 460px"
      @update:show="(v: boolean) => !v && (movingCat = null)"
    >
      <div class="mb-8px text-13px text-gray-500">
        「{{ movingCat?.name }}」當前位於：<b>{{ moveCatParentName }}</b
        >，選擇新的上級分類：
      </div>
      <NScrollbar class="max-h-280px rounded-4px border border-gray-200 dark:border-gray-700">
        <div
          class="flex cursor-pointer items-center justify-between px-12px py-7px text-13px"
          :class="
            moveCatTarget === null
              ? 'bg-primary-100 font-medium text-primary'
              : 'hover:bg-gray-100 dark:hover:bg-gray-800'
          "
          @click="moveCatTarget = null"
        >
          <span>📁 根分類（作為頂級分類）</span>
          <span v-if="moveCatTarget === null" class="text-primary">✓</span>
        </div>
        <div
          v-for="cat in moveCatTree"
          :key="cat.id"
          class="flex cursor-pointer items-center justify-between py-7px pr-12px text-13px"
          :class="
            moveCatTarget === cat.id
              ? 'bg-primary-100 font-medium text-primary'
              : 'hover:bg-gray-100 dark:hover:bg-gray-800'
          "
          :style="{ paddingLeft: `${12 + cat.depth * 16}px` }"
          @click="moveCatTarget = cat.id"
        >
          <span class="truncate">{{ cat.name }}</span>
          <span v-if="moveCatTarget === cat.id" class="text-primary">✓</span>
        </div>
      </NScrollbar>
      <div class="mt-6px text-12px text-gray-400">不能移動到自身或其子分類下（防環）。</div>
      <template #action>
        <NButton @click="movingCat = null">取消</NButton>
        <NButton type="primary" @click="handleMoveCategory">移動到此處</NButton>
      </template>
    </NModal>

    <!-- 分類刪除確認（⋯ 菜單無法包 Popconfirm → NModal） -->
    <NModal
      :show="deleteCatConfirm.show"
      preset="dialog"
      title="刪除分類"
      style="width: 420px"
      @update:show="(v: boolean) => !v && (deleteCatConfirm.show = false)"
    >
      確定刪除分類「{{ deleteCatConfirm.cat?.name }}」？分類下有素材或子分類時將被拒絕。
      <template #action>
        <NButton @click="deleteCatConfirm.show = false">取消</NButton>
        <NButton
          type="error"
          @click="deleteCatConfirm.cat && handleDeleteCategory(deleteCatConfirm.cat)"
          >刪除</NButton
        >
      </template>
    </NModal>
  </NModal>
</template>
