<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NCollapse,
  NCollapseItem,
  NForm,
  NFormItem,
  NInput,
  NSpace,
  NSpin,
  NSwitch,
} from "naive-ui";
import { fetchSettings, updateSettings } from "@/service/api";
import { checkAuth } from "@/directives";
import TelegramDelivery from "./telegram-delivery.vue";

const emit = defineEmits<{ dirty: [value: boolean]; busy: [value: boolean] }>();
interface Target {
  chat_id: string;
  topic: string;
  key: number;
}
const loading = ref(true);
const failed = ref(false);
const saving = ref(false);
const enabled = ref(false);
const orderEnabled = ref(false);
const lowStockEnabled = ref(false);
const token = ref("");
const savedToken = ref(false);
const events = ref<string[]>(["order.paid"]);
const targets = ref<Target[]>([]);
const baseline = ref("");
let rowKey = 0;
const eventOptions = [
  { value: "order.created", label: "新訂單" },
  { value: "order.paid", label: "付款成功" },
  { value: "order.delivered", label: "發貨完成" },
  { value: "order.refunded", label: "退款成功" },
];
const state = () =>
  JSON.stringify({
    enabled: enabled.value,
    orderEnabled: orderEnabled.value,
    lowStockEnabled: lowStockEnabled.value,
    token: token.value,
    events: events.value,
    targets: targets.value.map((t) => ({ chat_id: t.chat_id, topic: t.topic })),
  });
const dirty = computed(() => !!baseline.value && state() !== baseline.value);
watch(dirty, (value) => emit("dirty", value));
watch(saving, (value) => emit("busy", value));
const writable = computed(() => checkAuth("settings:update"));
const chatError = (t: Target) => {
  const v = t.chat_id.trim();
  if (!/^(-?\d+|@[A-Za-z0-9_]{5,})$/.test(v) || v.length > 64)
    return "填寫 Chat ID（可為負數）或 @用户名";
  if (!v.startsWith("@")) {
    try {
      const n = BigInt(v);
      if (n === 0n || n < -9223372036854775808n || n > 9223372036854775807n)
        return "Chat ID 超出有效範圍";
    } catch {
      return "Chat ID 無效";
    }
  }
  return "";
};
const topicError = (t: Target) =>
  t.topic.trim() &&
  (!/^\d+$/.test(t.topic.trim()) ||
    Number(t.topic) < 1 ||
    Number(t.topic) > 2147483647)
    ? "Topic ID 須為 1–2147483647 的整數，未使用話題請留空"
    : "";
