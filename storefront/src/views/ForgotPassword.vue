<template>
  <div class="card" style="max-width: 420px; margin: 40px auto;">
    <h2 style="margin-bottom: 16px;">{{ $t('找回密碼') }}</h2>

    <!-- 步驟一：輸入郵箱發碼 -->
    <template v-if="step === 1">
      <div class="field">
        <label>{{ $t('註冊郵箱 *') }}</label>
        <input class="input" v-model="email" type="email" placeholder="you@example.com" @keyup.enter="sendCode" />
        <div class="muted">{{ $t('若該郵箱已註冊，驗證碼將發送至郵箱（15 分鐘內有效）') }}</div>
      </div>
      <div v-if="captchaCfg.reset" class="field">
        <label>{{ $t('圖形驗證碼 *') }}</label>
        <CaptchaInput ref="captchaRef" @update:code="captchaCode = $event" @update:captcha-id="captchaId = $event" />
      </div>
      <div v-if="error" class="error" style="margin-bottom: 8px;">{{ uiText(error) }}</div>
      <button class="btn" style="width: 100%; margin-top: 8px;" :disabled="sending" @click="sendCode">
        {{ sending ? $t('發送中…') : $t('發送驗證碼') }}
      </button>
    </template>

    <!-- 步驟二：驗證碼 + 新密碼 -->
    <template v-else>
      <div class="muted" style="margin-bottom: 12px;"> {{ $t('驗證碼已發送至') }} <b>{{ email }}</b>
        <button class="btn secondary" style="margin-left: 8px; padding: 2px 10px;" :disabled="cooldown > 0 || sending" @click="sendCode">
          {{ cooldown > 0 ? $t('{0}s 後可重發', [cooldown]) : $t('重新發送') }}
        </button>
      </div>
      <div class="field">
        <label>{{ $t('驗證碼 *') }}</label>
        <input class="input" v-model="code" type="text" maxlength="6" :placeholder="$t('6 位數字')" />
      </div>
      <div class="field">
        <label>{{ $t('新密碼 *') }}</label>
        <input class="input" v-model="newPassword" type="password" :placeholder="$t('至少 6 位')" />
      </div>
      <div class="field">
        <label>{{ $t('確認新密碼 *') }}</label>
        <input class="input" v-model="confirmPassword" type="password" :placeholder="$t('再輸入一次')" />
      </div>
      <div v-if="error" class="error" style="margin-bottom: 8px;">{{ uiText(error) }}</div>
      <button class="btn" style="width: 100%; margin-top: 8px;" :disabled="resetting" @click="doReset">
        {{ resetting ? $t('重置中…') : $t('重置密碼') }}
      </button>
    </template>

    <div class="muted" style="margin-top: 12px; text-align: center;"> {{ $t('想起密碼了？') }}<router-link to="/login">{{ $t('返回登錄') }}</router-link>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t } from '@/i18n';

import { ref, onMounted, onUnmounted } from 'vue';
import { useRouter } from 'vue-router';
import CaptchaInput from '@/components/CaptchaInput.vue';
import { forgotPassword, resetPassword, fetchCaptchaConfig, type CaptchaConfig } from '@/api';
import { setToken } from '@/api/client';
import { refreshAuth } from '@/auth';

const router = useRouter();
const step = ref<1 | 2>(1);
const email = ref('');
const code = ref('');
const newPassword = ref('');
const confirmPassword = ref('');
const error = ref('');
const sending = ref(false);
const captchaCfg = ref<CaptchaConfig>({ login: false, register: true, order: false, reset: true });
const captchaId = ref('');
const captchaCode = ref('');
const captchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null);

onMounted(async () => {
  captchaCfg.value = await fetchCaptchaConfig();
});
const resetting = ref(false);
const cooldown = ref(0);
let timer: ReturnType<typeof setInterval> | null = null;

onUnmounted(() => { if (timer) clearInterval(timer); });

function startCooldown() {
  cooldown.value = 60;
  timer = setInterval(() => {
    cooldown.value--;
    if (cooldown.value <= 0 && timer) { clearInterval(timer); timer = null; }
  }, 1000);
}

async function sendCode() {
  if (!email.value.includes('@')) {
    error.value = $t("請輸入有效郵箱");
    return;
  }
  sending.value = true;
  error.value = '';
  const { error: err } = await forgotPassword(email.value.trim(), captchaCfg.value.reset ? { captcha_id: captchaId.value, captcha_code: captchaCode.value } : undefined);
  sending.value = false;
  // 防枚舉：任何輸入都成功——不區分"郵箱不存在"
  if (err) {
    error.value = err; // 僅冷卻等業務錯誤到達這裏
    return;
  }
  step.value = 2;
  if (cooldown.value <= 0) startCooldown();
}

async function doReset() {
  if (code.value.length !== 6) {
    error.value = $t("請輸入 6 位驗證碼");
    return;
  }
  if (newPassword.value.length < 6) {
    error.value = $t("新密碼至少 6 位");
    return;
  }
  if (newPassword.value !== confirmPassword.value) {
    error.value = $t("兩次輸入的密碼不一致");
    return;
  }
  resetting.value = true;
  error.value = '';
  const { data, error: err } = await resetPassword({
    email: email.value.trim(),
    code: code.value.trim(),
    new_password: newPassword.value
  });
  resetting.value = false;
  if (err || !data) {
    error.value = err || $t("重置失敗（驗證碼錯誤或已過期）");
    return;
  }
  setToken(data.token); // 重置即登錄
  await refreshAuth();
  router.push('/member');
}
</script>
