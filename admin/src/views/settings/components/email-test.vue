<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { testEmail } from "@/service/api";

const props = defineProps<{ unsaved: boolean }>();
const show = ref(false);
const sending = ref(false);
const form = reactive({ recipient: "", subject: "SMTP 郵件測試", content: "這是一封測試郵件，用於確認本站郵件發送是否正常。" });
type TestResult = { success: boolean; message: string; metadata?: Record<string, string> };
const result = ref<TestResult | null>(null);
const connectionModes: Record<string, string> = { auto: "自動", plain: "普通 SMTP", tls: "SSL/TLS", starttls: "STARTTLS" };
const authModes: Record<string, string> = { auto: "自動", plain: "PLAIN", login: "LOGIN", cram_md5: "CRAM-MD5", none: "無需認證" };
const valid = computed(() => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.recipient.trim()) && !!form.subject.trim() && !!form.content.trim());

async function send() {
  if (props.unsaved || sending.value || !valid.value) return;
  sending.value = true;
  result.value = null;
  try {
    const { error } = await testEmail({ recipient: form.recipient.trim(), subject: form.subject.trim(), content: form.content });
    if (error) {
      const detail = error.response?.data as { message?: string; metadata?: Record<string, string> } | undefined;
      result.value = { success: false, message: detail?.message || error.message || "測試發送失敗，請檢查 SMTP 配置後重試。", metadata: detail?.metadata };
    } else {
      result.value = { success: true, message: "SMTP 服務器已接受測試郵件，請檢查收件箱或垃圾郵件。若未收到，請查看郵件服務投遞日誌及 SPF / DKIM / DMARC 配置。" };
    }
  } catch (error: any) {
    result.value = { success: false, message: error?.message || "測試發送失敗，請重試。" };
  } finally { sending.value = false; }
}
</script>

<template>
  <div v-auth="'settings:update'" class="mt-16px max-w-760px">
    <NButton :disabled="unsaved" @click="show = true; result = null">測試郵件發送</NButton>
    <p class="mt-6px text-13px text-gray-500">{{ unsaved ? '請先保存上方配置，再發送測試郵件。' : '使用已保存的配置發送測試郵件；失敗時顯示具體階段、服務器錯誤和排查建議。' }}</p>
    <NModal v-model:show="show" preset="card" title="測試郵件發送"
      :style="{ width: 'min(600px, calc(100vw - 32px))' }"
      :closable="!sending" :mask-closable="!sending" :close-on-esc="!sending">
      <NForm label-placement="top" :disabled="sending" :aria-busy="sending">
        <NFormItem label="收件郵箱" required>
          <NInput v-model:value="form.recipient" placeholder="輸入接收測試郵件的郵箱" :maxlength="254" :input-props="{ type: 'email', 'aria-label': '收件郵箱' }" @update:value="result = null" />
        </NFormItem>
        <NFormItem label="郵件標題" required>
          <NInput v-model:value="form.subject" :input-props="{ 'aria-label': '郵件標題' }" placeholder="輸入郵件標題" @update:value="result = null" />
        </NFormItem>
        <NFormItem label="郵件內容" required>
          <NInput v-model:value="form.content" :input-props="{ 'aria-label': '郵件內容' }" type="textarea" :rows="5" placeholder="輸入郵件內容（純文本）" @update:value="result = null" />
        </NFormItem>
      </NForm>
      <NAlert v-if="result" :type="result.success ? 'success' : 'error'" :title="result.success ? '提交成功' : '發送失敗'" role="status" class="mb-16px">
        <div class="break-words" style="overflow-wrap: anywhere">
          <p v-if="result.metadata?.stage_label" class="mb-8px font-600">失敗階段：{{ result.metadata.stage_label }}</p>
          <p v-if="result.metadata?.host" class="mb-8px text-13px">
            {{ result.metadata.host }}:{{ result.metadata.port }} · {{ connectionModes[result.metadata.security] || result.metadata.security }}
            · 認證：{{ authModes[result.metadata.authentication] || result.metadata.authentication }}
          </p>
          <p v-if="result.metadata?.smtp_code && result.metadata.smtp_code !== '0'" class="mb-8px">SMTP 狀態碼：{{ result.metadata.smtp_code }}</p>
          <p>{{ result.message }}</p>
          <p v-if="result.metadata?.hint" class="mt-12px"><b>排查建議：</b>{{ result.metadata.hint }}</p>
        </div>
      </NAlert>
      <div class="flex justify-end gap-8px">
        <NButton :disabled="sending" @click="show = false">關閉</NButton>
        <NButton type="primary" :loading="sending" :disabled="unsaved || !valid" @click="send">發送測試郵件</NButton>
      </div>
    </NModal>
  </div>
</template>
