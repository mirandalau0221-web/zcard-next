<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { NAlert, NButton, NEmpty, NSpace, NTag, NPagination } from "naive-ui";
import { request } from "@/service/request";
import { checkAuth } from "@/directives";

const props = defineProps<{
  unsaved: boolean;
  targets: { chat_id: string; topic_id: number }[];
  enabled: boolean;
}>();
interface DeliveryLog {
  id: number;
  event_type: string;
  recipient: string;
  topic_id?: number;
  subject: string;
  status: string;
  error_message?: string;
  attempts?: number;
  next_attempt_at?: number;
  message_id?: string;
  retryable?: boolean;
}
const logs = ref<DeliveryLog[]>([]);
const loading = ref(false);
const testing = ref(false);
const retrying = ref<number | null>(null);
const total = ref(0);
const page = ref(1);
const loaded = ref(false);
const loadFailed = ref(false);
const testNotice = ref("");
let timer: ReturnType<typeof setTimeout> | undefined;
let disposed = false;
async function load() {
  loading.value = true;
  try {
    const { data, error } = await request<{
      logs?: DeliveryLog[];
      total?: number;
    }>({
      url: "/api/v1/admin/notify/logs",
      params: { page: page.value, page_size: 10, channel: "telegram" },
    });
    loadFailed.value = !!error;
    if (!error && data) {
      logs.value = data.logs || [];
      total.value = Number(data.total || 0);
      loaded.value = true;
      return logs.value;
    }
  } finally {
    loading.value = false;
  }
  return [];
}
async function pollTest(ids: string[], deadline: number) {
  if (disposed) return;
  const rows = await load();
  if (disposed) return;
  const matches = rows.filter((row) => ids.includes(String(row.id)));
  if (
    matches.length === ids.length &&
    matches.every((row) => row.status !== "pending")
  ) {
    testNotice.value = matches.every((row) => row.status === "sent")
      ? "測試消息已發送，請到對應接收位置查看。"
      : "測試未全部送達，請查看發送記錄中的具體原因。";
    testing.value = false;
  } else if (Date.now() >= deadline) {
    testNotice.value = "測試仍在後台處理，可刷新發送記錄查看結果。";
    testing.value = false;
  } else timer = setTimeout(() => void pollTest(ids, deadline), 3000);
}
async function test(target: { chat_id: string; topic_id: number }) {
  if (testing.value || props.unsaved || !props.enabled) return;
  testing.value = true;
  try {
    const { data, error } = await request<{ log_ids: (number | string)[] }>({
      url: "/api/v1/admin/notify/telegram/test",
      method: "post",
      data: target,
    });
    if (error) {
      testing.value = false;
      return;
    }
    testNotice.value = `測試已排隊：${target.chat_id}${target.topic_id ? ` / Topic ${target.topic_id}` : " / 默認位置"}。排隊成功不代表已經送達。`;
    if (checkAuth("notify:read") && data?.log_ids?.length) {
      page.value = 1;
      await pollTest(data.log_ids.map(String), Date.now() + 60000);
    } else testing.value = false;
  } catch {
    testing.value = false;
  }
}
async function retry(id: number) {
  retrying.value = id;
  try {
    const { error } = await request({
      url: `/api/v1/admin/notify/logs/${id}/resend`,
      method: "post",
      data: {},
    });
    if (!error) {
      window.$message?.success("已按原接收位置加入重試隊列");
      await load();
    }
  } finally {
    retrying.value = null;
  }
}
const labels: Record<string, string> = {
  pending: "待發送 / 重試中",
  sent: "已發送",
  failed: "發送失敗",
  skipped: "已停止",
};
onMounted(() => {
  if (checkAuth("notify:read")) void load();
});
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(timer);
});
</script>
<template>
  <section class="mt-24px telegram-delivery" aria-label="TG 測試與發送記錄">
    <h3 class="font-600">測試與發送記錄</h3>
    <NAlert class="mt-12px" type="info" :show-icon="false"
      >只測試已保存的接收位置；消息不包含卡密或查詢密碼。網絡結果不明確時重試可能重複，請先檢查目標話題。</NAlert
    >
    <NSpace class="mt-12px">
      <template v-if="checkAuth('notify:write')">
        <NButton
          v-for="target in targets"
          :key="`${target.chat_id}:${target.topic_id}`"
          :loading="testing"
          :disabled="unsaved || !enabled || testing"
          @click="test(target)"
        >
          測試 {{ target.chat_id }} ·
          {{ target.topic_id ? `Topic ${target.topic_id}` : "默認位置" }}
        </NButton>
      </template>
      <NButton v-if="checkAuth('notify:read')" :loading="loading" @click="load"
        >刷新發送記錄</NButton
      >
    </NSpace>
    <p v-if="unsaved" class="mt-8px">請先保存 TG 配置，再測試或重試。</p>
    <p v-else-if="!enabled" class="mt-8px">
      請先配置 Token 並開啓通道及訂單或低庫存通知。
    </p>
    <p v-if="testNotice" class="mt-12px" role="status">{{ testNotice }}</p>
    <NAlert v-if="loadFailed" class="mt-12px" type="error"
      >發送記錄加載失敗，請點擊刷新重試。</NAlert
    >
    <NEmpty
      v-else-if="loaded && !logs.length"
      class="mt-16px"
      description="暫無 Telegram 發送記錄"
    />
    <article v-for="log in logs" :key="log.id" class="telegram-log">
      <NSpace align="center"
        ><b>{{ log.subject }}</b
        ><NTag
          size="small"
          :type="
            log.status === 'sent'
              ? 'success'
              : log.status === 'failed'
                ? 'error'
                : 'default'
          "
          >{{ labels[log.status] || log.status }}</NTag
        ></NSpace
      >
      <div>
        Chat ID：{{ log.recipient }} ·
        {{
          Number(log.topic_id)
            ? `Topic ID：${log.topic_id}`
            : "默認位置（未指定話題）"
        }}
      </div>
      <div>
        嘗試 {{ log.attempts || 0 }} 次<span v-if="log.message_id">
          · 消息 ID {{ log.message_id }}</span
        >
      </div>
      <div v-if="log.next_attempt_at">
        下次嘗試：{{
          new Date(Number(log.next_attempt_at) * 1000).toLocaleString()
        }}
      </div>
      <div v-if="log.error_message" role="status">{{ log.error_message }}</div>
      <NButton
        v-if="log.retryable && checkAuth('notify:write')"
        size="small"
        :loading="retrying === log.id"
        :disabled="retrying !== null || unsaved || !enabled"
        @click="retry(log.id)"
        >重試原接收位置</NButton
      >
    </article>
    <NPagination
      v-if="total > 10"
      v-model:page="page"
      :item-count="total"
      :page-size="10"
      @update:page="load"
    />
  </section>
</template>
<style scoped>
.telegram-delivery {
  overflow-wrap: anywhere;
}
.telegram-delivery :deep(.n-button) {
  max-width: 100%;
  height: auto;
  min-height: 34px;
  padding: 8px 12px;
}
.telegram-delivery :deep(.n-button__content) {
  white-space: normal;
  overflow-wrap: anywhere;
}
.telegram-log {
  padding: 16px 0;
  border-bottom: 1px solid var(--n-border-color);
  line-height: 1.8;
}
</style>
