<template>
  <div class="ticket-detail">
    <div class="card" style="margin-bottom: 16px;">
      <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px;">
        <h2>{{ ticket?.ticket_no }}</h2>
        <div class="actions">
          <span :class="ticket?.priority === 'urgent_paid' ? 'badge red' : 'badge gray'">
            {{ ticket?.priority === 'urgent_paid' ? $t('已加急') : $t('普通優先級') }}
          </span>
          <span :class="statusBadge">{{ statusText }}</span>
        </div>
      </div>
      <div class="muted" style="margin-top: 4px;">
        {{ ticket?.type === 'presale' ? $t('售前') : $t('售後') }} {{ $t('· 創建於') }} {{ fmtTime(ticket?.created_at || 0) }}
      </div>
      <div class="actions" style="margin-top: 12px;" v-if="ticket">
        <button class="btn secondary" v-if="urgentAvailable"
                :disabled="urging" type="button" @click.stop.prevent="openUrgent">
          {{ urging ? $t('處理中…') : urgentFee > 0 ? $t('付費加急（{0}）', [formatMoney(urgentFee)]) : $t('免費加急') }}
        </button>
        <template v-if="ticket.status === 'resolved' && !ticket.satisfaction">
          <span class="muted">{{ $t('對本單服務評價：') }}</span>
          <select v-model.number="rateValue" style="padding: 4px;">
            <option :value="5">{{ $t('★★★★★ 很滿意') }}</option>
            <option :value="4">{{ $t('★★★★ 滿意') }}</option>
            <option :value="3">{{ $t('★★★ 一般') }}</option>
            <option :value="2">{{ $t('★★ 不滿意') }}</option>
            <option :value="1">{{ $t('★ 很不滿意') }}</option>
          </select>
          <button class="btn secondary" @click="doRate">{{ $t('提交評價') }}</button>
        </template>
        <span v-if="ticket.satisfaction" class="muted">{{ $t('已評價：') }}{{ '★'.repeat(ticket.satisfaction) }}</span>
      </div>
      <div v-if="actionError" class="error" style="margin-top: 8px;">{{ uiText(actionError) }}</div>
      <div v-if="urgentError" class="error" style="margin-top:8px;">{{ uiText(urgentError) }}</div>
      <div v-if="urgentOk" class="success" style="margin-top: 8px;" role="status">{{ urgentOk.already_urgent ? $t('工單已加急，本次未重複扣費') : urgentOk.fee_cents > 0 ? $t('加急成功，已扣費 {0}，客服將優先處理', [formatMoney(urgentOk.fee_cents)]) : $t('已免費加急，客服將優先處理') }}</div>
    </div>

    <!-- 會話流（內部備註後端已過濾；user 右側藍、admin 左側灰、system 居中） -->
    <div class="card" style="margin-bottom: 16px;">
      <div class="chat">
        <div v-for="m in messages" :key="m.id" :class="`msg ${m.sender_type}`">
          <div v-if="m.sender_type !== 'user'" class="muted" style="margin-bottom: 2px;">
            {{ m.sender_type === 'admin' ? $t('客服') : $t('系統') }} · {{ fmtTime(m.created_at) }}
          </div>
          <div v-else class="muted" style="margin-bottom: 2px; text-align: right;">{{ fmtTime(m.created_at) }}</div>
          <div>{{ m.content }}</div>
        </div>
        <div v-if="!messages.length" class="muted" style="text-align: center;">{{ $t('加載中…') }}</div>
      </div>
    </div>

    <!-- 回覆 -->
    <div class="card" v-if="ticket && ticket.status !== 'closed'">
      <div class="field">
        <label>{{ $t('追加回復') }}</label>
        <textarea class="input" v-model="replyContent" rows="3" :placeholder="$t('補充信息或回覆客服')"></textarea>
      </div>
      <div v-if="replyError" class="error" style="margin-bottom: 8px;">{{ uiText(replyError) }}</div>
      <button class="btn" :disabled="replying || !replyContent.trim()" @click="doReply">{{ replying ? $t('發送中…') : $t('發送') }}</button>
    </div>
    <dialog ref="urgentDialog" class="urgent-dialog" aria-labelledby="urgent-title" @cancel.prevent="closeUrgent">
      <h3 id="urgent-title">{{ quotedFee > 0 ? $t('確認付費加急') : $t('確認免費加急') }}</h3>
      <p>{{ quotedFee > 0 ? $t('本次將從賬户餘額扣除 {0}，確認後客服將優先處理。', [formatMoney(quotedFee)]) : $t('本次加急免費，確認後客服將優先處理。') }}</p>
      <div class="urgent-dialog-actions">
        <button type="button" class="btn secondary" :disabled="urging" @click.stop.prevent="closeUrgent">{{ $t('取消') }}</button>
        <button type="button" class="btn btn-primary" :disabled="urging" @click.stop.prevent="doUrgent">{{ urging ? $t('處理中…') : quotedFee > 0 ? $t('確認支付 {0}', [formatMoney(quotedFee)]) : $t('確認免費加急') }}</button>
      </div>
    </dialog>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import { ref, computed, onMounted, nextTick } from 'vue';
