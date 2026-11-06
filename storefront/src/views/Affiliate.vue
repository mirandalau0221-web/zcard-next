<template>
  <div>
    <!-- 推廣中心頭部：推廣碼 + 二維碼 + 推廣鏈接 -->
    <div class="card promo-hero" v-if="my">
      <!-- 左列：推廣碼 + 推廣鏈接（縱向分區） -->
      <div class="promo-left">
        <div class="promo-block">
          <div class="promo-block-title">{{ $t('我的推廣碼') }}</div>
          <div class="promo-code-row">
            <div class="promo-code">{{ my.promo_code || my.user_id }}</div>
            <button class="btn secondary" @click="copyCode">{{ copiedCode ? $t('✓ 已複製') : $t('複製推廣碼') }}</button>
          </div>
        </div>
        <div class="promo-block">
          <div class="promo-block-title">{{ $t('推廣鏈接') }}</div>
          <div class="actions">
            <input class="input" :value="my.invite_url" readonly style="flex: 1;" />
            <button class="btn secondary" @click="copyLink">{{ copied ? $t('✓ 已複製') : $t('複製鏈接') }}</button>
          </div>
          <div class="muted" style="margin-top: 8px;">{{ affiliateConfig?.enabled ? $t('好友通過鏈接註冊/下單，符合推廣規則的訂單可獲得佣金') : $t('推廣規則以站點當前配置為準') }}</div>
        </div>
      </div>
      <!-- 右列：二維碼（垂直居中） -->
      <div class="promo-qr">
        <canvas ref="qrCanvas" width="200" height="200"></canvas>
        <div class="muted" style="text-align: center; margin-top: 6px;">{{ $t('掃碼進入推廣頁') }}</div>
        <button class="btn secondary" style="width: 100%; margin-top: 6px;" @click="downloadQr">{{ $t('下載二維碼') }}</button>
      </div>
    </div>

    <div v-if="inviteBenefit?.has_benefit" class="card" style="margin-bottom: 16px;">
      <b>{{ $t('邀請註冊專享') }}</b>
      <p>{{ $t('新客户通過你的推薦鏈接註冊，即可獲得 {0} 待遇', [inviteBenefit.level?.name || $t('專屬會員')]) }}{{ inviteBenefit.level?.display_mode === 'public' ? $t('，會員價享 {0}', [levelDiscount(inviteBenefit.level.discount)]) : '' }}。</p>
    </div>
    <!-- 收益統計 -->
    <div class="stat-grid" v-if="my">
      <div class="card"><div class="muted">{{ $t('凍結中佣金') }}</div><div class="stat-num">{{ formatMoney(my.pending_cents) }}</div></div>
      <div class="card"><div class="muted">{{ $t('可提現') }}</div><div class="stat-num" style="color: #16a34a;">{{ formatMoney(my.available_cents) }}</div></div>
      <div class="card"><div class="muted">{{ $t('累計佣金') }}</div><div class="stat-num">{{ formatMoney(my.total_cents) }}</div></div>
      <div class="card"><div class="muted">{{ $t('已提現') }}</div><div class="stat-num">{{ formatMoney(my.withdrawn_cents) }}</div></div>
      <div class="card" v-if="my.debt_cents > 0"><div class="muted">{{ $t('負債（退款扣回）') }}</div><div class="stat-num" style="color: #dc2626;">{{ formatMoney(my.debt_cents) }}</div></div>
    </div>

    <!-- 團隊概覽 + 規則説明 -->
    <div class="card" style="margin-bottom: 16px;" v-if="my && affiliateConfig">
      <div style="display: flex; justify-content: space-between; flex-wrap: wrap; gap: 8px; align-items: center;">
        <div>{{ $t('團隊：直推') }} {{ my.team_l1 }} {{ $t('人') }}<span v-if="affiliateConfig.levels >= 2"> {{ $t('· 二級') }} {{ my.team_l2 }} {{ $t('人') }}</span><span v-if="affiliateConfig.levels >= 3"> {{ $t('· 三級') }} {{ my.team_l3 }} {{ $t('人') }}</span></div>
        <span class="badge blue">{{ affiliateConfig.enabled ? $t('{0}級分銷', [levelNames[affiliateConfig.levels]]) : $t('分銷已關閉') }}</span>
      </div>
      <div class="muted" style="margin-top: 6px;">
        <template v-if="affiliateConfig.enabled">{{ $t('佣金按 {0} 計算，最多 {1} 層，比例由站長配置，凍結期後可提現。', [affiliateConfig.base === 'profit' ? $t('利潤') : $t('訂單金額'), affiliateConfig.levels]) }}</template>
        <template v-else>{{ $t('當前已關閉分銷，歷史佣金可在佣金流水中查看。') }}</template>
      </div>
    </div>

    <div v-if="configError" class="card error" role="alert">{{ uiText(configError) }} <button class="btn secondary" @click="loadAffiliateConfig">{{ $t('重試') }}</button></div>

    <div class="tabs">
      <button :class="{ active: tab === 'team' }" @click="switchTab('team')">{{ $t('我的團隊') }}</button>
      <button :class="{ active: tab === 'commissions' }" @click="switchTab('commissions')">{{ $t('佣金流水') }}</button>
    </div>

    <!-- 團隊 -->
    <div v-if="tab === 'team' && affiliateConfig" class="card">
      <div class="actions" style="margin-bottom: 8px;">
        <select v-model="teamTier" @change="loadTeam(1)" style="padding: 6px;">
          <option :value="0">{{ $t('全部層級') }}</option>
          <option v-for="level in affiliateConfig.levels" :key="level" :value="level">{{ level === 1 ? $t('直推（一級）') : $t('{0}級', [levelNames[level]]) }}</option>
        </select>
      </div>
      <table class="list">
        <thead><tr><th>{{ $t('用户') }}</th><th>{{ $t('層級') }}</th><th>{{ $t('加入時間') }}</th></tr></thead>
        <tbody>
          <tr v-for="m in team" :key="m.user_id">
            <td>{{ m.username_masked }}</td>
            <td><span class="badge blue">L{{ m.tier }}</span></td>
            <td class="muted">{{ fmtTime(m.joined_at) }}</td>
          </tr>
          <tr v-if="!team.length"><td colspan="3" class="muted" style="text-align: center;">{{ $t('暫無團隊成員') }}</td></tr>
        </tbody>
      </table>
    </div>

    <!-- 佣金流水 -->
    <div v-if="tab === 'commissions'" class="card">
      <table class="list table-desktop">
        <thead><tr><th>{{ $t('時間') }}</th><th>{{ $t('訂單') }}</th><th>{{ $t('層級') }}</th><th>{{ $t('基數') }}</th><th>{{ $t('佣金') }}</th><th>{{ $t('狀態') }}</th></tr></thead>
        <tbody>
          <tr v-for="c in commissions" :key="c.id">
            <td class="muted">{{ fmtTime(c.created_at) }}</td>
            <td>#{{ c.order_id }}</td>
            <td>L{{ c.tier }}</td>
            <td>{{ formatMoney(c.base_amount) }}</td>
            <td :class="c.amount >= 0 ? 'success' : 'error'">{{ formatSignedMoney(c.amount) }}</td>
            <td><span :class="commissionBadge(c.status)">{{ commissionText(c.status) }}</span></td>
          </tr>
          <tr v-if="!commissions.length"><td colspan="6" class="muted" style="text-align: center;">{{ $t('暫無佣金記錄') }}</td></tr>
        </tbody>
      </table>
      <!-- 移動端佣金卡片 -->
      <div class="table-cards">
        <div v-for="c in commissions" :key="c.id" class="mcard">
          <div class="mcard-row">
            <span class="mcard-title">#{{ c.order_id }} · L{{ c.tier }}</span>
            <span :class="commissionBadge(c.status)">{{ commissionText(c.status) }}</span>
          </div>
          <div class="mcard-row">
            <span class="muted">{{ fmtTime(c.created_at) }} {{ $t('· 基數') }} {{ formatMoney(c.base_amount) }}</span>
            <span :class="c.amount >= 0 ? 'success' : 'error'" style="font-weight: 700;">{{ formatSignedMoney(c.amount) }}</span>
          </div>
        </div>
        <div v-if="!commissions.length" class="muted" style="text-align: center; padding: 16px 0;">{{ $t('暫無佣金記錄') }}</div>
      </div>
      <div class="actions" style="margin-top: 12px;" v-if="commissionsTotal > pageSize">
        <button class="btn secondary" :disabled="commissionsPage <= 1" @click="loadCommissions(commissionsPage - 1)">{{ $t('上一頁') }}</button>
        <span class="muted">{{ commissionsPage }} / {{ Math.ceil(commissionsTotal / pageSize) }}</span>
        <button class="btn secondary" :disabled="commissionsPage >= Math.ceil(commissionsTotal / pageSize)" @click="loadCommissions(commissionsPage + 1)">{{ $t('下一頁') }}</button>
      </div>
    </div>

    <!-- 提現（獨立頁入口） -->
    <div v-if="tab === 'withdraw'" class="card" style="display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap;">
      <div>
        <b>{{ $t('佣金提現') }}</b>
        <div class="muted" style="margin-top: 4px;">{{ $t('支付寶 / 微信收款碼 / USDT TRC20 · 提現記錄 · 工單支持') }}</div>
      </div>
      <router-link class="btn btn-primary" to="/withdraw">{{ $t('去提現') }}</router-link>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import { getInviteBenefit, type InviteBenefit } from '@/api';
