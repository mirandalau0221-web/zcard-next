<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import { computed, ref, onMounted, onBeforeUnmount } from "vue";
import { api } from "@/api/client";
import { newRequestId } from "../../../packages/request-id";
const props = defineProps<{
  orderNo: string;
  metadata?: Record<string, string>;
}>();
type Snapshot = {
  state: string;
  phase: string;
  phone_number?: string;
  otp_code?: string;
  otp_message?: string;
  expires_at?: string;
  cancel_available_at?: string;
  can_cancel?: boolean;
  can_finish?: boolean;
  operation_status?: string;
  operation_action?: string;
  refund_status?: string;
  message?: string;
  active?: boolean;
};
const snapshot = ref<Snapshot | null>(null),
  error = ref(""),
  busy = ref(false),
  copied = ref(""),
  now = ref(Date.now());
let timer: ReturnType<typeof setTimeout> | undefined,
  clock: ReturnType<typeof setInterval> | undefined,
  closed = false,
  loadVersion = 0;
const pending = ref<{ action: "cancel" | "finish"; id: string }>();
const actionError = ref("");
const titles: Record<string, string> = {
  get allocating() { return $t("獲取號碼中"); },
  get waiting_sms() { return $t("等待短信"); },
  get sms_received() { return $t("已收到短信"); },
  get completed() { return $t("接碼已結束"); },
  get canceled() { return $t("已取消"); },
  get expired() { return $t("租期已結束"); },
  get rejected() { return $t("未能獲取號碼"); },
};
const title = computed(() =>
  snapshot.value?.phase === "review"
    ? $t("結果待核對")
    : titles[snapshot.value?.state || ""] || $t("確認結果中"),
);
const remaining = computed(() => {
  const end = Date.parse(snapshot.value?.expires_at || "");
  if (!Number.isFinite(end)) return "";
  const n = Math.max(0, Math.floor((end - now.value) / 1000));
  return n
    ? $t("{0} 分 {1} 秒", [Math.floor(n / 60), n % 60])
    : snapshot.value?.active
      ? $t("租期已到，正在確認最終結果")
      : $t("租期已結束");
});
const meta = computed(() =>
  ["country", "platform", "operator"]
    .map((k) => props.metadata?.[`${k}_name`] || props.metadata?.[`${k}_id`])
    .filter(Boolean)
    .join(" · "),
);
async function load() {
  if (closed) return;
  const version = ++loadVersion;
  if (timer) clearTimeout(timer);
  const r = await api.get<Snapshot>(
    `/orders/${encodeURIComponent(props.orderNo)}/sms`,
  );
  if (closed || version !== loadVersion) return;
  error.value = r.error || "";
  if (r.data) snapshot.value = r.data;
  if (r.error || r.data?.active)
    timer = setTimeout(load, document.hidden ? 30000 : r.error ? 10000 : 4000);
}
function visibilityChanged() {
  if (!document.hidden) void load();
}
async function copy(value: string, label: string) {
  try {
    await navigator.clipboard.writeText(value);
    copied.value = $t("{0}已複製", [label]);
  } catch {
    copied.value = $t("複製失敗，請手動選擇複製");
  }
}
async function act(action: "cancel" | "finish") {
  if (busy.value) return;
  if (
    !pending.value &&
    !window.confirm(
      action === "cancel"
        ? $t("申請取消接碼？供貨確認退款後退還本次實付；請求受理不代表已退款。")
        : $t("結束本次接碼？完成後將停止繼續接收短信，已收到的短信仍可查看。"),
    )
  )
    return;
  if (!pending.value) {
    try {
      pending.value = { action, id: newRequestId() };
    } catch (e) {
      actionError.value = e instanceof Error ? e.message : $t("無法生成操作標識");
      return;
    }
  }
  if (pending.value.action !== action) return;
  busy.value = true;
  const r = await api.post<Snapshot>(
    `/orders/${encodeURIComponent(props.orderNo)}/sms/${action}`,
    { request_id: pending.value.id },
  );
  busy.value = false;
  if (closed) return;
  actionError.value = r.error || "";
  if (
    r.status &&
    r.status >= 400 &&
    r.status < 500 &&
    r.status !== 408 &&
    r.status !== 429
  )
    pending.value = undefined;
  if (r.data) {
    snapshot.value = r.data;
    pending.value = undefined;
  }
  await load();
}
onMounted(() => {
  document.addEventListener("visibilitychange", visibilityChanged);
  void load();
  clock = setInterval(() => (now.value = Date.now()), 1000);
});
onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", visibilityChanged);
  closed = true;
  if (timer) clearTimeout(timer);
  if (clock) clearInterval(clock);
  snapshot.value = null;
});
</script>
<template>
  <section class="card sms-order" aria-labelledby="sms-title">
    <h3 id="sms-title">{{ $t('短信接碼 ·') }} {{ title }}</h3>
    <p v-if="meta" class="muted">{{ meta }}</p>
    <p v-if="error" role="alert" class="error">{{ uiText(error) }}</p>
    <p v-if="actionError" role="alert" class="error">{{ uiText(actionError) }}</p>
    <template v-if="snapshot">
      <div v-if="snapshot.phone_number" class="sms-field">
        <div>
          <span class="muted">{{ $t('號碼') }}</span
          ><strong>{{ snapshot.phone_number }}</strong>
        </div>
        <button
          class="btn secondary"
          @click="copy(snapshot.phone_number, $t('號碼'))"
        > {{ $t('複製號碼') }} </button>
      </div>
      <p v-if="remaining" class="muted">{{ remaining }}</p>
      <div v-if="snapshot.otp_code" class="sms-field">
        <div>
          <span class="muted">{{ $t('驗證碼') }}</span
          ><strong>{{ snapshot.otp_code }}</strong>
        </div>
        <button
          class="btn secondary"
          @click="copy(snapshot.otp_code, $t('驗證碼'))"
        > {{ $t('複製驗證碼') }} </button>
      </div>
      <div v-if="snapshot.otp_message">
        <p>{{ $t('短信正文') }}</p>
        <pre>{{ snapshot.otp_message }}</pre>
        <button
          class="btn secondary"
          @click="copy(snapshot.otp_message, $t('正文'))"
        > {{ $t('複製正文') }} </button>
      </div>
      <p v-if="snapshot.message">{{ snapshot.message }}</p>
      <p v-if="snapshot.refund_status === 'succeeded'">{{ $t('已退款至會員餘額') }}</p>
      <p v-if="snapshot.operation_status === 'pending'">
        {{
          snapshot.operation_action === "cancel" ? $t('取消') : $t('完成')
        }}{{ $t('請求已提交，等待確認。') }} </p>
      <p v-else-if="snapshot.operation_status === 'rejected'"> {{ $t('上一次操作未獲允許，短信仍可查看。') }} </p>
      <p
        v-if="snapshot.cancel_available_at && !snapshot.can_cancel"
        class="muted"
      > {{ $t('預計可申請取消時間：') }}{{
          new Date(snapshot.cancel_available_at).toLocaleString(localeTag)
        }}{{ $t('，最終以供貨確認能力為準。') }} </p>
      <div class="sms-actions">
        <button
          v-if="pending"
          class="btn secondary"
          :disabled="busy"
          @click="act(pending.action)"
        > {{ pending.action === "cancel" ? $t('重試確認取消結果') : $t('重試確認完成結果') }} </button>
        <button
          v-if="snapshot.can_cancel"
          class="btn secondary"
          :disabled="busy || !!pending"
          @click="act('cancel')"
        > {{ $t('申請取消') }}</button
        ><button
          v-if="snapshot.can_finish"
          class="btn"
          :disabled="busy || !!pending"
          @click="act('finish')"
        > {{ $t('完成接碼') }}</button
        ><button class="btn secondary" :disabled="busy" @click="load"> {{ $t('刷新狀態') }} </button>
      </div>
    </template>
    <p v-else class="muted">{{ $t('正在讀取接碼狀態…') }}</p>
    <p role="status" aria-live="polite">{{ copied }}</p>
  </section>
</template>
<style scoped>
.sms-order {
  padding: 24px;
}
.sms-field {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  margin: 16px 0;
  flex-wrap: wrap;
}
.sms-field strong {
  display: block;
  font-size: 1.3rem;
  overflow-wrap: anywhere;
}
.sms-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 16px;
}
.sms-order pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: inherit;
  line-height: 1.6;
}
.sms-order button {
  min-height: 44px;
}
</style>
