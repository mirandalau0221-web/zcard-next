<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { NCheckbox } from "naive-ui";
import { useAuthStore } from "@/store/modules/auth";
import { useFormRules, useNaiveForm } from "@/hooks/common/form";
import { $t } from "@/locales";
import { fetchAdminCaptchaConfig, fetchAdminCaptchaImage } from "@/service/api";

defineOptions({
  name: "PwdLogin",
});

const authStore = useAuthStore();
const { formRef, validate } = useNaiveForm();

interface FormModel {
  userName: string;
  password: string;
}

const model: FormModel = reactive({
  userName: "",
  password: "",
});

// 僅保存賬號；進入登錄頁時清除舊版本曾保存的密碼。
const REMEMBER_KEY = "zcard.admin.remember";
const rememberAccount = ref(false);
const challenge = ref("");
const otpCode = ref("");
const useRecovery = ref(false);
const challengeUntil = ref(0);
function loadRemembered() {
  try {
    const r = JSON.parse(localStorage.getItem(REMEMBER_KEY) || "{}");
    if (r.userName) model.userName = r.userName;
    rememberAccount.value = Boolean(r.userName);
    localStorage.setItem(REMEMBER_KEY, JSON.stringify({ userName: r.userName || "" }));
  } catch {
    /* 損壞忽略 */
  }
}
function saveRemembered() {
  if (rememberAccount.value)
    localStorage.setItem(REMEMBER_KEY, JSON.stringify({ userName: model.userName }));
  else localStorage.removeItem(REMEMBER_KEY);
}
async function restartLogin() {
  challenge.value = "";
  otpCode.value = "";
  model.password = "";
  useRecovery.value = false;
  if (showCaptcha.value) await loadCaptchaImage();
}

const rules = computed<Record<keyof FormModel, App.Global.FormRule[]>>(() => {
  // inside computed to make locale reactive, if not apply i18n, you can define it without computed
  const { formRules } = useFormRules();

  return {
    userName: formRules.userName,
    password: formRules.pwd,
  };
});

// ── 後台登錄圖形驗證碼（security.captcha_admin_login 開啓時顯示）──
const showCaptcha = ref(false);
const captchaId = ref("");
const captchaImage = ref("");
const captchaCode = ref("");
const captchaLength = ref(4);
const captchaCharset = ref<"digit" | "letter" | "mixed">("digit");
const captchaPlaceholder = computed(() => {
  const format = captchaCharset.value === "letter" ? "字母，不區分大小寫"
    : captchaCharset.value === "mixed" ? "數字和字母，不區分大小寫" : "數字";
  return `圖形驗證碼（${captchaLength.value} 位${format}）`;
});

function updateCaptchaCode(value: string) {
  const invalid = captchaCharset.value === "letter" ? /[^a-z]/gi
    : captchaCharset.value === "mixed" ? /[^a-z0-9]/gi : /\D/g;
  captchaCode.value = value.replace(invalid, "").slice(0, captchaLength.value);
}

onMounted(async () => {
  loadRemembered();
  try {
    const { data } = await fetchAdminCaptchaConfig();
    showCaptcha.value = data?.enabled === true;
    if (showCaptcha.value) await loadCaptchaImage();
  } catch {
    // 開關接口失敗：按未開啓處理（後端開啓時會拒絕無驗證碼登錄）
  }
});

/** 拉取驗證碼圖片（點擊圖片可刷新） */
async function loadCaptchaImage() {
  try {
    const { data } = await fetchAdminCaptchaImage();
    if (data?.captcha_id) {
      captchaId.value = data.captcha_id;
      captchaImage.value = data.image_base64;
      captchaLength.value = Number.isInteger(data.length) && data.length! >= 4 && data.length! <= 8 ? data.length! : 4;
      captchaCharset.value = data.charset === "letter" || data.charset === "mixed" ? data.charset : "digit";
      captchaCode.value = "";
    }
  } catch {
    // 拉取失敗保留原圖——用戶點擊可重試
  }
}