import { levelDiscount } from '@/composables/member-level';
import { ref, computed, onMounted, nextTick } from 'vue';
import QRCode from 'qrcode';
import {
  myAffiliate, listTeam, listCommissions, createWithdrawal, fetchAffiliateConfig,
  type AffiliateConfig,
  type MyAffiliateReply, type TeamMember, type CommissionItem
} from '@/api';
import { formatMoney, formatSignedMoney } from '@/api/client';

const affiliateConfig = ref<AffiliateConfig | null>(null);
const configError = ref('');
const levelNames = computed(() => ['', $t("一"), $t("二"), $t("三")]);

const tab = ref<'team' | 'commissions' | 'withdraw'>('team');
const my = ref<MyAffiliateReply | null>(null);
const copied = ref(false);
const copiedCode = ref(false);
const qrCanvas = ref<HTMLCanvasElement | null>(null);

const team = ref<TeamMember[]>([]);
const teamTier = ref(0);
const commissions = ref<CommissionItem[]>([]);
const commissionsPage = ref(1);
const commissionsTotal = ref(0);
const pageSize = 15;

const withdrawYuan = ref<number | null>(null);
const withdrawMethod = ref('alipay');
const withdrawAccount = ref('');
const withdrawError = ref('');
const withdrawOk = ref<{ withdrawal_id: number; amount_cents: number; fee_cents: number; credited_cents: number } | null>(null);
const withdrawing = ref(false);

