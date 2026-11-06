<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { NQrCode } from "naive-ui";
import { request } from "@/service/request";
import { fetchGetUserInfo } from "@/service/api";
import { useAuthStore } from "@/store/modules/auth";
import { recoveryCodes, recoveryTicket } from "./state";
const props = defineProps<{ resetTarget?: { id: number; username: string } }>();
const emit = defineEmits<{ completed: [] }>();
const auth = useAuthStore();
const enabled = ref(false);
const boundAt = ref(0);
const loaded = ref(false);
const loadFailed = ref(false);
const busy = ref(false);
const mode = ref<"status" | "setup" | "disable">("status");
const pending = ref<{ secret: string; otpauth_url: string }>();
const form = reactive({ password: "", code: "", confirm: "", reason: "" });
const errorText = ref("");
async function loadStatus() {
  loadFailed.value = false;
  const { data, error } = await fetchGetUserInfo();
  if (!error) {
    enabled.value = Boolean(data?.admin?.totp_enabled);
    boundAt.value = Number(data?.admin?.totp_bound_at || 0);
    loaded.value = true;
  } else {
    loadFailed.value = true;
  }
}
onMounted(loadStatus);
async function run(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  errorText.value = "";
  try {
    await fn();
  } finally {
    busy.value = false;
  }
}
function start(value: "setup" | "disable") {
  mode.value = value;
  form.password = "";
  form.code = "";
}
async function setup() {
  if (!form.password || (enabled.value && !form.code && !recoveryTicket.value)) {
    errorText.value = "請輸入當前密碼和動態碼或恢復碼";
    return;
  }
  await run(async () => {
    const { data, error } = await request<{ secret: string; otpauth_url: string }>({
      url: "/api/v1/admin/auth/totp/enable",
      method: "post",
      data: { password: form.password, code: form.code, recovery_ticket: recoveryTicket.value },
    });
    if (!error) {
      pending.value = data;
      recoveryTicket.value = "";
      form.password = "";
      form.code = "";
      form.confirm = "";
    }
  });
}
async function confirm() {
  if (!/^\d{6}$/.test(form.confirm)) {
    errorText.value = "請輸入 6 位動態驗證碼";
    return;
  }
  await run(async () => {
    const { data, error } = await request<{ recovery_codes: string[] }>({
      url: "/api/v1/admin/auth/totp/confirm",
      method: "post",
      data: { code: form.confirm },
    });
    if (!error) {
      recoveryCodes.value = data.recovery_codes;
      pending.value = undefined;
      form.confirm = "";
      await auth.resetStore();
    }
  });
}
async function disable() {
  if (!form.password || !form.code) {
    errorText.value = "請輸入當前密碼和動態碼或恢復碼";
    return;
  }
  await run(async () => {
    const { error } = await request({
      url: "/api/v1/admin/auth/totp/disable",
      method: "post",
      data: { password: form.password, code: form.code },
    });
    if (!error) {
      window.$message?.success("已解綁，請重新登錄");
      await auth.resetStore();
    }
  });
}
async function resetOther() {
  if (
    !props.resetTarget ||
    !form.password ||
    !form.reason.trim() ||
    (enabled.value && !form.code)
  ) {
    errorText.value = "請填寫當前密碼、重置原因，以及你自己的動態碼（已綁定時）";
    return;
  }
  await run(async () => {
    const { error } = await request({
      url: "/api/v1/admin/auth/totp/reset",
      method: "post",
      data: {
        admin_id: props.resetTarget!.id,
        password: form.password,
        code: form.code,
        reason: form.reason,
      },
    });
    if (!error) {
      form.password = "";
      form.code = "";
      window.$message?.success("已重置該員工的兩步驗證，舊會話已失效");
      emit("completed");
    }
  });
}
async function cancel() {
  if (pending.value) {
    await run(async () => {
      const { error } = await request({ url: "/api/v1/admin/auth/totp/cancel", method: "post" });
      if (!error) {
        pending.value = undefined;
        mode.value = "status";
      }
    });
  } else {
    mode.value = "status";
  }
  form.password = "";
  form.code = "";
  form.confirm = "";
}
</script>
<template>
  <NCard
    :title="resetTarget ? `重置 ${resetTarget.username} 的兩步驗證` : '當前賬號兩步驗證'"
    class="my-16px"
  >
    <NSpin :show="(!loaded && !loadFailed) || busy">
      <NSpace :key="resetTarget ? 'reset' : pending ? 'pending' : mode" vertical :size="16">
        <NAlert v-if="loadFailed" type="error"
          >讀取賬號安全狀態失敗。<NButton text @click="loadStatus">重試</NButton></NAlert
        >
        <NAlert v-if="errorText" type="error" role="alert">{{ errorText }}</NAlert>
        <template v-if="resetTarget">
          <NAlert type="warning"
            >該員工的驗證器、恢復碼及全部後台會話將失效。請驗證你自己的身份後重置。</NAlert
          >
          <NFormItem label="你的當前密碼"
            ><NInput v-model:value="form.password" type="password" autocomplete="current-password"
          /></NFormItem>
          <NFormItem v-if="enabled" label="你的動態碼或恢復碼"
            ><NInput v-model:value="form.code" autocomplete="one-time-code"
          /></NFormItem>
          <NFormItem label="重置原因"
            ><NInput v-model:value="form.reason" :maxlength="255"
          /></NFormItem>
          <NButton type="error" :loading="busy" :disabled="!loaded" @click="resetOther"
            >確認強制重置</NButton
          >
        </template>
        <template v-else-if="pending">
          <NAlert type="info"
            >請在 10 分鐘內用 Google Authenticator
            掃碼，並輸入動態碼完成綁定。確認前原有登錄方式繼續有效。</NAlert
          >
          <NQrCode :value="pending.otpauth_url" :size="200" aria-label="谷歌驗證器綁定二維碼" />
          <NFormItem label="無法掃碼時手動輸入密鑰"
            ><NInput :value="pending.secret" readonly
          /></NFormItem>
          <NFormItem label="新驗證器的 6 位動態碼"
            ><NInput
              v-model:value="form.confirm"
              :maxlength="6"
              autocomplete="one-time-code"
              :input-props="{ inputmode: 'numeric' }"
              @keyup.enter="confirm"
          /></NFormItem>
          <NSpace
            ><NButton type="primary" :loading="busy" @click="confirm">確認綁定</NButton
            ><NButton :disabled="busy" @click="cancel">取消綁定</NButton></NSpace
          >
        </template>
        <template v-else-if="mode !== 'status'">
          <NAlert :type="mode === 'disable' ? 'warning' : 'info'">{{
            mode === "disable"
              ? "解綁後登錄後台不再要求動態碼，全部舊會話會失效。"
              : "綁定僅對當前賬號生效。更換驗證器確認成功前，舊驗證器繼續有效。"
          }}</NAlert>
          <NFormItem label="當前密碼"
            ><NInput v-model:value="form.password" type="password" autocomplete="current-password"
          /></NFormItem>
          <NAlert v-if="enabled && recoveryTicket && mode === 'setup'" type="info"
            >已通過恢復碼驗證身份，可以重新綁定驗證器。</NAlert
          >
          <NFormItem v-else-if="enabled" label="當前動態碼或未使用的恢復碼"
            ><NInput v-model:value="form.code" autocomplete="one-time-code"
          /></NFormItem>
          <NSpace
            ><NButton
              :type="mode === 'disable' ? 'error' : 'primary'"
              :loading="busy"
              @click="mode === 'disable' ? disable() : setup()"
              >{{ mode === "disable" ? "確認解綁" : "生成綁定二維碼" }}</NButton
            ><NButton :disabled="busy" @click="cancel">取消</NButton></NSpace
          >
        </template>
        <template v-else>
          <NTag :type="enabled ? 'success' : 'default'">{{
            enabled ? "已綁定 Google Authenticator" : "尚未綁定"
          }}</NTag>
          <p v-if="enabled && boundAt">綁定時間：{{ new Date(boundAt * 1000).toLocaleString() }}</p>
          <p>
            綁定後，每次登錄後台都需要密碼和 6
            位動態驗證碼。手機丟失時可使用恢復碼；恢復碼也丟失時，可在服務器通過命令行恢復。
          </p>
          <NAlert v-if="recoveryTicket" type="warning"
            >你剛通過恢復碼登錄，建議現在重新綁定驗證器。</NAlert
          >
          <NSpace
            ><NButton type="primary" :disabled="!loaded" @click="start('setup')">{{
              enabled ? "更換驗證器 / 更新恢復碼" : "綁定谷歌驗證器"
            }}</NButton
            ><NButton v-if="enabled" @click="start('disable')">解綁驗證器</NButton></NSpace
          >
        </template>
      </NSpace>
    </NSpin>
  </NCard>
</template>