async function handleSubmit() {
  if (authStore.loginLoading) return;
  if (challenge.value) {
    if (Date.now() >= challengeUntil.value) {
      window.$message?.warning("驗證已過期，請重新登錄");
      await restartLogin();
      return;
    }
    if (
      useRecovery.value
        ? !/^[a-fA-F0-9]{20}$/.test(otpCode.value.trim())
        : !/^\d{6}$/.test(otpCode.value.trim())
    ) {
      window.$message?.warning(useRecovery.value ? "請輸入 20 位恢復碼" : "請輸入 6 位動態碼");
      return;
    }
    const result = await authStore.login("", "", {
      challenge: challenge.value,
      totp_code: otpCode.value.trim(),
    });
    otpCode.value = "";
    if (result?.reason === "identity.MFA_EXPIRED") await restartLogin();
    return;
  }
  await validate();
  if (showCaptcha.value && !captchaCode.value.trim()) {
    window.$message?.warning("請輸入圖形驗證碼");
    return;
  }
  saveRemembered();
  const result = await authStore.login(
    model.userName,
    model.password,
    showCaptcha.value
      ? { captcha_id: captchaId.value, captcha_code: captchaCode.value.trim() }
      : undefined,
  );
  if (result?.challenge) {
    challenge.value = result.challenge;
    challengeUntil.value = Date.now() + 5 * 60_000;
    model.password = "";
    return;
  }
  // 登錄失敗（錯誤消息已由 store 處理）或成功跳轉後刷新驗證碼，下次登錄用新圖
  if (showCaptcha.value) await loadCaptchaImage();
}
</script>

<template>
  <NForm
    ref="formRef"
    :model="model"
    :rules="rules"
    size="large"
    :show-label="false"
    @keyup.enter="handleSubmit"
  >
    <template v-if="!challenge">
      <NFormItem path="userName">
        <NInput
          v-model:value="model.userName"
          autocomplete="username"
          :placeholder="$t('page.login.common.userNamePlaceholder')"
        />
      </NFormItem>
      <NFormItem path="password">
        <NInput
          v-model:value="model.password"
          autocomplete="current-password"
          type="password"
          show-password-on="click"
          :placeholder="$t('page.login.common.passwordPlaceholder')"
        />
      </NFormItem>
      <!-- 後台登錄驗證碼（安全 → 後台登錄驗證碼 開啓時顯示） -->
      <NFormItem v-if="showCaptcha" path="captchaCode">
        <div class="flex w-full items-center gap-8px">
          <NInput
            :value="captchaCode"
            class="flex-1"
            :maxlength="captchaLength"
            :placeholder="captchaPlaceholder"
            :input-props="{ inputmode: captchaCharset === 'digit' ? 'numeric' : 'text', 'aria-label': '圖形驗證碼', autocapitalize: 'off', spellcheck: false }"
            @update:value="updateCaptchaCode"
          />
          <img
            v-if="captchaImage"
            :src="captchaImage"
            alt="驗證碼"
            title="點擊刷新"
            class="h-40px cursor-pointer rounded-6px border border-#e5e7eb"
            @click="loadCaptchaImage"
          />
          <NButton v-else quaternary size="small" @click="loadCaptchaImage">獲取驗證碼</NButton>
        </div>
      </NFormItem>
      <div class="mb-8px">
        <NCheckbox v-model:checked="rememberAccount" size="small">記住賬號</NCheckbox>
      </div>
    </template>
    <template v-else>
      <NAlert type="info" class="mb-16px">{{
        useRecovery
          ? "輸入一個未使用的恢復碼。每個恢復碼只能使用一次。"
          : "請輸入 Google Authenticator 中的 6 位動態驗證碼。"
      }}</NAlert>
      <NFormItem :label="useRecovery ? '恢復碼' : '動態驗證碼'" :show-label="true">
        <NInput
          v-model:value="otpCode"
          :placeholder="useRecovery ? '20 位恢復碼' : '6 位動態驗證碼'"
          :maxlength="useRecovery ? 20 : 6"
          autocomplete="one-time-code"
          :input-props="{ inputmode: useRecovery ? 'text' : 'numeric' }"
        />
      </NFormItem>
      <NSpace class="mb-16px"
        ><NButton
          text
          @click="
            useRecovery = !useRecovery;
            otpCode = '';
          "
          >{{ useRecovery ? "使用動態驗證碼" : "手機丟失？使用恢復碼" }}</NButton
        ><NButton text @click="restartLogin">返回賬號登錄</NButton></NSpace
      >
    </template>
    <NSpace vertical :size="24">
      <!-- admin 面僅密碼登錄：驗證碼登錄/註冊/找回密碼均為 storefront 能力，
           後端無對應端點，模板入口已移除（勿恢復——會切到無實現的登錄模塊） -->
      <NButton
        type="primary"
        size="large"
        round
        block
        :loading="authStore.loginLoading"
        @click="handleSubmit"
      >
        {{ $t("common.confirm") }}
      </NButton>
    </NSpace>
  </NForm>
</template>

<style scoped></style>
