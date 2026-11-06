<template>
  <div class="card" style="max-width: 420px; margin: 40px auto;">
    <h2 style="margin-bottom: 16px;">{{ $t('登錄') }}</h2>
    <div class="field">
      <label>{{ $t('賬號') }}</label>
      <input class="input" v-model="username" type="text" :placeholder="$t('用戶名 / 郵箱 / 手機號')" @keyup.enter="submit" />
    </div>
    <div class="field">
      <label>{{ $t('密碼') }}</label>
      <input class="input" v-model="password" type="password" :placeholder="$t('密碼')" @keyup.enter="submit" />
    </div>
    <div v-if="captchaCfg.login" class="field">
      <label>{{ $t('圖形驗證碼 *') }}</label>
      <CaptchaInput ref="captchaRef" @update:code="captchaCode = $event" @update:captcha-id="captchaId = $event" />
    </div>
    <div v-if="error" class="error">{{ uiText(error) }}</div>
    <button class="btn" style="width: 100%; margin-top: 8px;" :disabled="loading" @click="submit">
      {{ loading ? $t('登錄中…') : $t('登錄') }}
    </button>
    <div class="muted" style="margin-top: 12px; text-align: center;"> {{ $t('沒有賬號？') }}<router-link :to="{ path: '/register', query: $route.query.redirect ? { redirect: String($route.query.redirect) } : {} }">{{ $t('註冊') }}</router-link>
      ｜ <router-link to="/forgot-password">{{ $t('忘記密碼？') }}</router-link>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t } from '@/i18n';

import { ref, onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { login, fetchCaptchaConfig, type CaptchaConfig } from '@/api';
import CaptchaInput from '@/components/CaptchaInput.vue';
import { setToken } from '@/api/client';
import { refreshAuth } from '@/auth';

const route = useRoute();
const router = useRouter();
const username = ref('');
const password = ref('');
const error = ref('');
const loading = ref(false);
const captchaCfg = ref<CaptchaConfig>({ login: false, register: true, order: false, reset: true });
const captchaId = ref('');
const captchaCode = ref('');
const captchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null);

onMounted(async () => {
  captchaCfg.value = await fetchCaptchaConfig();
});

async function submit() {
  if (!username.value || !password.value) {
    error.value = $t("請輸入賬號和密碼");
    return;
  }
  loading.value = true;
  error.value = '';
  if (captchaCfg.value.login && !captchaCode.value) {
    error.value = $t("請輸入圖形驗證碼");
    return;
  }
  const { data, error: err } = await login({
    username: username.value,
    password: password.value,
    captcha_id: captchaId.value || undefined,
    captcha_code: captchaCode.value || undefined,
  });
  loading.value = false;
  if (err || !data) {
    error.value = err || $t("登錄失敗");
    captchaRef.value?.refresh(); // 失敗自動換圖
    return;
  }
  setToken(data.access_token);
  await refreshAuth(); // 頂欄登錄態即時更新（SPA 內不整頁刷新）
  router.push(typeof route.query.redirect === 'string' ? route.query.redirect : '/member');
}
</script>
