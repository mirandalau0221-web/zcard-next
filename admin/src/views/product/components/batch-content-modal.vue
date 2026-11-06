<script setup lang="ts">
import { computed, h, nextTick, reactive, ref, watch } from "vue";
import { NButton, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { previewBatchProductContent, batchUpdateProductContent, getBatchProductContentResult } from "@/service/api/catalog";
import type { BatchContentPreview, BatchContentTarget, BatchContentResult, ProductContentPatch } from "@/service/api/catalog";
import { checkAuth } from "@/directives";
import { resolveMediaUrl } from "@/utils/media";
import MediaField from "@/components/common/media-picker/media-field.vue";
import RichEditor from "@/components/common/rich-editor/index.vue";

const props = defineProps<{ show: boolean; ids: number[]; categoryId: number | null; categories: { value: number; label: string }[] }>();
const emit = defineEmits<{ (e: "update:show", value: boolean): void; (e: "saved"): void }>();
const step = ref(1);
const scope = ref("selected");
const selectedIDs = ref<number[]>([]);
const categoryID = ref<number | null>(null);
const descendants = ref(true);
const busy = ref(false);
const errorText = ref("");
const resultUnknown = ref(false);
const definiteFailure = ref(false);
const preview = ref<BatchContentPreview | null>(null);
const sampleID = ref<number | null>(null);
const cover = ref<string[]>([]);
const draft = reactive<ProductContentPatch>({ cover_action: 0, cover: "", images_action: 0, images: [], description_action: 0, description: "" });
let previewSequence = 0;
const options = [{ label: "保持不變", value: 0 }, { label: "替換", value: 1 }, { label: "清空", value: 2 }];
const upstreamOptions = [...options, { label: "恢復跟隨上游（僅對接商品）", value: 3 }];
const categoryLabel = computed(() => props.categories.find(c => c.value === categoryID.value)?.label || "未選擇類目");
const scopeLabel = computed(() => scope.value === "selected" ? `已勾選 ${selectedIDs.value.length} 件商品` : `${categoryLabel.value} · ${descendants.value ? "包含子類目" : "僅直屬商品"} · 全部狀態和渠道`);
const hasPatch = computed(() => draft.cover_action + draft.images_action + draft.description_action > 0);
const sample = computed(() => preview.value?.targets.find(p => p.id === sampleID.value));
const sampleAfter = computed(() => {
  if (!sample.value || !preview.value) return null;
  const p = preview.value.patch;
  return { ...sample.value, cover: p.cover_action === 1 ? p.cover : p.cover_action === 2 ? "" : sample.value.cover,
    images: p.images_action === 1 ? p.images : p.images_action === 2 ? [] : sample.value.images,
    description: p.description_action === 1 ? p.description : p.description_action === 2 ? "" : sample.value.description };
});
const columns: DataTableColumns<BatchContentTarget> = [
  { title: "商品", key: "name", ellipsis: { tooltip: true } },
  { title: "來源", key: "upstream", width: 70, render: row => row.upstream ? "對接" : "自營" },
  { title: "變化", key: "changed", width: 100, render: row => h(NTag, { size: "small", type: row.changed ? "warning" : "default" }, { default: () => row.content_changed ? "內容變化" : row.protection_changed ? "保護變化" : "無變化" }) },
  { title: "預覽", key: "preview", width: 70, render: row => h(NButton, { size: "small", onClick: () => { sampleID.value = row.id; } }, { default: () => "查看" }) }
];
watch(() => props.show, show => {
  if (!show) { previewSequence++; return; }
  selectedIDs.value = [...new Set(props.ids)];
  scope.value = selectedIDs.value.length ? "selected" : "category";
  categoryID.value = props.categoryId;
  descendants.value = true;
  cover.value = [];
  Object.assign(draft, { cover_action: 0, cover: "", images_action: 0, images: [], description_action: 0, description: "" });
  step.value = 1; preview.value = null; errorText.value = ""; resultUnknown.value = false; definiteFailure.value = false;
});
watch([scope, categoryID, descendants, cover, draft], () => { previewSequence++; preview.value = null; errorText.value = ""; }, { deep: true });
watch(step, async () => {
  await nextTick();
  document.querySelector<HTMLElement>(".batch-content-modal")?.scrollTo({ top: 0 });
});
function close() { if (!busy.value && !resultUnknown.value) emit("update:show", false); }
function next() {
  if (scope.value === "selected" && !selectedIDs.value.length || scope.value === "category" && !categoryID.value) { errorText.value = "請勾選商品或選擇具體類目"; return; }
  errorText.value = ""; step.value = 2;
}
async function createPreview() {
  errorText.value = ""; definiteFailure.value = false;
  if (!hasPatch.value) { errorText.value = "請至少啓用一個修改字段"; return; }
  if (draft.cover_action === 1 && !cover.value.length || draft.images_action === 1 && !draft.images.length || draft.description_action === 1 && !draft.description.trim()) { errorText.value = "替換內容不能為空；需要刪除內容時請選擇清空"; return; }
  const sequence = ++previewSequence;
  busy.value = true;
  try {
    const { data, error } = await previewBatchProductContent({ ...(scope.value === "selected" ? { ids: selectedIDs.value } : { category_id: categoryID.value!, include_descendants: descendants.value }), patch: { ...draft, cover: cover.value[0] || "" } });
    if (sequence !== previewSequence || !props.show) return;
    if (error || !data) { errorText.value = "預覽失敗，請檢查提示後重試，草稿已保留"; return; }
    preview.value = data; sampleID.value = data.targets[0]?.id || null; step.value = 3;
  } finally { busy.value = false; }
}
function complete(data: BatchContentResult) {
  window.$message?.success(`已處理 ${data.matched} 件商品，修改 ${data.changed} 件，未變化 ${data.unchanged} 件，跳過鎖定商品 ${data.skipped_locked || 0} 件`);
  resultUnknown.value = false; emit("update:show", false); emit("saved");
}
async function submit() {
  if (!preview.value || busy.value) return;
  busy.value = true; errorText.value = "";
  try {
    const { data, error } = await batchUpdateProductContent(preview.value.request_id);
    if (!error && data?.completed) { complete(data); return; }
    const reason = (error as any)?.response?.data?.reason || "";
    if (reason.startsWith("catalog.BATCH_CONTENT")) {
      definiteFailure.value = true; resultUnknown.value = false;
      errorText.value = (error as any)?.response?.data?.message || "提交未執行，請重新預覽";
      return;
    }
    resultUnknown.value = true;
    await checkResult();
  } finally { busy.value = false; }
}
async function checkResult() {
  if (!preview.value) return;
  const { data, error } = await getBatchProductContentResult(preview.value.request_id);
  if (!error && data?.completed) { complete(data); return; }
  if ((error as any)?.response?.data?.reason === "catalog.BATCH_CONTENT_NOT_FOUND") {
    resultUnknown.value = false; definiteFailure.value = true; errorText.value = "預覽已過期且未執行，請重新預覽"; return;
  }
  // The original request may still be running. Only retry the SAME immutable request.
  resultUnknown.value = true;
  errorText.value = error ? "暫時無法確認執行結果。請查詢結果或重試同一批次。" : "本批次尚未確認提交。可重試同一批次；如提示內容衝突或預覽過期，請重新預覽。";
}
function repreview() {
  if (busy.value) return;
  // Only available after a definite service rejection, not an unknown network result.
  resultUnknown.value = false; definiteFailure.value = false; preview.value = null; step.value = 2;
}
</script>

<template>
  <!-- Locked skips remain visible in the preview, not reported as errors. -->
  <NModal :show="show" preset="card" title="批量修改內容" class="batch-content-modal" style="width: min(960px, calc(100vw - 24px)); max-height: calc(100dvh - 24px); overflow-y: auto"
    :mask-closable="!busy && !resultUnknown" :closable="!busy && !resultUnknown" :close-on-esc="!busy && !resultUnknown" @update:show="close">
    <NSteps :current="step" size="small" class="mb-16px"><NStep title="選擇範圍" /><NStep title="編輯內容" /><NStep title="預覽確認" /></NSteps>
    <NAlert v-if="preview?.skipped_locked" type="info" class="mb-12px">本次自動跳過 {{ preview.skipped_locked }} 件鎖定商品。</NAlert>
      <NAlert v-if="errorText" type="error" class="mb-12px" role="alert">{{ errorText }}</NAlert>
    <div v-if="step === 1">
      <NRadioGroup v-model:value="scope"><NSpace vertical>
        <NRadio value="selected" :disabled="!selectedIDs.length">已勾選商品（{{ selectedIDs.length }} 件）</NRadio>
        <NRadio value="category">指定類目全部商品（跨分頁）</NRadio>
      </NSpace></NRadioGroup>
      <NFormItem v-if="scope === 'category'" label="商品類目" class="mt-16px">
        <div class="w-full"><NSelect v-model:value="categoryID" :options="categories" filterable placeholder="搜索類目完整路徑" />
          <NCheckbox v-model:checked="descendants" class="mt-12px">包含子類目</NCheckbox>
        </div>
      </NFormItem>
      <NAlert v-if="scope === 'category'" type="warning" class="mt-12px">將包含該類目所有狀態、所有渠道的商品，不受列表關鍵詞和庫存篩選限制。</NAlert>
      <p class="mt-12px text-13px text-gray-500">單次最多 1000 件；預覽後新增商品不會納入本批次。</p>
    </div>
    <div v-else-if="step === 2">
      <p class="mb-12px break-words">範圍：{{ scopeLabel }}</p>
      <NAlert v-if="!checkAuth('media:read')" type="warning" class="mb-12px">當前賬號沒有素材讀取權限，請聯繫管理員授權後選擇圖片。</NAlert>
      <NForm label-placement="top" :disabled="busy" :inert="busy || undefined">
        <NFormItem label="商品封面"><div class="w-full"><NSelect v-model:value="draft.cover_action" :options="upstreamOptions" />
          <MediaField v-if="draft.cover_action === 1" v-model:value="cover" :disabled="busy" class="mt-12px" tip="從素材管理選擇一張圖片，統一應用到目標商品" />
        </div></NFormItem>
        <NFormItem label="詳情圖集"><div class="w-full"><NSelect v-model:value="draft.images_action" :options="options" />
          <MediaField v-if="draft.images_action === 1" v-model:value="draft.images" multiple sortable :disabled="busy" class="mt-12px" tip="最多 20 張，按下方順序展示" />
        </div></NFormItem>
        <NFormItem label="產品介紹"><div class="w-full"><NSelect v-model:value="draft.description_action" :options="upstreamOptions" />
          <RichEditor v-if="draft.description_action === 1" v-model="draft.description" height="300px" class="mt-12px" placeholder="輸入統一介紹，圖片從素材庫插入" />
        </div></NFormItem>
      </NForm>
      <NAlert type="info">對接商品的封面、介紹替換或清空後會受本地保護。恢復跟隨上游後，由下一次內容同步更新。</NAlert>
    </div>
    <div v-else-if="preview">
      <NAlert type="warning" class="mb-12px">
        {{ scopeLabel }}：共 {{ preview.matched }} 件，自營 {{ preview.matched - preview.upstream_count }} 件，對接 {{ preview.upstream_count }} 件。
        將修改 {{ preview.changed }} 件（內容變化 {{ preview.content_changed }}，保護變化 {{ preview.protection_changed }}），未變化 {{ preview.unchanged }} 件。
        <div v-if="[draft.cover_action, draft.images_action, draft.description_action].includes(2)">本次包含清空操作，清空的內容將被刪除。</div>
      </NAlert>
      <div class="flex flex-wrap gap-8px mb-12px">
        <NTag>封面：{{ upstreamOptions.find(o => o.value === draft.cover_action)?.label }}</NTag>
        <NTag>圖集：{{ options.find(o => o.value === draft.images_action)?.label }}</NTag>
        <NTag>介紹：{{ upstreamOptions.find(o => o.value === draft.description_action)?.label }}</NTag>
      </div>
      <NDataTable :columns="columns" :data="preview.targets" :row-key="(row: BatchContentTarget) => row.id" :pagination="{ pageSize: 10, pageSlot: 5 }" size="small" />
      <p class="my-12px">預覽商品：{{ sample?.name }}</p>
      <div v-if="sample && sampleAfter" class="content-comparison">
        <NCard v-for="(item, index) in [sample, sampleAfter]" :key="index" size="small" :title="index === 0 ? '修改前' : '修改後'">
          <p class="mb-8px">封面</p><NImage v-if="item.cover" :src="resolveMediaUrl(item.cover)" width="88" height="88" object-fit="cover" /><span v-else>無封面</span>
          <p class="my-8px">詳情圖集</p><div class="flex flex-wrap gap-8px"><NImage v-for="url in item.images" :key="url" :src="resolveMediaUrl(url)" width="64" height="64" object-fit="cover" /><span v-if="!item.images?.length">無圖集</span></div>
          <p class="my-8px">產品介紹</p><div v-if="item.description" class="content-html" v-html="item.description" /><span v-else>無介紹</span>
        </NCard>
      </div>
    </div>
    <template #footer><div class="flex flex-wrap justify-end gap-8px">
      <NButton :disabled="busy || resultUnknown" @click="close">取消</NButton>
      <NButton v-if="step > 1" :disabled="busy || resultUnknown" @click="step--; errorText = ''">上一步</NButton>
      <NButton v-if="step === 1" type="primary" @click="next">下一步</NButton>
      <NButton v-if="step === 2" type="primary" :loading="busy" :disabled="!hasPatch" @click="createPreview">生成預覽</NButton>
      <NButton v-if="resultUnknown" :disabled="busy" @click="checkResult">查詢執行結果</NButton>
      <NButton v-if="definiteFailure" :disabled="busy" @click="repreview">重新預覽</NButton>
      <NButton v-if="step === 3 && !definiteFailure" type="primary" :loading="busy" @click="submit">{{ resultUnknown ? '重試同一批次' : '確認修改' }}</NButton>
    </div></template>
  </NModal>
</template>
<style scoped>
.batch-content-modal { width: min(960px, calc(100vw - 24px)); max-height: calc(100dvh - 24px); overflow-y: auto; }
.content-comparison { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
.content-html { overflow-wrap: anywhere; max-height: 340px; overflow-y: auto; }
.content-html :deep(img), .content-html :deep(video) { max-width: 100%; height: auto; }
@media (max-width: 640px) { .content-comparison { grid-template-columns: minmax(0, 1fr); } }
</style>
