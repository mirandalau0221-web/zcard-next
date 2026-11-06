<script setup lang="ts">
import { ref, watch } from 'vue';
import { uiText, t as $t } from '@/i18n';
import { sendOrderAccessCode, recoverOrderAccess } from '@/api';
const props = defineProps<{ orderNo?: string }>();
const emit = defineEmits<{ recovered: [orderNo: string] }>();
const number = ref(props.orderNo || '');
const email = ref('');
const code = ref('');
const busy = ref(false);
const sent = ref(false);
const error = ref('');
watch(() => props.orderNo, no => { number.value = no || ''; });
watch([number, email], () => { sent.value = false; code.value = ''; error.value = ''; });
async function sendCode() {
  if (busy.value || !number.value.trim() || !email.value.trim()) return;
  busy.value = true; error.value = '';
  const r = await sendOrderAccessCode(number.value.trim(), email.value.trim());
  busy.value = false;
  if (r.error) error.value = r.error;
  else sent.value = true;
}
async function recover() {
  if (busy.value) return;
  busy.value = true; error.value = '';
  const r = await recoverOrderAccess(number.value.trim(), email.value.trim(), code.value.trim());
  busy.value = false;
  if (r.error) error.value = r.error;
  else emit('recovered', number.value.trim());
}
</script>
<template>
  <form class="order-recovery" @submit.prevent="recover">
    <p>{{ $t('通過下單郵箱驗證，查看訂單與交付結果') }}</p>
    <label v-if="!orderNo">{{ $t('訂單號') }}<input v-model="number" class="input" required autocomplete="off" /></label>
    <label>{{ $t('下單郵箱') }}<input v-model="email" class="input" type="email" required autocomplete="email" /></label>
    <button type="button" class="btn secondary" :disabled="busy || !number.trim() || !email.trim()" @click="sendCode">{{ $t('發送驗證碼') }}</button>
    <p v-if="sent" role="status">{{ $t('若郵箱與訂單匹配，將收到驗證碼，請檢查郵箱') }}</p>
    <label>{{ $t('郵箱驗證碼') }}<input v-model="code" class="input" required inputmode="numeric" autocomplete="one-time-code" minlength="6" maxlength="6" pattern="[0-9]{6}" /></label>
    <p v-if="error" role="alert" class="error">{{ uiText(error) }}</p>
    <button type="submit" class="btn btn-primary" :disabled="busy || !sent">{{ busy ? $t('驗證中…') : $t('驗證並查看訂單') }}</button>
  </form>
</template>
<style scoped>
.order-recovery{display:flex;flex-direction:column;gap:12px;max-width:440px;width:100%;margin:16px auto;text-align:left;box-sizing:border-box}
.order-recovery label{display:flex;flex-direction:column;gap:6px}.order-recovery input{width:100%;box-sizing:border-box}.order-recovery p{margin:0;font-size:14px}
</style>