const inviteBenefit = ref<InviteBenefit | null>(null);
onMounted(async () => {
  const [{ data }] = await Promise.all([myAffiliate(), loadAffiliateConfig()]);
  my.value = data;
  if (data?.promo_code) {
    const result = await getInviteBenefit(data.promo_code);
    inviteBenefit.value = result.data || null;
  }
  // 推廣鏈接二維碼（canvas 渲染；鏈接為空跳過）
  if (data?.invite_url) {
    await nextTick();
    if (qrCanvas.value) {
      try {
        await QRCode.toCanvas(qrCanvas.value, data.invite_url, { width: 200, margin: 2 });
      } catch { /* 渲染失敗忽略——複製鏈接兜底 */ }
    }
  }
});

function switchTab(t: 'team' | 'commissions' | 'withdraw') {
  tab.value = t;
  if (t === 'commissions' && !commissions.value.length) loadCommissions(1);
}

async function loadAffiliateConfig() {
  configError.value = '';
  affiliateConfig.value = await fetchAffiliateConfig();
  if (!affiliateConfig.value) {
    configError.value = $t("分銷設置加載失敗，請重試。");
    return;
  }
  if (teamTier.value > affiliateConfig.value.levels) teamTier.value = 0;
  await loadTeam(1);
}

async function loadTeam(page: number) {
  if (!affiliateConfig.value) return;
  const { data } = await listTeam(teamTier.value || undefined, page, pageSize);
  if (data) team.value = data.members || [];
}

async function loadCommissions(page: number) {
  const { data } = await listCommissions(page, pageSize);
  if (data) {
    commissions.value = data.commissions || [];
    commissionsTotal.value = data.total;
    commissionsPage.value = page;
  }
}