import { useRoute } from 'vue-router';
import { getTicket, replyTicket, rateTicket, payUrgent, type TicketItem, type TicketMessage } from '@/api';
import { formatMoney } from '@/api/client';

const route = useRoute();
const ticketNo = String(route.params.no || '');

const ticket = ref<TicketItem | null>(null);
const messages = ref<TicketMessage[]>([]);
const replyContent = ref('');
const replyError = ref('');
const replying = ref(false);
const actionError = ref('');
const urging = ref(false);
const urgentOk = ref<{ paid: boolean; fee_cents: number; already_urgent?: boolean } | null>(null);
const urgentAvailable = ref(false);
const urgentFee = ref(0);
const urgentError = ref('');
const quotedFee = ref(0);
const urgentDialog = ref<HTMLDialogElement | null>(null);
const rateValue = ref(5);

const statusText = computed(() =>
  ({ get open() { return $t("待處理"); }, get processing() { return $t("處理中"); }, get resolved() { return $t("已解決"); }, get closed() { return $t("已關閉"); } } as Record<string, string>)[ticket.value?.status || ''] || ticket.value?.status
);
const statusBadge = computed(() =>
  ({ open: 'badge orange', processing: 'badge blue', resolved: 'badge green', closed: 'badge gray' } as Record<string, string>)[ticket.value?.status || ''] || 'badge gray'
);

onMounted(reload);

async function reload() {
  const { data, error } = await getTicket(ticketNo);
  urgentAvailable.value = false;
  if (error) { actionError.value = error; return; }
  if (data) {
    urgentAvailable.value = !!data.urgent_available;
    urgentFee.value = data.urgent_fee_cents || 0;
    urgentError.value = data.urgent_error || '';
    ticket.value = data.ticket;
    messages.value = data.messages || [];
  }
}

async function doReply() {
  replying.value = true;
  replyError.value = '';
  const { error } = await replyTicket(ticketNo, replyContent.value.trim());
  replying.value = false;
  if (error) {
    replyError.value = error;
    return;
  }
  replyContent.value = '';
  reload();
}

async function openUrgent() {
  if (urging.value) return;
  urging.value = true;
  actionError.value = '';
  await reload();
  urging.value = false;
  if (urgentAvailable.value) {
    quotedFee.value = urgentFee.value;
    await nextTick();
    urgentDialog.value?.showModal();
  }
}
function closeUrgent() { if (!urging.value) urgentDialog.value?.close(); }
async function doUrgent() {
  if (urging.value || !urgentDialog.value?.open) return;
  urging.value = true;
  actionError.value = '';
  const { data, error } = await payUrgent(ticketNo, quotedFee.value);
  urging.value = false;
  urgentDialog.value?.close();
  if (error || !data) {
    actionError.value = error || $t("加急失敗（餘額不足？）");
    return;
  }
  if (!data.paid) {
    actionError.value = data.error || $t("加急失敗");
    return;
  }
  urgentOk.value = data;
  reload();
}

async function doRate() {
  actionError.value = '';
  const { error } = await rateTicket(ticketNo, rateValue.value);
  if (error) {
    actionError.value = error;
    return;
  }
  reload();
}

function fmtTime(ts: number): string {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '';
}
</script>

<style scoped>
.ticket-detail { min-width:0; overflow-wrap:anywhere; }
.urgent-dialog { width:min(420px,calc(100vw - 32px)); box-sizing:border-box; border:0; border-radius:14px; padding:24px; margin:auto; color:#1f2937; box-shadow:0 16px 48px #0f172a30; }
.urgent-dialog::backdrop { background:rgb(15 23 42 / 45%); }
.urgent-dialog h3 { margin:0 0 12px; font-size:20px; }
.urgent-dialog p { line-height:1.7; margin:0 0 20px; }
.urgent-dialog-actions { display:flex; justify-content:flex-end; gap:12px; flex-wrap:wrap; }
.urgent-dialog-actions .btn { min-height:44px; min-width:72px; justify-content:center; align-items:center; }
</style>
