<template>
  <aside class="cat-tree" :class="{ 'cat-tree--panel': variant === 'panel' }" :style="{ '--cat-tree-top': `${stickyTop}px` }">
    <div class="cat-tree-card">
      <!-- 標題欄：品牌豎條 + 標題 + 分類數（與右側「全部商品」區標題同一設計語言） -->
      <button class="cat-tree-head" type="button" :aria-expanded="allExpanded" :disabled="!branchIds.length" @click="toggleAll">
        <span class="head-bar"></span>
        <span class="cat-tree-title">{{ $t('全部分類') }}</span>
        <span v-if="categories.length" class="cat-tree-count">{{ categories.length }} {{ $t('類') }}</span>
        <span v-if="branchIds.length" class="cat-tree-toggle-label">{{ allExpanded ? $t('摺疊下級') : $t('展開下級') }}</span>
      </button>
      <div class="cat-tree-body">
        <!-- 全部商品入口 -->
        <button
          class="tree-all"
          :class="{ active: modelValue === 0 }"
          @click="select(0)"
        >
          <ThemeIcon name="grid" />
          <span class="flex-1 text-left">{{ $t('全部商品') }}</span>
        </button>
        <button v-if="showRecommended" type="button" class="tree-all tree-recommended" :class="{ active: modelValue === -1 }" @click="select(-1)">
          <ThemeIcon name="folder" />
          <span>{{ $t('推薦商品') }}</span>
        </button>
        <!-- 分類樹：遞歸渲染任意層級（三級/四級均可展開） -->
        <CategoryTreeNode
          v-for="c in tree"
          :key="c.id"
          :node="c"
          :depth="0"
          :expanded="expanded"
          :model-value="modelValue"
          @select="select"
          @toggle="toggle"
        />
        <div v-if="loading && !tree.length" class="tree-empty muted" role="status">{{ $t('正在加載分類…') }}</div>
        <div v-else-if="error" class="tree-empty" role="alert">{{ $t('分類加載失敗') }} <button class="btn secondary" type="button" :disabled="loading" @click="$emit('retry')">{{ $t('重試') }}</button></div>
        <div v-else-if="!tree.length" class="tree-empty muted">{{ $t('暫無分類') }}</div>
      </div>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { t as $t } from '@/i18n';

import ThemeIcon from '@/components/ThemeIcon.vue';
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue';
import CategoryTreeNode from './CategoryTreeNode.vue';
import type { CategoryItem } from '@/api';

// variant：sidebar=PC 左側欄（默認）；panel=移動端摺疊面板（全寬、限高滾動）
const props = withDefaults(
  defineProps<{
    categories: CategoryItem[];
    loading?: boolean;
    error?: boolean;
    /** 當前選中分類 id（0=全部，-1=推薦；僅前端篩選值） */
    modelValue: number;
    showRecommended?: boolean;
    variant?: 'sidebar' | 'panel';
  }>(),
  { variant: 'sidebar' },
);
const emit = defineEmits<{
  (e: 'update:modelValue', v: number): void;
  (e: 'retry'): void;
}>();

// 跟隨導航實際高度，避免平板換行或自定義站名把分類欄遮住。
const stickyTop = ref(72);
let headerObserver: ResizeObserver | undefined;
onMounted(() => {
  if (props.variant === 'panel') return;
  const header = document.querySelector('.topbar');
  if (!header) return;
  const measure = () => { stickyTop.value = header.getBoundingClientRect().height + 12; };
  measure();
  headerObserver = new ResizeObserver(measure);
  headerObserver.observe(header);
});
onBeforeUnmount(() => headerObserver?.disconnect());

// 分類樹（任意層級：parent_id 鏈構建 children；一級為根）
const tree = computed(() => {
  const map = new Map<number, any>();
  for (const c of props.categories) map.set(c.id, { ...c, children: [] });
  const rootsArr: any[] = [];
  for (const node of map.values()) {
    if (node.parent_id && map.has(node.parent_id)) map.get(node.parent_id)!.children.push(node);
    else rootsArr.push(node);
  }
  return rootsArr;
});

