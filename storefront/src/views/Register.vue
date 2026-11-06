<template>
  <div class="card" style="max-width: 420px; margin: 40px auto;">
    <h2 style="margin-bottom: 16px;">{{ $t('註冊') }}</h2>

    <!-- 註冊關閉 -->
    <template v-if="cfg && !cfg.enabled">
      <div class="muted" style="text-align: center; padding: 24px 0;">{{ $t('站點暫未開放註冊') }}</div>
      <div class="muted" style="text-align: center;"> {{ $t('已有賬號？') }}<router-link to="/login">{{ $t('登錄') }}</router-link>
      </div>
    </template>

    <template v-else>
      <!-- 註冊方式切換（多選時顯示；單選自動隱藏） -->
      <div v-if="methods.length > 1" class="method-tabs">
        <button
          v-for="m in methods"
          :key="m"
          class="method-tab"
          :class="{ active: activeMethod === m }"
          @click="activeMethod = m"
        >
          {{ methodLabel(m) }}
        </button>
      </div>

      <div class="field">
        <label>{{ $t('用戶名 *') }}</label>
        <input class="input" v-model="username" type="text" :placeholder="$t('用戶名')" />
      </div>
      <div class="field">
        <label>{{ $t('密碼 *') }}</label>
        <input class="input" v-model="password" type="password" :placeholder="$t('至少 6 位')" />
      </div>

      <!-- 郵箱驗證模式：郵箱必填 + 驗證碼 -->
      <div v-if="activeMethod === 'email'" class="field">
        <label>{{ $t('郵箱 *') }}</label>
        <div style="display: flex; gap: 8px;">
          <input class="input" v-model="email" type="email" :placeholder="$t('用於登錄和找回密碼')" style="flex: 1;" />
          <button class="btn secondary" :disabled="sending || cooldown > 0" @click="doSendCode('email')">
            {{ cooldown > 0 ? `${cooldown}s` : sending ? $t('發送中') : $t('發驗證碼') }}
          </button>
        </div>
      </div>
      <div v-else class="field">
        <label>{{ $t('郵箱') }}</label>
        <input class="input" v-model="email" type="email" :placeholder="$t('選填')" />
      </div>

      <!-- 手機驗證模式：手機號必填 + 短信驗證碼 -->
      <div v-if="activeMethod === 'phone'" class="field">
        <label>{{ $t('手機號 *') }}</label>
        <div style="display: flex; gap: 8px;">
          <input class="input" v-model="phone" type="tel" :placeholder="$t('11 位手機號')" style="flex: 1;" />
          <button class="btn secondary" :disabled="sending || cooldown > 0" @click="doSendCode('phone')">
            {{ cooldown > 0 ? `${cooldown}s` : sending ? $t('發送中') : $t('發驗證碼') }}
          </button>
        </div>
      </div>

      <!-- 驗證碼輸入（email/phone 模式） -->
      <div v-if="activeMethod === 'email' || activeMethod === 'phone'" class="field">
        <label>{{ $t('驗證碼 *') }}</label>
        <input class="input" v-model="code" type="text" :placeholder="$t('6 位數字驗證碼')" maxlength="6" />
      </div>

      <!-- 圖形驗證碼（captcha_register 開啓時） -->
      <div v-if="captchaCfg.register" class="field">
        <label>{{ $t('圖形驗證碼 *') }}</label>
        <CaptchaInput ref="captchaRef" @update:code="captchaCode = $event" @update:captcha-id="captchaId = $event" />
      </div>
      <div class="field">
        <label>{{ $t('推廣碼') }}</label>
        <input class="input" v-model="inviteCode" type="text" :placeholder="$t('選填（好友的推廣碼，如 ABC12345）')" />
        <div class="muted" style="margin-top: 4px;" aria-live="polite">
          <span v-if="benefitLoading">{{ $t('正在查詢推薦權益…') }}</span>
          <span v-else-if="benefitError">{{ uiText(benefitError) }}</span>
          <span v-else-if="benefit?.has_benefit">{{ $t('註冊成功即可獲得 {0} 待遇', [benefit.level?.name || $t('專屬會員')]) }}{{ benefit.level?.display_mode === 'public' ? $t('，會員價享 {0}', [levelDiscount(benefit.level.discount)]) : '' }}。</span>
          <span v-else-if="benefit?.valid">{{ $t('推薦碼有效，將綁定邀請關係。') }}</span>
          <span v-else-if="benefit">{{ $t('推薦碼無效或推薦人已停用，請檢查或清空。') }}</span>
        </div>
      </div>
      <div v-if="error" class="error">{{ uiText(error) }}</div>
      <button class="btn" style="width: 100%; margin-top: 8px;" :disabled="loading" @click="submit">
        {{ loading ? $t('註冊中…') : $t('註冊') }}
      </button>
      <div class="muted" style="margin-top: 12px; text-align: center;"> {{ $t('已有賬號？') }}<router-link to="/login">{{ $t('登錄') }}</router-link>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t } from '@/i18n';

import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { register, sendRegisterCode, fetchRegisterConfig, fetchCaptchaConfig, type RegisterConfig, type CaptchaConfig, getInviteBenefit, type InviteBenefit } from '@/api';
import CaptchaInput from '@/components/CaptchaInput.vue';
import { setToken } from '@/api/client';
import { refreshAuth } from '@/auth';
import { levelDiscount } from '@/composables/member-level';
import { getRefCode } from '@/ref';

