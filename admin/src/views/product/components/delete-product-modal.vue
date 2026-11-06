<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NModal, NSpace, NSpin } from "naive-ui";
import { deleteProduct, previewDeleteProduct, type ProductDeletePreview } from "@/service/api/catalog";

const props = defineProps<{ show: boolean; product: { id: number; name: string } | null }>();
const emit = defineEmits<{ (e: "update:show", value: boolean): void; (e: "deleted"): void }>();
const preview = ref<ProductDeletePreview | null>(null);
const loading = ref(false);
const deleting = ref(false);
const loadError = ref("");
const orderCount = computed(() => Number(preview.value?.order_count || 0));
let generation = 0;
async function load() {
  const current = ++generation;
  preview.value = null;
  loadError.value = "";
  if (!props.show || !props.product) { loading.value = false; return; }
  loading.value = true;
  try {
    const { data, error } = await previewDeleteProduct(props.product.id);
    if (current !== generation) return;
    if (error || !data) loadError.value = "無法讀取刪除影響，請重新加載後再操作。";
    else preview.value = data;
  } finally {
    if (current === generation) loading.value = false;
  }
}
watch(() => [props.show, props.product?.id], load, { immediate: true });
function close() { if (!deleting.value) emit("update:show", false); }
async function submit() {
  if (deleting.value || loading.value || !props.product || !preview.value) return;
  deleting.value = true;
  try {
    const { error } = await deleteProduct(props.product.id);
    if (!error) {
      window.$message?.success("商品已刪除，訂單快照及交付記錄已保留");
      emit("deleted");
      emit("update:show", false);
    } else await load();
  } finally { deleting.value = false; }
}
</script>

<template>
  <NModal :show="show" preset="card" title="刪除商品" style="width: 560px; max-width: 94vw"
    :closable="!deleting" :mask-closable="false" :close-on-esc="!deleting" @update:show="!$event && close()">
    <NSpin :show="loading">
      <div class="delete-content">
        <p class="product-name">{{ preview?.name || product?.name }}</p>
        <NAlert v-if="loadError" type="error" :bordered="false">{{ loadError }} <NButton size="small" @click="load">重新加載</NButton></NAlert>
        <template v-if="preview">
          <p>保留關聯訂單：<strong>{{ orderCount }}</strong> 條</p>
          <NAlert type="warning" :bordered="false">確認後商品立即從商城、商品管理和供貨目錄移除，詳情鏈接失效。商品鎖定、待付款或發貨中的訂單不會阻止刪除。</NAlert>
          <p>保留訂單快照、交付內容、取貨及退款記錄，已有訂單繼續處理；賬戶餘額不變。刪除後無法通過上下架或自動同步恢復商品。</p>
        </template>
      </div>
    </NSpin>
    <template #footer>
      <NSpace justify="end">
        <NButton :disabled="deleting" @click="close">取消</NButton>
        <NButton type="error" :loading="deleting" :disabled="loading || !preview" @click="submit">確認刪除（保留訂單）</NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.delete-content { display: flex; flex-direction: column; gap: 12px; max-height: 65dvh; overflow: auto; line-height: 1.7; }
.product-name { font-weight: 600; overflow-wrap: anywhere; }
</style>
