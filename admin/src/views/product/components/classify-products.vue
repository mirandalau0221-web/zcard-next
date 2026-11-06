<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { NAlert, NButton, NInput, NModal, NSelect, NSpace, NTag } from "naive-ui";
import { classifyProducts } from "@/service/api/catalog";
const props = defineProps<{ show: boolean; categories: { label: string; value: number }[] }>();
const emit = defineEmits<{ (e: "update:show", v: boolean): void; (e: "saved"): void }>();
const form = reactive({
  keywords: "",
  excludes: "",
  match_all: false,
  category_id: null as number | null,
});
const result = ref<any>(null),
  busy = ref(false),
  error = ref(""),
  applied = ref(false);
const words = (s: string) => [
  ...new Set(
    s
      .split(/[,，;；\n]+/)
      .map((v) => v.trim())
      .filter(Boolean),
  ),
];
const target = computed(
  () => props.categories.find((c) => c.value === form.category_id)?.label || "",
);
watch(form, () => {
  result.value = null;
  applied.value = false;
});
watch(
  () => props.show,
  (v) => {
    if (v) {
      result.value = null;
      error.value = "";
      applied.value = false;
    }
  },
);
async function run(apply = false) {
  if (busy.value || !form.category_id || !words(form.keywords).length) return;
  busy.value = true;
  error.value = "";
  try {
    const { data, error: e } = await classifyProducts({
      keywords: words(form.keywords),
      excludes: words(form.excludes),
      match_all: form.match_all,
      category_id: form.category_id,
      apply,
      revisions: apply
        ? Object.fromEntries((result.value?.items || []).map((p: any) => [p.id, p.revision]))
        : {},
    });
    if (e) {
      error.value = (e as any)?.response?.data?.message || "處理失敗，請重試；設置已保留";
      return;
    }
    result.value = data;
    applied.value = apply;
    if (apply) emit("saved");
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <NModal
    :show="show"
    preset="card"
    title="按關鍵詞自動分類"
    style="width: 740px; max-width: 96vw"
    :mask-closable="!busy"
    :closable="!busy"
    :close-on-esc="!busy"
    @update:show="(v) => emit('update:show', v)"
  >
    <div class="classify-products">
      <NAlert type="info"
        >範圍：本站全部商品，包含已下架商品。按商品名稱匹配；鎖定商品、已手動指定分類的商品會跳過。先預覽，再確認修改。</NAlert
      >
      <label
        >包含關鍵詞（逗號分隔）<NInput
          v-model:value="form.keywords"
          :disabled="busy"
          placeholder="例如：小火箭，Shadowrocket，Apple ID"
      /></label>
      <NSelect
        :value="form.match_all ? 1 : 0"
        @update:value="(v) => (form.match_all = Boolean(v))"
        :disabled="busy"
        :options="[
          { label: '包含任意一個關鍵詞', value: 0 },
          { label: '必須包含所有關鍵詞', value: 1 },
        ]"
      />
      <label
        >排除關鍵詞（可不填）<NInput
          v-model:value="form.excludes"
          :disabled="busy"
          placeholder="例如：獨享，充值"
      /></label>
      <label
        >分到哪個分類<NSelect
          v-model:value="form.category_id"
          :disabled="busy"
          :options="categories"
          filterable
          placeholder="選擇目標分類"
      /></label>
      <NAlert v-if="error" type="error">{{ error }}</NAlert>
      <template v-if="result">
        <NAlert v-if="result.truncated" type="warning"
          >匹配超過1000件，請補充關鍵詞縮小範圍後重新預覽。</NAlert
        >
        <NAlert v-else :type="applied ? 'success' : 'info'"
          >{{
            applied
              ? `已修改 ${result.updated || 0} 件`
              : `將修改 ${result.items?.length || 0} 件，移至「${target}」`
          }}；跳過鎖定 {{ result.skipped_locked || 0 }} 件，保留手動分類
          {{ result.skipped_protected || 0 }} 件，已在目標分類
          {{ result.unchanged || 0 }} 件。</NAlert
        >
        <div class="classification-preview">
          <div v-for="p in result.items" :key="p.id" class="classification-row">
            <span>{{ p.name }}</span
            ><NTag :type="p.status === 'conflict' ? 'warning' : 'default'">{{
              p.status === "conflict"
                ? "設置已變化，跳過"
                : p.status === "updated"
                  ? "已修改"
                  : p.status === "protected"
                    ? "保留手動分類"
                    : target
            }}</NTag>
          </div>
        </div>
        <p v-if="applied">
          僅本次預覽中的商品會被修改。今後導入的新商品，可在導入窗口保存關鍵詞分類規則。
        </p>
      </template>
    </div>
    <template #footer
      ><NSpace justify="end"
        ><NButton :disabled="busy" @click="emit('update:show', false)">關閉</NButton
        ><NButton
          :loading="busy && !applied"
          :disabled="!form.category_id || !words(form.keywords).length"
          @click="run(false)"
          >預覽匹配商品</NButton
        ><NButton
          v-if="result && !applied"
          type="primary"
          :disabled="busy || result.truncated || !result.items?.length"
          @click="run(true)"
          >確認分類 {{ result.items?.length || 0 }} 件商品</NButton
        ></NSpace
      ></template
    >
  </NModal>
</template>
<style scoped>
.classify-products {
  display: grid;
  gap: 12px;
  max-height: 68dvh;
  overflow: auto;
}
.classify-products label {
  display: grid;
  gap: 6px;
}
.classification-preview {
  max-height: 280px;
  overflow: auto;
}
.classification-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 0;
  overflow-wrap: anywhere;
}
.classification-row span {
  min-width: 0;
}
</style>
