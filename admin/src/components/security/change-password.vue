<script setup lang="ts">
import { reactive, ref } from "vue";
import { request } from "@/service/request";
import { useAuthStore } from "@/store/modules/auth";

const emit = defineEmits<{ completed: []; cancel: [] }>();
const auth = useAuthStore();
const busy = ref(false);
const form = reactive({ current_password: "", new_password: "", confirm_password: "" });
const errors = reactive({ current_password: "", new_password: "", confirm_password: "" });
const errorText = ref("");
async function submit() {
  if (busy.value) return;
  errors.current_password = form.current_password ? "" : "請輸入當前密碼";
  errors.new_password = [...form.new_password].length < 6 ? "新密碼至少需要 6 個字符" : new TextEncoder().encode(form.new_password).length > 72 ? "新密碼過長，請縮短後再試" : form.new_password === form.current_password ? "新密碼不能與當前密碼相同" : "";
  errors.confirm_password = !form.confirm_password ? "請再次輸入新密碼" : form.confirm_password !== form.new_password ? "兩次輸入的新密碼不一致" : "";
  errorText.value = "";
  if (Object.values(errors).some(Boolean)) return;
  busy.value = true;
  try {
    const { error } = await request({ url: "/api/v1/admin/auth/password", method: "post", data: { ...form } });
    if (error) {
      errorText.value = (error as any)?.response?.data?.message || "修改失敗，請檢查當前密碼或稍後重試";
      return;
    }
    form.current_password = form.new_password = form.confirm_password = "";
    emit("completed");
    window.$message?.success("密碼已修改，請使用新密碼重新登錄");
    // The server has already invalidated all old access and refresh tokens.
    await auth.resetStore();
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <NModal :show="true" preset="card" title="修改密碼" style="width: 460px; max-width: calc(100vw - 24px)" :mask-closable="!busy" :close-on-esc="!busy" :closable="!busy" @update:show="!$event && emit('cancel')">
    <p class="mb-16px text-gray-500">修改成功後將自動退出，請使用新密碼重新登錄。</p>
    <NAlert v-if="errorText" type="error" class="mb-16px" role="alert">{{ errorText }}</NAlert>
    <NForm :disabled="busy" @submit.prevent="submit">
      <NFormItem label="當前密碼" :validation-status="errors.current_password ? 'error' : undefined" :feedback="errors.current_password">
        <NInput v-model:value="form.current_password" type="password" show-password-on="click" autocomplete="current-password" :input-props="{ 'aria-label': '當前密碼' }" />
      </NFormItem>
      <NFormItem label="新密碼" :validation-status="errors.new_password ? 'error' : undefined" :feedback="errors.new_password">
        <NInput v-model:value="form.new_password" type="password" show-password-on="click" autocomplete="new-password" placeholder="至少 6 個字符" :input-props="{ 'aria-label': '新密碼' }" />
      </NFormItem>
      <NFormItem label="確認新密碼" :validation-status="errors.confirm_password ? 'error' : undefined" :feedback="errors.confirm_password">
        <NInput v-model:value="form.confirm_password" type="password" show-password-on="click" autocomplete="new-password" :input-props="{ 'aria-label': '確認新密碼' }" />
      </NFormItem>
      <NSpace justify="end">
        <NButton :disabled="busy" @click="emit('cancel')">取消</NButton>
        <NButton type="primary" attr-type="submit" :loading="busy" :disabled="busy">確認修改</NButton>
      </NSpace>
    </NForm>
  </NModal>
</template>