function destination(t: Target) {
  const raw = t.chat_id.trim();
  return {
    chat_id: raw.startsWith("@") ? raw.toLowerCase() : BigInt(raw).toString(),
    topic_id: t.topic.trim() ? Number(t.topic) : 0,
  };
}
const duplicate = computed(() => {
  const keys = targets.value
    .filter((t) => !chatError(t) && !topicError(t))
    .map((t) => JSON.stringify(destination(t)));
  return new Set(keys).size !== keys.length;
});
const tokenError = computed(() =>
  token.value && !/^\d+:[A-Za-z0-9_-]{20,}$/.test(token.value)
    ? "Token 格式不正確，請從 @BotFather 複製完整 Token"
    : "",
);
const validation = computed(() => {
  if (enabled.value && lowStockEnabled.value && (!targets.value.length || (!savedToken.value && !token.value)))
    return "啓用低庫存通知需要 Token 和至少一個接收位置";
  if (tokenError.value) return tokenError.value;
  if (targets.value.some((t) => chatError(t) || topicError(t)))
    return "請修正接收位置中標出的錯誤";
  if (duplicate.value)
    return "接收位置重複：同一 Chat ID 和 Topic ID 只需填寫一次";
  if (
    enabled.value &&
    orderEnabled.value &&
    (!targets.value.length ||
      (!savedToken.value && !token.value) ||
      !events.value.length)
  )
    return "啓用訂單通知需要 Token、至少一個接收位置和一個通知事件";
  return "";
});
const savedTargets = ref<{ chat_id: string; topic_id: number }[]>([]);
async function load() {
  loading.value = true;
  failed.value = false;
  try {
    const { data, error } = await fetchSettings("notify");
    if (error || !data) {
      failed.value = true;
      return;
    }
    const values: Record<string, any> = {};
    for (const item of (data as any).items || [])
      values[item.key] = JSON.parse(item.value_json);
    enabled.value = values.telegram_enabled === true;
    orderEnabled.value = values.telegram_order_enabled === true;
    lowStockEnabled.value = values.telegram_low_stock_enabled === true;
    savedToken.value = values.telegram_bot_token === "****";
    token.value = "";
    events.value = values.telegram_events || ["order.paid"];
    const list = Array.isArray(values.telegram_targets)
      ? values.telegram_targets
      : [
          ...new Set<string>(
            (values.telegram_chat_ids || "")
              .split(",")
              .map((s: string) => s.trim())
              .filter(Boolean),
          ),
        ].map((chat_id) => ({ chat_id, topic_id: 0 }));
    targets.value = list.map((t: any) => ({
      chat_id: String(t.chat_id),
      topic: t.topic_id ? String(t.topic_id) : "",
      key: ++rowKey,
    }));
    savedTargets.value = targets.value
      .filter((t) => !chatError(t) && !topicError(t))
      .map(destination);
    if (!targets.value.length)
      targets.value.push({ chat_id: "", topic: "", key: ++rowKey });
    baseline.value = state();
  } catch {
    failed.value = true;
  } finally {
    loading.value = false;
  }
}
async function save() {
  if (validation.value || !dirty.value || saving.value) return;
  saving.value = true;
  try {
    const values: Record<string, unknown> = {
      telegram_enabled: enabled.value,
      telegram_order_enabled: orderEnabled.value,
      telegram_low_stock_enabled: lowStockEnabled.value,
      telegram_events: events.value,
      telegram_targets: targets.value.map(destination),
    };
    if (token.value) values.telegram_bot_token = token.value;
    const { error } = await updateSettings(
      Object.entries(values).map(([key, value]) => ({
        group: "notify",
        key,
        value_json: JSON.stringify(value),
      })),
    );
    if (!error) {
      if (token.value) savedToken.value = true;
      token.value = "";
      savedTargets.value = targets.value.map(destination);
      baseline.value = state();
      window.$message?.success("TG通知設置已保存");
    }
  } finally {
    saving.value = false;
  }
}
onMounted(load);
</script>

