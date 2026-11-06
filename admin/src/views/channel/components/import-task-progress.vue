<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { NAlert, NButton, NDataTable, NPagination, NProgress, NSpace, NSwitch } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchSupplySyncTask, fetchSupplyImportItems, retrySupplyImportTask, cancelSupplySyncTask } from "@/service/api";
import { checkAuth } from "@/directives";

const props = defineProps<{ taskId: number }>();
const emit = defineEmits<{ (e: "changed"): void; (e: "configure"): void }>();
const task = ref<any>(null);
const items = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const problemsOnly = ref(false);
const busy = ref(false);
const connectionLost = ref(false);
let generation = 0;
let disposed = false;
let timer: ReturnType<typeof setTimeout> | undefined;
const running = computed(() => ["pending", "processing"].includes(task.value?.status));
const canWrite = computed(() => checkAuth("supply:write"));
const paused = computed(() => ["AUTH_FAILED", "ACCESS_DENIED", "CONNECTION_INVALID", "CONNECTION_DISABLED", "CONFIG_CHANGED"].includes(task.value?.error_code));
const title = computed(() => {
  const t = task.value;
  if (!t) return "正在讀取導入進度";
  if (t.cancel_requested_at && running.value) return "正在停止，已導入商品會保留";
  if (paused.value) return "導入已暫停，需要處理貨源配置";
  if (t.status === "canceled") return "導入已停止，已導入商品保留";
  if (t.status === "done") return t.error_code ? "處理完成，部分商品需要處理" : "導入完成";
  return t.status === "pending" ? "導入已排隊，後台將自動處理" : "正在導入商品";
});
const progress = computed(() => task.value?.total ? Math.min(100, Math.floor(Number(task.value.processed) / Number(task.value.total) * 100)) : 0);
const stateText: Record<string, string> = { pending: "待處理", retry: "等待重試", done: "完成", failed: "需處理", skipped: "已跳過" };
const columns: DataTableColumns<any> = [
  { title: "商品", key: "name", minWidth: 180, ellipsis: { tooltip: true } },
  { title: "商品編號", key: "code", width: 155, ellipsis: { tooltip: true } },
  { title: "進度", key: "state", width: 145, render: r => r.saved && r.stage === "stock" && r.state !== "done" ? `已保存 · 庫存${stateText[r.state] || r.state}` : stateText[r.state] || r.state },
  { title: "説明", key: "error_summary", minWidth: 230, render: r => r.error_summary || (r.saved ? "商品已保存" : "等待後台處理") },
];
async function refresh() {
  const version = generation;
  const id = props.taskId;
  const [t, detail] = await Promise.all([
    fetchSupplySyncTask(id),
    fetchSupplyImportItems(id, { page: page.value, page_size: 20, problems_only: problemsOnly.value }),
  ]);
  if (version !== generation) return;
  connectionLost.value = Boolean(t.error || detail.error);
  if (!t.error && t.data) task.value = t.data;
  if (!detail.error && detail.data) { items.value = (detail.data as any).items || []; total.value = Number((detail.data as any).total || 0); }
}
function schedule() {
  if (disposed) return;
  clearTimeout(timer);
  timer = setTimeout(async () => {
    if (!document.hidden) await refresh();
    if (running.value || connectionLost.value || !task.value) schedule();
  }, 3000);
}
watch(() => props.taskId, async () => {
  generation++;
  clearTimeout(timer);
  task.value = null; page.value = 1; problemsOnly.value = false;
  await refresh(); schedule();
}, { immediate: true });
watch([page, problemsOnly], () => { generation++; refresh(); });
onBeforeUnmount(() => { disposed = true; generation++; clearTimeout(timer); });
async function retry(scope: "failed" | "stock" | "remaining", confirmed = false) {
  if (paused.value && !confirmed) {
    window.$dialog?.warning({ title: "確認當前貨源配置後繼續", content: "未導入商品將使用當前貨源配置及原導入定價策略；已保存商品僅補查庫存，不重新改價。請先檢查賬號、匯率及分類映射。", positiveText: "確認並繼續", negativeText: "返回檢查", onPositiveClick: () => retry(scope, true) });
    return;
  }
  busy.value = true;
  try {
    const { error } = await retrySupplyImportTask(props.taskId, scope, confirmed);
    if (!error) { await refresh(); schedule(); emit("changed"); }
  } finally { busy.value = false; }
}
function stop() {
  window.$dialog?.warning({
    title: "停止後續導入？", content: "已導入商品會保留。當前處理步驟結束後停止，可稍後繼續未完成商品。",
    positiveText: "停止後續處理", negativeText: "繼續導入",
    onPositiveClick: async () => { await cancelSupplySyncTask(props.taskId); await refresh(); emit("changed"); },
  });
}

