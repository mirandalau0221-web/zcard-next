<script setup lang="ts">
/**
 * 可複用遠程分頁條（所有列表共用）：
 *   offset 模式（默認）：共 N 條 / M 頁 + 首頁« + 頁碼/上一頁/下一頁/每頁條數/跳頁 + 末頁»
 *   cursor 模式（訂單/流水等遊標分頁——架構禁 OFFSET 深翻頁）：首頁« + 上一頁/下一頁 +
 *     第 N 頁 + 每頁條數（跳頁/末頁與遊標語義衝突，不提供；hasMore 控制下一頁可用）
 * change 事件統一回調（page 與 pageSize 任一變化都觸發；改條數自動回第 1 頁）。
 */
import { computed } from "vue";
import { NButton, NPagination, NSelect } from "naive-ui";

const props = withDefaults(
  defineProps<{
    page: number;
    pageSize: number;
    /** offset 模式必填總條數；cursor 模式忽略 */
    total?: number;
    /** cursor 模式：是否還有下一頁 */
    hasMore?: boolean;
    mode?: "offset" | "cursor";
    pageSizes?: number[];
  }>(),
  { total: 0, hasMore: false, mode: "offset", pageSizes: () => [10, 20, 50, 100] },
);

const emit = defineEmits<{
  (e: "update:page", v: number): void;
  (e: "update:page-size", v: number): void;
  (e: "change", page: number, size: number): void;
}>();

const pageCount = computed(() =>
  props.mode === "offset" ? Math.max(1, Math.ceil(props.total / props.pageSize)) : 0,
);

function changePage(p: number) {
  if (p === props.page) return;
  emit("update:page", p);
  emit("change", p, props.pageSize);
}

function changeSize(s: number) {
  // 改每頁條數回第 1 頁（尾頁數據不足時停留舊頁會空白）
  emit("update:page-size", s);
  emit("update:page", 1);
  emit("change", 1, s);
}

function go(p: number) {
  if (props.mode === "offset") {
    changePage(Math.min(Math.max(1, p), pageCount.value));
  } else {
    changePage(Math.max(1, p));
  }
}
</script>

<template>
  <div
    class="mt-12px flex flex-wrap items-center justify-between gap-8px"
    style="position: sticky; bottom: 0; z-index: 5; padding-top: 8px; background: var(--n-color, #fff);"
  >
    <span class="text-12px text-gray-400">
      {{ mode === "offset" ? `共 ${total} 條 · ${pageCount} 頁` : `第 ${page} 頁` }}
    </span>
    <div class="flex items-center gap-4px">
      <NButton size="small" quaternary :disabled="page <= 1" title="第一頁" @click="go(1)"
        >«</NButton
      >

      <!-- offset：完整分頁（頁碼/每頁條數/跳頁） -->
      <NPagination
        v-if="mode === 'offset'"
        :page="page"
        :page-size="pageSize"
        :item-count="total"
        :page-sizes="pageSizes"
        show-size-picker
        show-quick-jumper
        size="small"
        @update:page="changePage"
        @update:page-size="changeSize"
      />

      <!-- cursor：上一頁/下一頁（頁碼鏈） + 每頁條數 -->
      <template v-else>
        <NButton size="small" quaternary :disabled="page <= 1" title="上一頁" @click="go(page - 1)"
          >‹</NButton
        >
        <span class="px-4px text-13px">{{ page }}</span>
        <NButton size="small" quaternary :disabled="!hasMore" title="下一頁" @click="go(page + 1)"
          >›</NButton
        >
        <NSelect
          :value="pageSize"
          size="small"
          class="w-100px"
          :options="pageSizes.map((s: number) => ({ label: `${s} 條/頁`, value: s }))"
          :consistent-menu-width="false"
          @update:value="changeSize"
        />
      </template>

      <NButton
        v-if="mode === 'offset'"
        size="small"
        quaternary
        :disabled="page >= pageCount"
        title="最後一頁"
        @click="go(pageCount)"
      >
        »
      </NButton>
    </div>
  </div>
</template>
