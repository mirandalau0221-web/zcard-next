<template>
  <div>
    <MemberTabs active="tickets" />
    <div class="card" style="margin-bottom: 16px;">
      <h2 style="margin-bottom: 12px;">{{ $t('新建工單') }}</h2>
      <div class="field">
        <label>{{ $t('類型') }}</label>
        <select v-model="newType">
          <option value="presale">{{ $t('售前諮詢') }}</option>
          <option value="aftersale">{{ $t('售後問題') }}</option>
        </select>
      </div>
      <div class="field">
        <label>{{ $t('問題描述 *') }}</label>
        <textarea class="input" v-model="newContent" rows="3" :placeholder="$t('請描述您遇到的問題（訂單號、發生時間、期望結果）')"></textarea>
      </div>
      <div class="field">
        <label>{{ $t('聯繫方式（遊客必填）') }}</label>
        <input class="input" v-model="newContact" type="text" :placeholder="$t('郵箱或 QQ（登錄用戶可留空）')" />
      </div>
      <div v-if="newError" class="error" style="margin-bottom: 8px;">{{ uiText(newError) }}</div>
      <button class="btn" :disabled="creating" @click="doCreate">{{ creating ? $t('提交中…') : $t('提交工單') }}</button>
    </div>

    <div class="card">
      <h2 style="margin-bottom: 12px;">{{ $t('我的工單') }}</h2>
      <table class="list table-desktop">
        <thead><tr><th>{{ $t('工單號') }}</th><th>{{ $t('類型') }}</th><th>{{ $t('優先級') }}</th><th>{{ $t('狀態') }}</th><th>{{ $t('創建時間') }}</th><th></th></tr></thead>
        <tbody>
          <tr v-for="t in tickets" :key="t.id">
            <td>{{ t.ticket_no }}</td>
            <td>{{ t.type === 'presale' ? $t('售前') : $t('售後') }}</td>
            <td>
              <span :class="t.priority === 'urgent_paid' ? 'badge red' : t.priority === 'high' ? 'badge orange' : 'badge gray'">
                {{ priorityText(t.priority) }}
              </span>
            </td>
            <td><span :class="ticketBadge(t.status)">{{ statusText(t.status) }}</span></td>
            <td class="muted">{{ fmtTime(t.created_at) }}</td>
            <td><router-link class="btn secondary" :to="`/tickets/${t.ticket_no}`">{{ $t('查看') }}</router-link></td>
          </tr>
          <tr v-if="!tickets.length"><td colspan="6" class="muted" style="text-align: center;">{{ $t('暫無工單') }}</td></tr>
        </tbody>
      </table>
      <!-- 移動端工單卡片 -->
      <div class="table-cards">
        <div v-for="t in tickets" :key="t.id" class="mcard">
          <div class="mcard-row">
            <span class="mcard-title">{{ t.ticket_no }}</span>
            <span :class="ticketBadge(t.status)">{{ statusText(t.status) }}</span>
          </div>
          <div class="mcard-row">
            <span class="muted">
              {{ t.type === 'presale' ? $t('售前') : $t('售後') }} ·
              <span :class="t.priority === 'urgent_paid' ? 'badge red' : t.priority === 'high' ? 'badge orange' : 'badge gray'">{{ priorityText(t.priority) }}</span>
              · {{ fmtTime(t.created_at) }}
            </span>
            <router-link class="btn secondary" :to="`/tickets/${t.ticket_no}`">{{ $t('查看') }}</router-link>
          </div>
        </div>
        <div v-if="!tickets.length" class="muted" style="text-align: center; padding: 16px 0;">{{ $t('暫無工單') }}</div>
      </div>
      <div class="actions" style="margin-top: 12px;" v-if="total > pageSize">
        <button class="btn secondary" :disabled="page <= 1" @click="load(page - 1)">{{ $t('上一頁') }}</button>
        <span class="muted">{{ page }} / {{ Math.ceil(total / pageSize) }}</span>
        <button class="btn secondary" :disabled="page >= Math.ceil(total / pageSize)" @click="load(page + 1)">{{ $t('下一頁') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import MemberTabs from "@/components/MemberTabs.vue";
import { ref, onMounted } from 'vue';
import { createTicket, listMyTickets, type TicketItem } from '@/api';
import { authState } from '@/auth';

const newType = ref('presale');
const newContent = ref('');
const newContact = ref('');
const newError = ref('');
const creating = ref(false);

const tickets = ref<TicketItem[]>([]);
const page = ref(1);
const total = ref(0);
const pageSize = 10;

onMounted(() => load(1));

async function load(p: number) {
  const { data } = await listMyTickets(p, pageSize);
  if (data) {
    tickets.value = data.tickets || [];
    total.value = data.total;
    page.value = p;
  }
}

async function doCreate() {
  if (!newContent.value.trim()) {
    newError.value = $t("請描述問題");
    return;
  }
  if (!authState.loggedIn && !newContact.value.trim()) {
    newError.value = $t("遊客請填寫聯繫方式（郵箱或 QQ）");
    return;
  }
  creating.value = true;
  newError.value = '';
  const { data, error } = await createTicket({
    type: newType.value,
    content: newContent.value.trim(),
    guest_contact: newContact.value.trim() || undefined
  });
  creating.value = false;
  if (error || !data) {
    newError.value = error || $t("提交失敗");
    return;
  }
  newContent.value = '';
  load(1);
}

function priorityText(p: string): string {
  return ({ get low() { return $t("低"); }, get normal() { return $t("普通"); }, get high() { return $t("高"); }, get urgent_paid() { return $t("加急"); } } as Record<string, string>)[p] || p;
}
function statusText(s: string): string {
  return ({ get open() { return $t("待處理"); }, get processing() { return $t("處理中"); }, get resolved() { return $t("已解決"); }, get closed() { return $t("已關閉"); } } as Record<string, string>)[s] || s;
}
function ticketBadge(s: string): string {
  return ({ open: 'badge orange', processing: 'badge blue', resolved: 'badge green', closed: 'badge gray' } as Record<string, string>)[s] || 'badge gray';
}
function fmtTime(ts: number): string {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '';
}
</script>

<style scoped>
/* ── 移動端表格→卡片（桌面表格不受影響）── */
.table-cards { display: none; }
.mcard {
  border: 1px solid #f3f4f6; border-radius: 10px; padding: 12px;
  display: flex; flex-direction: column; gap: 8px; background: #fafbfc;
}
.mcard-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.mcard-title { font-weight: 600; font-size: 14px; color: #111827; word-break: break-all; text-align: left; }
.mcard .btn { padding: 6px 12px; font-size: 13px; }
@media (max-width: 768px) {
  .table-desktop { display: none; }
  .table-cards { display: flex; flex-direction: column; gap: 10px; }
}
</style>