// 多級分類默認收起，選中深層分類時只展開其祖先。
const expanded = ref<Set<number>>(new Set());
const branchIds = computed(() => [...new Set(props.categories.filter((c) => c.parent_id).map((c) => c.parent_id!))]);
const allExpanded = computed(() => branchIds.value.length > 0 && branchIds.value.every((id) => expanded.value.has(id)));

function toggleAll() {
  expanded.value = new Set(allExpanded.value ? [] : branchIds.value);
}

watch(() => [props.modelValue, props.categories] as const, () => {
  const byId = new Map(props.categories.map((c) => [c.id, c]));
  const next = new Set(expanded.value);
  const seen = new Set<number>();
  let parent = byId.get(props.modelValue)?.parent_id;
  while (parent && !seen.has(parent)) {
    seen.add(parent);
    next.add(parent);
    parent = byId.get(parent)?.parent_id;
  }
  expanded.value = next;
}, { immediate: true });

function toggle(id: number) {
  const s = new Set(expanded.value);
  if (s.has(id)) s.delete(id);
  else s.add(id);
  expanded.value = s;
}

function select(id: number) {
  emit('update:modelValue', id);
}
</script>

<style scoped>
.cat-tree {
  display: none;
  width: 280px;
  flex-shrink: 0;
}
@media (min-width: 768px) {
  /* 吸頂必須放在參與首頁兩欄佈局的外層，才能沿整個商品區滾動。 */
  .cat-tree { display: block; position: sticky; top: var(--cat-tree-top, 72px); }
}
.cat-tree-card {
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  max-height: calc(100vh - var(--cat-tree-top, 72px) - 16px);
  max-height: calc(100dvh - var(--cat-tree-top, 72px) - 16px);
}
.cat-tree-head {
  flex-shrink: 0;
  width: 100%; border: none; font: inherit; text-align: left; cursor: pointer;
  display: flex; align-items: center; gap: 8px;
  padding: 14px 16px;
  border-bottom: 1px solid #e5e7eb;
  background: #f8fafc;
}
.cat-tree-toggle-label { font-size: 12px; color: #6b7280; white-space: nowrap; }
.head-bar {
  width: 4px; height: 16px; border-radius: 999px;
  background: #ff5722; display: inline-block; flex-shrink: 0;
}
.cat-tree-title { font-size: 16px; font-weight: 700; color: #111827; letter-spacing: 0.5px; }
.cat-tree-count {
  margin-left: auto;
  font-size: 12px; font-weight: 500; color: var(--zc-primary);
  background: color-mix(in srgb, var(--zc-primary) 8%, transparent);
  padding: 2px 9px; border-radius: 999px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.cat-tree-body {
  padding: 12px 10px; min-height: 0; overflow-y: auto;
  overscroll-behavior-y: contain;
  scrollbar-width: thin;
  scrollbar-color: #cbd5e1 transparent;
  scrollbar-gutter: stable;
  display: flex; flex-direction: column; gap: 4px;
}
.cat-tree-body > * { flex-shrink: 0; }

.tree-all {
  width: 100%;
  display: flex; align-items: center; gap: 9px;
  min-height: 48px;
  padding: 12px;
  border: none; background: none; cursor: pointer;
  border-radius: 8px; font-size: 15px; color: #374151;
  transition: all 0.15s; font-family: inherit;
  text-align: left;
}
.tree-all:hover { background: var(--zc-primary-soft); color: var(--zc-primary); }
.tree-all.active { background: var(--zc-primary); color: #fff; font-weight: 600; box-shadow: 0 2px 6px color-mix(in srgb, var(--zc-primary) 25%, transparent); }
.tree-all > span:first-child { font-size: 16px; }
.tree-empty { padding: 16px 0; text-align: center; }

/* 面板變體（移動端摺疊面板內嵌）：全寬平鋪、保留一級分類、限高滾動。
   雙類名提升優先級，覆蓋基礎 .cat-tree 的移動端 display:none */
.cat-tree.cat-tree--panel { display: block; width: 100%; position: static; }
.cat-tree--panel .cat-tree-card { max-height: none; border: none; border-radius: 0; }
.cat-tree-head:disabled { cursor: default; color: inherit; }
.cat-tree--panel .cat-tree-body { max-height: 56vh; padding: 4px 4px 8px; }
</style>
