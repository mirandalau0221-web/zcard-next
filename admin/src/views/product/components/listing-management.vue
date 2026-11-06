<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NDataTable, NModal, NSelect, NSpace } from "naive-ui";
import { manageProductListing } from "@/service/api";
const props = defineProps<{
  show: boolean;
  ids: number[];
  filter: Record<string, unknown>;
  initialAction: string;
  scopeLabel: string;
}>();
const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "saved"): void;
}>();
const action = ref("enable");
const busy = ref(false);
const error = ref("");
const result = ref<any>(null);
const applied = ref(false);
const processed = ref(0);
const actions = [
  { label: "開啓自動上下架", value: "enable" },
  { label: "暫停自動上下架", value: "disable" },
  { label: "人工上架", value: "on" },
  { label: "人工下架", value: "off" },
  { label: "人工隱藏", value: "hide" },
];
const ready = computed(
  () => result.value?.items?.filter((p: any) => p.result === "ready") || [],
);
const summary = ref("");
const labels: Record<string, string> = {
  ready: "可處理",
  locked: "已鎖定 · 跳過",
  unsupported: "不支持 · 跳過",
  conflict: "已變化 · 請重新預覽",
  updated: "已完成",
  failed: "失敗 · 可重新預覽",
};
const columns = [
  { title: "商品", key: "name", minWidth: 160 },
  {
    title: "當前狀態",
    key: "status",
    width: 90,
    render: (p: any) =>
      p.status === 1 ? "已上架" : p.status === 2 ? "已隱藏" : "已下架",
  },
  {
    title: "處理結果",
    key: "result",
    width: 160,
    render: (p: any) => labels[p.result] || p.result,
  },
  { title: "説明", key: "message", minWidth: 230 },
];
async function preview() {
  busy.value = true;
  error.value = "";
  result.value = null;
  applied.value = false;
  summary.value = "";
  processed.value = 0;
  try {
    const r = await manageProductListing({
      action: action.value,
      ids: props.ids.length ? props.ids : undefined,
      filter: props.ids.length ? undefined : props.filter,
    });
    if (r.error) {
      error.value =
        (r.error as any).response?.data?.message || "預覽失敗，請重試";
      return;
    }
    result.value = r.data;
  } finally {
    busy.value = false;
  }
}
watch(
  () => props.show,
  (show) => {
    if (show) {
      action.value = props.initialAction;
      void preview();
    }
  },
);
async function apply() {
  const targets = [...ready.value];
  if (!targets.length || result.value?.truncated) return;
  busy.value = true;
  error.value = "";
  let changed = 0,
    locked = Number(result.value.skipped_locked || 0),
    conflicts = 0,
    unsupported = Number(result.value.unsupported || 0),
    failed = 0;
  try {
    for (let start = 0; start < targets.length; start += 100) {
      const chunk = targets.slice(start, start + 100);
      const r = await manageProductListing({
        action: action.value,
        apply: true,
        revisions: Object.fromEntries(
          chunk.map((p: any) => [p.id, p.revision]),
        ),
      });
      if (r.error) {
        error.value =
          "本批結果未能確認，已完成部分會保留。重新預覽後可繼續，系統會檢查商品是否發生變化。";
        break;
      }
      const d = r.data;
      changed += Number(d.changed || 0);
      locked += Number(d.skipped_locked || 0);
      conflicts += Number(d.conflicts || 0);
      unsupported += Number(d.unsupported || 0);
      failed += Number(d.failed || 0);
      const byId = new Map(d.items.map((p: any) => [String(p.id), p]));
      result.value.items = result.value.items.map(
        (p: any) => byId.get(String(p.id)) || p,
      );
      processed.value += chunk.length;
    }
    applied.value = true;
    summary.value = `已完成 ${changed} 件，鎖定跳過 ${locked} 件，狀態變化 ${conflicts} 件，不支持 ${unsupported} 件，失敗 ${failed} 件。`;
    emit("saved");
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <NModal
    :show="show"
    preset="card"
    title="管理商品上下架"
    style="width: 960px; max-width: 96vw"
    :closable="!busy"
    :mask-closable="false"
    :close-on-esc="!busy"
    @update:show="emit('update:show', $event)"
  >
    <NSpace vertical :size="12">
      <div>範圍：{{ scopeLabel }}。先預覽，再應用；鎖定商品自動跳過。</div>
      <NSelect
        v-model:value="action"
        :options="actions"
        :disabled="busy"
        aria-label="上下架操作"
        @update:value="preview"
      />
      <NAlert v-if="action === 'enable'" type="info" :bordered="false"
        >僅支持全部規格由上游發貨的商品。開啓後後台約每 5
        分鐘調度一次完整檢查，大目錄需排隊；兩次獨立確認缺貨才下架。當前已下架商品也會在確認有貨後上架，原本因缺貨下架的隱藏商品恢復隱藏。人工上下架會暫停自動管理。</NAlert
      >
      <NAlert v-else type="info" :bordered="false">{{
        action === "disable"
          ? "暫停自動上下架，保留當前銷售狀態。"
          : "本次人工設置會暫停所選商品的自動上下架。"
      }}</NAlert>
      <NAlert v-if="error" type="error" role="alert"
        >{{ error }}
        <NButton :disabled="busy" size="small" @click="preview"
          >重新預覽</NButton
        ></NAlert
      >
      <NAlert v-if="result?.truncated" type="warning"
        >匹配超過 5000 件，請按貨源、分類等縮小範圍後再操作。</NAlert
      >
      <div v-if="result">
        匹配 {{ result.matched }} 件 · 鎖定跳過
        {{ result.skipped_locked || 0 }} 件 · 不支持
        {{ result.unsupported || 0 }} 件 ·
        {{ applied ? "結果見下表" : `可處理 ${ready.length} 件` }}
      </div>
      <div v-if="busy" role="status" aria-live="polite">
        {{ result ? `正在處理，已檢查 ${processed} 件…` : "正在讀取預覽…" }}
      </div>
      <NAlert v-if="summary" type="info" role="status"
        >{{ summary
        }}<span v-if="action === 'enable'"
          >自動管理設置生效後，實際上下架等待後台檢查。</span
        ></NAlert
      >
      <NDataTable
        :columns="columns"
        :data="result?.items || []"
        :row-key="(p: any) => p.id"
        :pagination="{ pageSize: 50 }"
        :max-height="420"
        :scroll-x="660"
      />
    </NSpace>
    <template #footer
      ><NSpace justify="end"
        ><NButton :disabled="busy" @click="emit('update:show', false)"
          >關閉</NButton
        ><NButton v-if="applied" :disabled="busy" @click="preview"
          >重新預覽</NButton
        ><NButton
          v-else
          type="primary"
          :loading="busy"
          :disabled="busy || !ready.length || result?.truncated"
          @click="apply"
          >應用到 {{ ready.length }} 件商品</NButton
        ></NSpace
      ></template
    >
  </NModal>
</template>
