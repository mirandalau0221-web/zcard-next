<script setup lang="ts">
import { computed, reactive, ref, watch, onUnmounted } from "vue";
import {
  NAlert,
  NButton,
  NDatePicker,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NSpin,
  NTag,
} from "naive-ui";
import { fetchDeliverySources, setDeliverySource } from "@/service/api/catalog";
const props = defineProps<{ show: boolean; productId: number; readonly?: boolean }>();
const emit = defineEmits<{ (e: "update:show", value: boolean): void; (e: "saved"): void }>();
const loading = ref(false),
  saving = ref(false),
  error = ref(""),
  dirty = ref(false);
const reply = ref<any>({ sources: [], receipts: [], revision: 0 });
const sku = ref(0),
  origin = ref("first");
const form = reactive({
  mode: "upstream",
  content: "",
  procurement_id: 0,
  content_index: 0,
  expires_at: 0,
  max_deliveries: 0,
  upstream_order_id: "",
});
const current = computed(() =>
  reply.value.sources.find((s: any) => Number(s.sku_id || 0) === sku.value),
);
const receipts = computed(() =>
  reply.value.receipts.filter((r: any) => Number(r.sku_id || 0) === sku.value),
);
const busy = computed(() => ["submitting", "polling", "uncertain"].includes(current.value?.status));
const names: Record<string, string> = {
  unconfigured: "未配置",
  empty: "等待首次採購",
  submitting: "首次採購中",
  polling: "查詢原採購結果",
  ready: "可重複發放",
  paused: "已暫停",
  uncertain: "需要核實原採購",
  failed: "需要處理",
  expired: "已過期",
};
function fill() {
  const s = current.value;
  form.mode = s?.mode || "upstream";
  form.content = "";
  form.procurement_id = 0;
  form.content_index = 0;
  form.expires_at = Number(s?.expires_at || 0);
  form.max_deliveries = Number(s?.max_deliveries || 0);
  form.upstream_order_id = s?.upstream_order_id || "";
  origin.value = s?.has_content ? "keep" : receipts.value.length ? "history" : "first";
  dirty.value = false;
}
let loadRequest=0;
async function load() {
  const request=++loadRequest; const productId=props.productId;
  loading.value = true;
  error.value = "";
  try {
    const { data, error: e } = await fetchDeliverySources(productId);
    if(request!==loadRequest || productId!==props.productId || !props.show)return;
    if (e) {
      error.value = "發貨設置加載失敗，請重試";
      return;
    }
    reply.value = data;
    reply.value.sources ||= [];
    reply.value.receipts ||= [];
    if (!reply.value.sources.some((s: any) => Number(s.sku_id || 0) === sku.value))
      sku.value = Number(reply.value.sources[0]?.sku_id || 0);
    fill();
  } finally {
    if(request===loadRequest)loading.value = false;
  }
}
let poll: ReturnType<typeof setInterval> | undefined;
function selectReceipt(v: string | number | null) {
  const [id, index] = String(v || "").split(":");
  form.procurement_id = Number(id || 0);
  form.content_index = Number(index || 0);
  dirty.value = true;
}
watch(
  () => [props.show,props.productId] as const,
  ([show]) => {
    clearInterval(poll);
    loadRequest++;
    if (show) {
      sku.value = 0;
      load();
      poll = setInterval(async () => {
        if (loading.value || saving.value || !busy.value) return;
        const pid = props.productId;
        const { data, error: e } = await fetchDeliverySources(pid);
        if (!e && props.show && pid === props.productId) {
          if(dirty.value && Number(data.revision)!==Number(reply.value.revision)){error.value="設置已被更新，當前草稿已保留，請重新加載後核對";return;}
          reply.value = data;
          if (!dirty.value) fill();
        }
      }, 5000);
    }
  },
  {immediate:true},
);
onUnmounted(() => clearInterval(poll));
function changeSku(value: number) {
  if (dirty.value) {
    window.$dialog?.warning({
      title: "切換規格？",
      content: "當前規格的未保存設置將丟棄。",
      positiveText: "丟棄並切換",
      negativeText: "繼續編輯",
      onPositiveClick: () => {
        sku.value = value;
        fill();
      },
    });
  } else {
    sku.value = value;
    fill();
  }
}
function close() {
  if (saving.value) return;
  if (dirty.value) {
    window.$dialog?.warning({
      title: "放棄未保存設置？",
      content: "已經保存的發貨設置仍會保留。",
      positiveText: "放棄並關閉",
      negativeText: "繼續編輯",
      onPositiveClick: () => emit("update:show", false),
    });
  } else emit("update:show", false);
}
async function save(action = "configure") {
  if (props.readonly || saving.value) return;
  if (action === "configure" && form.mode === "reuse") {
    if (origin.value === "manual" && !form.content.trim()) {
      error.value = "請填寫發貨內容";
      return;
    }
    if (origin.value === "history" && !form.procurement_id) {
      error.value = "請選擇一份歷史採購內容";
      return;
    }
  }
  saving.value = true;
  error.value = "";
  try {
    const { data, error: e } = await setDeliverySource(props.productId, {
      sku_id: sku.value,
      mode: form.mode,
      expected_revision: reply.value.revision,
      action,
      content: origin.value === "manual" ? form.content : "",
      procurement_id: origin.value === "history" ? form.procurement_id : 0,
      content_index: form.content_index,
      first_purchase: origin.value === "first",
      expires_at: form.expires_at,
      max_deliveries: form.max_deliveries,
      upstream_order_id: form.upstream_order_id,
    });
    if (e) {
      error.value = (e as any)?.response?.data?.message || "保存失敗，請檢查設置；草稿已保留";
      return;
    }
    reply.value = data;
    reply.value.sources ||= [];
    reply.value.receipts ||= [];
    fill();
    emit("saved");
    window.$message?.success("發貨設置已保存");
  } finally {
    saving.value = false;
  }
}
function submit() {
  if (form.mode === "upstream" && current.value?.mode !== "upstream") {
    window.$dialog?.warning({
      title: "切回每單上游採購？",
      content: "後續新訂單將逐單向上游採購併產生費用。已有訂單繼續按原方式處理。",
      positiveText: "切回每單採購",
      negativeText: "取消",
      onPositiveClick: () => save(),
    });
  } else {
    save();
  }
}
</script>
<template>
  <NModal
    :show="show"
    preset="card"
    title="商品發貨設置"
    style="width: 740px; max-width: 96vw"
    :mask-closable="false"
    @update:show="close"
  >
    <NSpin :show="loading">
      <div class="delivery-settings">
        <NAlert v-if="error" type="error"
          >{{ error }} <NButton v-if="!dirty" text @click="load">重新加載</NButton></NAlert
        >
        <NAlert v-if="readonly" type="warning">商品已鎖定，可查看，解鎖後才能修改發貨設置。</NAlert>
        <NFormItem label="商品規格"
          ><NSelect
            :value="sku" :disabled="saving||loading"
            :options="
              reply.sources.map((s: any) => ({ label: s.sku_name, value: Number(s.sku_id || 0) }))
            "
            @update:value="changeSku"
        /></NFormItem>
        <NSpace v-if="current"
          ><NTag>{{ names[current.status] || current.status }}</NTag
          ><span v-if="current.id"
            >內容版本 #{{ current.id }} · 已發放 {{ current.deliveries || 0 }} 次</span
          ></NSpace
        >
        <NAlert v-if="current?.error" type="warning">{{ current.error }}</NAlert>
        <NFormItem label="發貨來源"
          ><NSelect
            v-model:value="form.mode"
            :disabled="readonly || busy"
            :options="[
              { label: '每單向上游採購', value: 'upstream' },
              { label: '我的卡密：每條只賣一次', value: 'local' },
              { label: '重複發貨：複用同一份內容', value: 'reuse' },
            ]"
            @update:value="dirty = true"
        /></NFormItem>
        <NAlert v-if="form.mode === 'local'" type="info"
          >請在「卡密庫存」中為本規格導入卡密。缺貨時停止接單，不會自動向上游採購；售價由你自行管理。</NAlert
        >
        <template v-if="form.mode === 'reuse'">
          <NAlert type="info"
            >每個規格獨立保存內容，每單限購一份。內容準備好後直接本地發貨；過期或停用不會自動重新採購。庫存、售價不跟隨上游同步。</NAlert
          >
          <NFormItem label="內容來源"
            ><NSelect
              v-model:value="origin"
              :disabled="readonly || busy"
              :options="[
                ...(current?.has_content ? [{ label: '保留當前內容', value: 'keep' }] : []),
                { label: '選擇歷史採購內容', value: 'history' },
                { label: '填寫自己的賬號 / 卡密', value: 'manual' },
                { label: '首次有已付款訂單時採購一次', value: 'first' },
              ]"
              @update:value="dirty = true"
          /></NFormItem>
          <NFormItem v-if="origin === 'manual'" label="固定發貨內容"
            ><NInput
              v-model:value="form.content"
              :disabled="readonly || busy"
              type="textarea"
              :rows="5"
              placeholder="賬號、密碼及使用説明；內容加密保存，僅發給已付款買家"
              @update:value="dirty = true"
          /></NFormItem>
          <NFormItem v-if="origin === 'history'" label="選擇一份已購內容"
            ><NSelect
              :value="form.procurement_id ? `${form.procurement_id}:${form.content_index}` : null"
              :disabled="readonly || busy"
              :options="
                receipts.map((r: any) => ({
                  label: r.label,
                  value: `${r.procurement_id}:${r.content_index}`,
                }))
              "
              placeholder="僅顯示同商品同規格的成功採購（最近100筆訂單）"
              @update:value="selectReceipt"
          /></NFormItem>
          <NAlert v-if="origin === 'first'" type="warning"
            >首次採購只買 1
            份，會產生上游費用；多位買家共享同一次採購結果。響應不明確時暫停核實，不會再次採購。</NAlert
          >
          <NFormItem label="內容有效期（留空表示不設期限）"
            ><NDatePicker
              :value="form.expires_at ? form.expires_at * 1000 : null"
              :disabled="readonly || busy"
              type="datetime"
              clearable
              @update:value="
                (v) => {
                  form.expires_at = v ? Math.floor(v / 1000) : 0;
                  dirty = true;
                }
              "
          /></NFormItem>
          <NFormItem label="最多發放次數（0 表示不限）"
            ><NInputNumber
              v-model:value="form.max_deliveries"
              :disabled="readonly || busy"
              :min="0"
              :max="100000000"
              :precision="0"
              @update:value="dirty = true"
          /></NFormItem>
          <template v-if="current?.status === 'uncertain'"
            ><NFormItem label="原上游訂單號"
              ><NInput
                v-model:value="form.upstream_order_id"
                :disabled="readonly"
                placeholder="核對上游扣款後填寫原訂單號，只查原單，不重新購買" /></NFormItem
            ><NButton
              :disabled="readonly || !form.upstream_order_id"
              :loading="saving"
              @click="save('reconcile')"
              >核查原採購結果</NButton
            ></template
          >
          <NSpace v-if="current?.has_content"
            ><NButton
              v-if="current.status === 'ready'"
              :disabled="readonly"
              :loading="saving"
              @click="save('pause')"
              >暫停複用，停止新銷售</NButton
            ><NButton
              v-if="current.status === 'paused'"
              :disabled="readonly"
              :loading="saving"
              @click="save('resume')"
              >恢復複用</NButton
            ></NSpace
          >
        </template>
        <NAlert v-if="busy" type="warning"
          >首次採購結果尚未確認，暫不能更換來源。可先在商品列表下架，停止新訂單。</NAlert
        >
      </div>
    </NSpin>
    <template #footer
      ><NSpace justify="end"
        ><NButton @click="close">關閉</NButton
        ><NButton
          type="primary"
          :disabled="readonly || busy || loading"
          :loading="saving"
          @click="submit"
          >保存發貨設置</NButton
        ></NSpace
      ></template
    >
  </NModal>
</template>
<style scoped>
.delivery-settings {
  display: grid;
  gap: 12px;
  max-height: 70dvh;
  overflow: auto;
}
.delivery-settings :deep(.n-form-item) {
  margin-bottom: 0;
}
@media (max-width: 600px) {
  .delivery-settings {
    max-height: 65dvh;
  }
}
</style>