</script>

<template>
  <section class="flex flex-col gap-16px" aria-label="商品導入進度">
    <div role="status" aria-live="polite">
      <div class="mb-8px text-16px font-medium">{{ title }} · #{{ taskId }}</div>
      <NProgress type="line" :percentage="progress" :status="task?.error_code ? 'warning' : task?.status === 'done' ? 'success' : 'default'" />
      <div v-if="task" class="mt-10px flex flex-wrap gap-x-16px gap-y-6px text-14px">
        <span>共 {{ task.total || 0 }} 件</span>
        <span>已保存 {{ Number(task.created || 0) + Number(task.updated || 0) }}</span>
        <span>已跳過 {{ task.manual_skipped || 0 }}</span>
        <span>等待重試 {{ task.retrying_count || 0 }}</span>
        <span>待處理 {{ task.pending_count || 0 }}</span>
        <span>導入失敗 {{ task.failed_count || 0 }}</span>
      </div>
      <p v-if="Number(task?.stock_pending_count)" class="mt-8px text-13px">
        已保存商品中，{{ task.stock_pending_count }} 件庫存待確認<span v-if="Number(task.stock_failed_count)">，其中 {{ task.stock_failed_count }} 件需處理</span>。
        新商品在庫存確認前暫不上架。
      </p>
    </div>
    <NAlert v-if="connectionLost" type="warning" :show-icon="false">暫時無法讀取進度，正在重新連接。已提交的任務會在後台繼續，請勿重複導入。</NAlert>
    <NAlert v-if="task?.error_context" :type="task.error_code ? 'warning' : 'info'">{{ task.error_context }}</NAlert>
    <p v-if="running" class="text-13px text-gray-500">可以關閉頁面，後台會繼續處理。臨時失敗會自動重試，稍後可在貨源「任務」中查看結果。</p>
    <NSpace v-if="canWrite">
      <NButton v-if="running" :disabled="!!task.cancel_requested_at" @click="stop">停止任務</NButton>
      <template v-else-if="task">
        <NButton v-if="paused" @click="emit('configure')">檢查貨源配置</NButton>
        <NButton v-if="task.error_code === 'CONFIG_CHANGED'" type="primary" :loading="busy" @click="retry('remaining')">確認配置後繼續</NButton>
        <NButton v-else-if="['canceled', 'failed'].includes(task.status)" type="primary" :loading="busy" @click="retry('remaining')">繼續未完成商品</NButton>
        <NButton v-else-if="Number(task.failed_count)" type="primary" :loading="busy" @click="retry('failed')">重試失敗商品</NButton>
        <NButton v-if="Number(task.stock_failed_count) && task.error_code !== 'CONFIG_CHANGED'" :loading="busy" @click="retry('stock')">僅重試庫存</NButton>
      </template>
    </NSpace>
    <div class="flex items-center gap-8px"><NSwitch aria-label="僅看需處理和已跳過商品" v-model:value="problemsOnly" size="small" @update:value="page = 1" /><span>僅看需處理和已跳過商品</span></div>
    <div class="import-item-cards">
      <article v-for="item in items" :key="item.code" class="import-item-card">
        <strong>{{ item.name }}</strong>
        <span class="text-12px text-gray-500">{{ item.code }}</span>
        <span>{{ item.saved && item.stage === 'stock' && item.state !== 'done' ? `已保存 · 庫存${stateText[item.state] || item.state}` : stateText[item.state] }}</span>
        <p>{{ item.error_summary || (item.saved ? '商品已保存' : '等待後台處理') }}</p>
      </article>
      <p v-if="!items.length" class="text-13px text-gray-500">{{ problemsOnly ? '沒有需要處理的商品' : '暫無商品明細' }}</p>
    </div>
    <NDataTable class="import-item-table" :columns="columns" :data="items" :scroll-x="710" :max-height="380" size="small" />
    <NPagination v-model:page="page" :page-size="20" :item-count="total" :simple="true" />
  </section>
</template>

<style scoped>
.import-item-cards { display: none; }
@media (max-width: 600px) {
  .import-item-table { display: none; }
  .import-item-cards { display: grid; gap: 12px; }
  .import-item-card { display: grid; gap: 6px; padding: 12px; border: 1px solid var(--n-border-color, #d9dce3); border-radius: 8px; overflow-wrap: anywhere; }
}
</style>