async function copyLink() {
  if (!my.value?.invite_url) return;
  try {
    await navigator.clipboard.writeText(my.value.invite_url);
    copied.value = true;
    setTimeout(() => (copied.value = false), 2000);
  } catch {
    /* 剪貼板不可用（HTTP 環境）——手選複製 */
  }
}

async function copyCode() {
  const code = my.value?.promo_code || String(my.value?.user_id || '');
  if (!code) return;
  try {
    await navigator.clipboard.writeText(code);
    copiedCode.value = true;
    setTimeout(() => (copiedCode.value = false), 2000);
  } catch { /* 忽略 */ }
}

function downloadQr() {
  if (!qrCanvas.value) return;
  try {
    const link = document.createElement('a');
    link.download = `promo-qrcode-${my.value?.promo_code || my.value?.user_id}.png`;
    link.href = qrCanvas.value.toDataURL('image/png');
    link.click();
  } catch { /* 忽略 */ }
}

async function doWithdraw() {
  if (!withdrawYuan.value || withdrawYuan.value <= 0) {
    withdrawError.value = $t("請輸入提現金額");
    return;
  }
  if (!withdrawAccount.value.trim()) {
    withdrawError.value = $t("請填寫收款賬號");
    return;
  }
  withdrawing.value = true;
  withdrawError.value = '';
  withdrawOk.value = null;
  const { data, error } = await createWithdrawal({
    amount_cents: Math.round(withdrawYuan.value * 100),
    method_type: withdrawMethod.value,
    account: withdrawAccount.value.trim()
  });
  withdrawing.value = false;
  if (error || !data) {
    withdrawError.value = error || $t("申請失敗（可提餘額不足？）");
    return;
  }
  withdrawOk.value = data;
  withdrawYuan.value = null;
  withdrawAccount.value = '';
  const m = await myAffiliate();
  my.value = m.data;
}

function commissionText(s: string): string {
  return ({ get pending_confirm() { return $t("凍結中"); }, get available() { return $t("可提現"); }, get withdrawn() { return $t("已提現"); }, get reversed() { return $t("已回沖"); } } as Record<string, string>)[s] || s;
}
function commissionBadge(s: string): string {
  return ({ pending_confirm: 'badge orange', available: 'badge green', withdrawn: 'badge blue', reversed: 'badge red' } as Record<string, string>)[s] || 'badge gray';
}
function fmtTime(ts: number): string {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '';
}
</script>

<style scoped>
/* ── 移動端表格→卡片（佣金流水；桌面表格不受影響）── */
.table-cards { display: none; }
.mcard {
  border: 1px solid #f3f4f6; border-radius: 10px; padding: 12px;
  display: flex; flex-direction: column; gap: 8px; background: #fafbfc;
}
.mcard-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.mcard-title { font-weight: 600; font-size: 14px; color: #111827; word-break: break-all; text-align: left; }
@media (max-width: 768px) {
  .table-desktop { display: none; }
  .table-cards { display: flex; flex-direction: column; gap: 10px; }
}

/* 推廣頭部：左（碼+鏈接）/ 右（二維碼）經典兩欄；窄屏縱向堆疊 */
.promo-hero {
  display: flex; gap: 24px; align-items: center;
  background: linear-gradient(135deg, var(--zc-primary-soft), #fff);
  border-color: var(--zc-primary-tint);
  flex-wrap: wrap;
}
@media (max-width: 640px) { .promo-hero { flex-direction: column-reverse; } }
.promo-left {
  flex: 1; min-width: 260px;
  display: flex; flex-direction: column; gap: 18px;
}
.promo-block + .promo-block { border-top: 1px dashed var(--zc-primary-tint); padding-top: 16px; }
.promo-block-title { font-size: 13px; font-weight: 600; color: #6b7280; margin-bottom: 8px; }
.promo-code-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.promo-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 32px; font-weight: 800; letter-spacing: 4px; color: var(--zc-primary);
}
.promo-qr {
  background: #fff; border: 1px solid #e5e7eb; border-radius: 12px;
  padding: 12px; flex-shrink: 0;
}
.promo-qr canvas { display: block; border-radius: 6px; }
</style>