const route = useRoute();
const router = useRouter();
const username = ref('');
const password = ref('');
const email = ref('');
const phone = ref('');
const code = ref('');
const inviteCode = ref('');
const benefit = ref<InviteBenefit | null>(null);
const benefitLoading = ref(false);
const benefitError = ref('');
let benefitTimer: ReturnType<typeof setTimeout> | undefined;
let benefitVersion = 0;
watch(inviteCode, value => {
  const version = ++benefitVersion;
  clearTimeout(benefitTimer);
  benefit.value = null;
  benefitError.value = '';
  benefitLoading.value = !!value.trim();
  if (!value.trim()) return;
  benefitTimer = setTimeout(async () => {
    const result = await getInviteBenefit(value.trim());
    if (version !== benefitVersion) return;
    benefitLoading.value = false;
    if (result.error) benefitError.value = $t("推薦權益查詢失敗，可重試；註冊時將再次校驗。");
    else benefit.value = result.data || null;
  }, 300);
});
const error = ref('');
const loading = ref(false);
const cfg = ref<RegisterConfig | null>(null);
const captchaCfg = ref<CaptchaConfig>({ login: false, register: true, order: false, reset: true });
const captchaId = ref('');
const captchaCode = ref('');
const captchaRef = ref<InstanceType<typeof CaptchaInput> | null>(null);

// 註冊方式（多選數組；單選時無切換器）
type RegisterMethod = 'username' | 'email' | 'phone';
const methods = computed<RegisterMethod[]>(() =>
  (cfg.value?.methods?.length ? cfg.value.methods : ['username']) as RegisterMethod[]
);
const activeMethod = ref<RegisterMethod>('username');

watch(methods, (list) => {
  if (!list.includes(activeMethod.value)) {
    activeMethod.value = list.includes('username') ? 'username' : list[0];
  }
});

function methodLabel(m: string) {
  return m === 'email' ? $t("郵箱驗證") : m === 'phone' ? $t("手機驗證") : $t("用戶名");
}

// 驗證碼發送
const sending = ref(false);
const cooldown = ref(0);
let cdTimer: ReturnType<typeof setInterval> | null = null;

function startCooldown() {
  cooldown.value = 60;
  cdTimer = setInterval(() => {
    cooldown.value -= 1;
    if (cooldown.value <= 0 && cdTimer) {
      clearInterval(cdTimer);
      cdTimer = null;
    }
  }, 1000);
}

async function doSendCode(channel: 'email' | 'phone') {
  const target = channel === 'email' ? email.value.trim() : phone.value.trim();
  if (!target) {
    error.value = channel === 'email' ? $t("請先填寫郵箱") : $t("請先填寫手機號");
    return;
  }
  if (captchaCfg.value.register && !captchaCode.value) {
    error.value = $t("請先輸入圖形驗證碼");
    return;
  }
  sending.value = true;
  error.value = '';
  const { error: err } = await sendRegisterCode(target, channel, captchaCfg.value.register ? { captcha_id: captchaId.value, captcha_code: captchaCode.value } : undefined);
  sending.value = false;
  if (err) {
    error.value = err;
    captchaRef.value?.refresh();
    return;
  }
  window.alert($t("驗證碼已發送，請查收"));
  startCooldown();
}

// 推廣歸因預填：URL ?ref= 優先；否則讀 localStorage（推廣鏈接進站捕獲的 30 天歸因）
onMounted(async () => {
  const ref = Array.isArray(route.query.ref) ? route.query.ref[0] : route.query.ref;
  if (typeof ref === 'string' && ref) {
    inviteCode.value = ref;
  } else {
    const stored = getRefCode();
    if (stored) inviteCode.value = stored;
  }
  cfg.value = await fetchRegisterConfig();
  captchaCfg.value = await fetchCaptchaConfig();
});
onUnmounted(() => { if (cdTimer) clearInterval(cdTimer); clearTimeout(benefitTimer); benefitVersion++; });

async function submit() {
  if (!username.value || !password.value) {
    error.value = $t("用戶名和密碼必填");
    return;
  }
  if (activeMethod.value === 'email' && !email.value.trim()) {
    error.value = $t("請填寫郵箱");
    return;
  }
  if (activeMethod.value === 'phone' && !phone.value.trim()) {
    error.value = $t("請填寫手機號");
    return;
  }
  if ((activeMethod.value === 'email' || activeMethod.value === 'phone') && !code.value.trim()) {
    error.value = $t("請填寫驗證碼");
    return;
  }
  if (captchaCfg.value.register && !captchaCode.value) {
    error.value = $t("請輸入圖形驗證碼");
    return;
  }
  loading.value = true;
  error.value = '';
  const { data, error: err } = await register({
    username: username.value,
    password: password.value,
    email: email.value.trim() || undefined,
    phone: phone.value.trim() || undefined,
    code: code.value.trim() || undefined,
    invite_code: inviteCode.value.trim() || undefined,
    captcha_id: captchaId.value || undefined,
    captcha_code: captchaCode.value || undefined,
  });
  loading.value = false;
  if (err || !data) {
    error.value = err || $t("註冊失敗");
    captchaRef.value?.refresh();
    return;
  }
  setToken(data.token);
  await refreshAuth();
  router.push(typeof route.query.redirect === 'string' ? route.query.redirect : '/member');
}
</script>

<style scoped>
.method-tabs {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  padding: 4px;
  background: #f3f4f6;
  border-radius: 8px;
}
.method-tab {
  flex: 1;
  padding: 7px 0;
  font-size: 13px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: #6b7280;
  cursor: pointer;
  transition: all 0.15s;
}
.method-tab.active {
  background: #fff;
  color: var(--zc-primary);
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(15, 23, 42, 0.08);
}
</style>