<template>
  <NCard title="TG訂單與庫存通知" class="mt-24px" size="small">
    <NSpin v-if="loading" />
    <NAlert v-else-if="failed" type="error" title="讀取 TG 配置失敗">
      已有配置未被修改。<NButton class="ml-12px" @click="load"
        >重新加載</NButton
      >
    </NAlert>
    <template v-else>
      <p class="mb-16px">
        向商家個人、管理羣或指定話題發送主站訂單與低庫存提醒。工單設置與此處分別保存。
      </p>
      <NForm
        label-placement="top"
        :disabled="!writable || saving"
        class="tg-form"
      >
        <div class="tg-switches">
          <NFormItem label="Telegram 通知通道"
            ><NSwitch v-model:value="enabled" aria-label="Telegram 通知通道"
          /></NFormItem>
          <NFormItem label="主站訂單通知"
            ><NSwitch
              v-model:value="orderEnabled"
              aria-label="主站訂單通知"
            /><span class="ml-8px"
              >關閉只停止訂單通知，其他 TG 告警由通道開關控制</span
            ></NFormItem
          >
          <NFormItem label="主站低庫存通知">
            <NSwitch v-model:value="lowStockEnabled" aria-label="主站低庫存通知" />
          </NFormItem>
        </div>
        <NAlert v-if="lowStockEnabled" type="info" :show-icon="false" class="mb-16px">
          請在「系統設置 → 貨源」開啓庫存預警並設置“庫存低於此數量時提醒”。首次跌破閾值合併通知，持續低庫存不重複發送，降至零可追加一次。恢復後再次跌破受 30 分鐘冷卻限制；查詢失敗不會當作缺貨。
        </NAlert>
        <NFormItem
          label="Bot Token"
          :validation-status="tokenError ? 'error' : undefined"
          :feedback="
            tokenError ||
            (savedToken
              ? '已配置，留空保留原 Token'
              : '從 @BotFather 獲取；Token 加密保存')
          "
        >
          <NInput
            v-model:value="token"
            type="password"
            show-password-on="click"
            autocomplete="new-password"
            placeholder="填寫或替換 Bot Token"
            aria-label="Bot Token"
          />
        </NFormItem>
        <NFormItem label="通知事件"
          ><NCheckboxGroup v-model:value="events"
            ><NSpace
              ><NCheckbox
                v-for="event in eventOptions"
                :key="event.value"
                :value="event.value"
                :label="event.label" /></NSpace></NCheckboxGroup
        ></NFormItem>
        <div class="mb-12px font-600">接收位置（最多 20 個）</div>
        <p class="mb-12px">
          Topic ID
          只在發往指定話題時填寫；留空按普通方式發送。配置話題後發送失敗，不會自動改投羣主聊天。
        </p>
        <div
          v-for="(target, index) in targets"
          :key="target.key"
          class="tg-target"
        >
          <NFormItem
            :label="`接收位置 ${index + 1} · Chat ID`"
            :validation-status="
              dirty && chatError(target) ? 'error' : undefined
            "
            :feedback="dirty ? chatError(target) : ''"
          >
            <NInput
              v-model:value="target.chat_id"
              placeholder="例如 -1001234567890"
              :aria-label="`Chat ID ${index + 1}`"
            />
          </NFormItem>
          <NFormItem
            label="Topic ID（選填）"
            :validation-status="topicError(target) ? 'error' : undefined"
            :feedback="topicError(target)"
          >
            <NInput
              v-model:value="target.topic"
              placeholder="不使用話題請留空"
              :aria-label="`Topic ID ${index + 1}`"
            />
          </NFormItem>
          <NButton
            :disabled="!writable || saving"
            @click="targets.splice(index, 1)"
            >刪除位置 {{ index + 1 }}</NButton
          >
        </div>
        <NButton
          :disabled="!writable || saving || targets.length >= 20"
          @click="targets.push({ chat_id: '', topic: '', key: ++rowKey })"
          >添加接收位置</NButton
        >
      </NForm>
      <NCollapse class="mt-16px"
        ><NCollapseItem title="如何填寫 Chat ID 和 Topic ID？" name="help">
          <p>
            先將機器人加入接收羣，並允許發送消息；個人接收需先向機器人發送
            /start。使用話題需在羣裏開啓話題功能。
          </p>
          <p>
            Chat ID 是羣或接收人的編號，Topic ID
            是該羣內話題的編號，不是羣編號，也不是話題中任意一條消息的編號。可從現有機器人配置或消息更新中的
            chat.id、message_thread_id 獲取。
          </p>
          <p>
            也可複製話題內消息的鏈接：例如 t.me/your_group/123/456，其中
            @your_group 是接收羣、123 是 Topic ID、456 是消息編號；若鏈接含
            thread=123，則話題編號為
            123。只有一個末尾數字的普通消息鏈接不能直接當成話題編號。
          </p>
          <p>先保存，再在下面按接收位置測試，確認消息進入正確話題。</p>
        </NCollapseItem></NCollapse
      >
      <NAlert v-if="dirty && validation" class="mt-16px" type="warning">{{
        validation
      }}</NAlert>
      <NSpace class="mt-16px" align="center">
        <NButton
          v-if="writable"
          type="primary"
          :loading="saving"
          :disabled="!dirty || !!validation"
          @click="save"
          >保存 TG 配置</NButton
        >
        <span v-if="dirty">TG 配置有未保存修改</span>
      </NSpace>
      <TelegramDelivery
        :unsaved="dirty || saving"
        :targets="savedTargets"
        :enabled="enabled && (orderEnabled || lowStockEnabled) && savedToken"
      />
    </template>
  </NCard>
</template>

<style scoped>
.tg-form {
  max-width: 960px;
}
.tg-switches {
  display: flex;
  flex-wrap: wrap;
  gap: 0 24px;
}
.tg-target {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) auto;
  gap: 12px;
  align-items: start;
  margin: 12px 0;
}
.tg-target > :last-child {
  margin-top: 30px;
}
@media (max-width: 640px) {
  .tg-target {
    grid-template-columns: minmax(0, 1fr);
    padding-bottom: 16px;
    border-bottom: 1px solid var(--n-border-color);
  }
  .tg-target > :last-child {
    margin-top: 0;
    justify-self: start;
  }
}
</style>
