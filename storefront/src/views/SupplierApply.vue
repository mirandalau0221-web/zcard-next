<template>
  <div class="supplier-page">
    <!-- 説明 -->
    <div class="card" style="margin-bottom: 16px;">
      <div style="display: flex; justify-content: space-between; flex-wrap: wrap; gap: 8px; align-items: center;">
        <div>{{ $t('對接申請（供貨）') }}</div>
        <span class="tag">{{ $t('審核通過後下發 app_id / app_key') }}</span>
      </div>
      <div class="muted" style="margin-top: 6px; line-height: 1.7;"> {{ $t('申請通過後，你的第三方站點（acg-faka / dujiao-next / 另一套 ZCard）可把本站作為上游供貨方， 填本站地址 + 下方憑據即可對接，無需改動對方代碼。協議在申請時選定，一個賬户對應一種面板。 下游下單從賬户「供貨餘額」扣款，餘額不足請先充值。') }} </div>
    </div>

    <!-- 申請表單 -->
    <div class="card apply-card">
      <div class="apply-title">{{ $t('提交新申請') }}</div>
      <div class="muted" style="margin: -6px 0 16px;">{{ $t('選擇對接協議並填寫站點信息，審核通過後即可獲得憑據') }}</div>
      <div class="field-label">{{ $t('對接協議') }}</div>
      <div class="protocol-grid">
        <label
          v-for="p in protocols"
          :key="p.value"
          class="protocol-option"
          :class="{ active: form.protocol === p.value }"
        >
          <input v-model="form.protocol" type="radio" :value="p.value" class="protocol-radio" />
          <span class="protocol-dot" aria-hidden="true"></span>
          <span class="protocol-head">
            <b>{{ p.label }}</b>
          </span>
          <span class="muted protocol-desc">{{ p.desc }}</span>
        </label>
      </div>
      <div class="apply-form-grid">
        <div class="field">
          <label>{{ $t('站點 / 店鋪名') }} <i class="req">*</i></label>
          <input v-model="form.display_name" class="input" :placeholder="$t('如：星空卡商城')" maxlength="100" />
        </div>
        <div class="field">
          <label>{{ $t('聯繫方式') }}</label>
          <input v-model="form.contact" class="input" :placeholder="$t('QQ / 郵箱 / 手機')" maxlength="255" />
        </div>
      </div>
      <div class="field">
        <label>{{ $t('交付回調地址（選填）') }}</label>
        <input v-model="form.notify_url" class="input" :placeholder="$t('直接填域名即可，支持 http/https')" maxlength="500" />
        <div class="muted field-hint">{{ $t('示例：shop.example.com/callback 或 http(s)://shop.example.com/callback（不填則無回調）') }}</div>
      </div>
      <div class="field">
        <label>{{ $t('申請理由（選填）') }}</label>
        <textarea v-model="form.apply_reason" class="input" style="min-height: 80px;" :placeholder="$t('審核時參考，可簡述站點規模與主營類目')" maxlength="500"></textarea>
      </div>
      <div class="apply-footer">
        <button class="btn apply-submit" :disabled="submitting" @click="submit">{{ submitting ? $t('提交中…') : $t('提交申請') }}</button>
        <span v-if="formError" class="error">{{ uiText(formError) }}</span>
      </div>
    </div>

    <!-- 我的對接賬户 -->
    <div class="card">
      <div style="font-weight: 600; margin-bottom: 12px;">{{ $t('我的對接賬户（{0}）', [accounts.length]) }}</div>
      <div v-if="loading" class="muted">{{ $t('加載中…') }}</div>
      <div v-else-if="!accounts.length" class="muted">{{ $t('暫無申請，提交上方表單即可開始對接') }}</div>
      <div v-else class="account-list" style="display: flex; flex-direction: column; gap: 10px;">
        <div v-for="a in accounts" :key="a.id" class="account-item" style="border: 1px solid #e5e6e8; border-radius: 10px; padding: 12px;">
          <div class="account-head">
            <div class="account-title">
              <b>{{ a.display_name }}</b>
              <span class="tag" style="margin-left: 8px;">{{ protocolLabel(a.protocol) }}</span>
              <span class="tag" :style="statusStyle(a.status)">{{ statusLabel(a.status) }}</span>
            </div>
            <div class="account-actions">
              <span v-if="a.status === 'approved'" class="account-balance"> {{ $t('餘額') }} <b style="color: var(--zc-primary); font-size: 16px;">{{ formatMoney(a.balance_cache || 0) }}</b>
              </span>
              <button v-if="a.status === 'approved'" class="btn" style="padding: 6px 14px; font-size: 13px;" @click="openRecharge(a)">{{ $t('充值') }}</button>
              <button v-if="a.status === 'applying'" class="btn secondary" @click="cancel(a)">{{ $t('撤銷申請') }}</button>
              <button v-if="a.status === 'approved'" class="btn secondary" @click="showCredentials(a)">{{ credOpenId === a.id ? $t('收起憑據') : $t('查看憑據') }}</button>
              <button v-if="a.status === 'approved'" class="btn secondary" @click="regenerate(a)">{{ $t('重置密鑰') }}</button>
            </div>
          </div>
          <div v-if="a.apply_reason" class="muted" style="margin-top: 6px;">{{ $t('申請理由：') }}{{ a.apply_reason }}</div>
          <div v-if="a.review_note" class="muted" style="margin-top: 4px; color: #b45309;">{{ $t('審核意見：') }}{{ a.review_note }}</div>
          <div class="muted" style="margin-top: 4px;">{{ $t('申請時間：') }}{{ fmt(a.created_at) }}<template v-if="a.reviewed_at"> {{ $t('· 審核時間：') }}{{ fmt(a.reviewed_at) }}</template></div>

          <!-- 憑據 + 對接指引 -->
          <div v-if="a.status === 'approved' && credOpenId === a.id" class="account-credentials" style="margin-top: 12px; background: #f9fafb; border-radius: 8px; padding: 12px;">
            <div v-if="credLoading" class="muted">{{ $t('加載憑據…') }}</div>
            <template v-else-if="credentials">
              <div style="font-weight: 600; margin-bottom: 8px;">{{ $t('憑據（請妥善保存）') }}</div>
              <div class="cred-row">
                <span class="muted" style="width: 72px;">app_id</span>
                <code style="flex: 1; background: #fff; border: 1px solid #e5e6e8; border-radius: 6px; padding: 6px 10px; font-size: 13px;">{{ credentials.api_key }}</code>
                <button class="btn secondary" @click="copy(credentials.api_key)">{{ copied === 'key' ? '✓' : $t('複製') }}</button>
              </div>
              <div class="cred-row">
                <span class="muted" style="width: 72px;">app_key</span>
                <code style="flex: 1; background: #fff; border: 1px solid #e5e6e8; border-radius: 6px; padding: 6px 10px; font-size: 13px;">{{ credentials.api_secret }}</code>
                <button class="btn secondary" @click="copy(credentials.api_secret)">{{ copied === 'secret' ? '✓' : $t('複製') }}</button>
              </div>
              <div v-if="regeneratedSecret" style="margin-bottom: 8px; padding: 8px 10px; background: #fef3c7; border-radius: 6px; font-size: 13px;"> {{ $t('新密鑰（舊密鑰已失效）：') }}<code>{{ regeneratedSecret }}</code>
              </div>
            </template>

            <!-- IP 白名單（審核通過後可配置；空 = 所有 IP 放行） -->
            <div style="margin-top: 14px; padding-top: 12px; border-top: 1px dashed #e5e6e8;">
              <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 6px;">
                <b style="font-size: 13px;">{{ $t('🔒 IP 白名單') }}</b>
                <span class="muted" style="font-size: 12px;">{{ (a.ip_whitelist?.length || 0) ? $t('已限制 {0} 條', [a.ip_whitelist!.length]) : $t('未限制（所有 IP 可調用）') }}</span>
              </div>
              <div class="muted" style="margin: 6px 0 8px; line-height: 1.7; font-size: 12px;"> {{ $t('出於安全考慮，可限制只有指定 IP 的服務器才能調用本賬户的對接接口。') }} <b>{{ $t('不填寫 = 默認所有 IP 都可以請求') }}</b>{{ $t('；填寫後僅白名單內 IP 可用（支持精確 IP 如 1.2.3.4，或網段如 10.0.0.0/24，最多 20 條）。服務器出口 IP 變更後請及時更新，否則接口將被拒絕。') }} </div>
              <div v-if="(a.ip_whitelist || []).length" style="display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px;">
                <span v-for="ip in a.ip_whitelist" :key="ip" class="ip-chip">
                  <code>{{ ip }}</code>
                  <button class="ip-chip-x" :title="$t('移除')" @click="removeWhitelistIP(a, ip)">✕</button>
                </span>
              </div>
              <div style="display: flex; gap: 8px; flex-wrap: wrap;">
                <input
                  v-model="whitelistInput"
                  class="input"
                  style="flex: 1; min-width: 0; width: 100%;"
                  :placeholder="$t('添加 IP 或網段，如 1.2.3.4 / 10.0.0.0/24')"
                  @keyup.enter="addWhitelistIP(a)"
                />
                <button class="btn secondary" :disabled="whitelistSaving" @click="addWhitelistIP(a)">{{ $t('添加') }}</button>
              </div>
              <div v-if="whitelistError" style="color: #dc2626; font-size: 12px; margin-top: 6px;">{{ uiText(whitelistError) }}</div>
            </div>
            <div class="muted" style="margin-top: 10px; line-height: 1.8;">
              <b style="color: #1f2329;">{{ $t('對接方式') }}</b><br />
              <template v-if="a.protocol === 'acg_faka'"> {{ $t('① 對方 acg-faka 後台 →「店鋪共享 / 共享店鋪」→ 新增店鋪') }}<br /> {{ $t('② 類型選「異次元(原生)」，地址填本站地址（') }}<code>{{ origin }}</code>）<br /> {{ $t('③ 商户ID 填上面的 app_id，密鑰填 app_key，保存前會自動連通測試') }} </template>
              <template v-else-if="a.protocol === 'dujiao_next'"> {{ $t('① 對方 dujiao-next 後台 →「站點連接」→ 新增連接') }}<br /> {{ $t('② 協議選 dujiao-next，地址填本站地址（') }}<code>{{ origin }}</code>/api/v1/upstream）<br /> {{ $t('③ API Key 填上面的 app_id，API Secret 填 app_key') }} </template>
              <template v-else> {{ $t('① 對方 ZCard 後台 →「貨源渠道」→ 新增連接（協議選 zcard）') }}<br /> {{ $t('② 地址填本站地址（') }}<code>{{ origin }}</code>{{ $t('/api/supply），app_id 填上面的 api_key、密鑰填 api_secret') }} </template>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 充值彈窗（大廠收銀風格：餘額卡 + 檔位網格 + 支付方式 + 主按鈕） -->
    <div v-if="rechargeOpen" class="recharge-mask" @click.self="closeRecharge">
      <div class="recharge-modal">
        <div class="recharge-head">
          <div style="font-weight: 700; font-size: 16px;">{{ $t('供貨餘額充值') }}</div>
          <button class="recharge-close" @click="closeRecharge">✕</button>
        </div>

        <!-- 餘額卡 -->
        <div class="balance-hero">
          <div class="muted" style="color: rgba(255,255,255,0.75);">{{ $t('當前餘額') }}</div>
          <div style="font-size: 28px; font-weight: 800; letter-spacing: 0.5px;">{{ formatMoney(rechargeTarget?.balance_cache || 0) }}</div>
          <div style="font-size: 12px; opacity: 0.8; margin-top: 4px;">{{ rechargeTarget?.display_name }} · {{ protocolLabel(rechargeTarget?.protocol || '') }}</div>
        </div>

        <div class="recharge-body">
          <!-- 成功反饋 -->
          <div v-if="rechargeDone" class="recharge-done">
            <div style="font-size: 42px;">✅</div>
            <div style="font-weight: 700; margin-top: 8px;">{{ $t('充值成功') }}</div>
            <div class="muted" style="margin-top: 4px;">{{ $t('已到賬') }} {{ formatBaseMoney(rechargeDoneAmount) }}{{ $t('，支付完成後餘額自動更新') }}</div>
            <button class="btn" style="margin-top: 16px; width: 100%;" @click="closeRecharge">{{ $t('完成') }}</button>
          </div>

          <template v-else>
            <!-- 金額檔位 -->
            <div v-if="!rechargeRedirect && !rechargeQrcode" class="recharge-section">
              <div class="recharge-label">{{ $t('充值金額') }}</div>
              <div class="tier-grid">
                <button
                  v-for="t in presetTiers"
                  :key="t"
                  class="tier-card"
                  :class="{ active: rechargeYuan === t }"
                  @click="rechargeYuan = t; focusCustom = false"
                >
                  <div>{{ formatBaseMoney(t * 100) }}</div>
                  <div v-if="supplierGiftOf(t)" style="font-size: 11px; margin-top: 2px; color: #16a34a;">{{ $t('送') }} {{ formatBaseMoney(supplierGiftOf(t)) }}</div>
                </button>
                <button class="tier-card custom" :class="{ active: !presetTiers.includes(rechargeYuan as number) && rechargeYuan !== null }" @click="focusCustom = true"> {{ $t('自定義') }} </button>
              </div>
              <div v-if="focusCustom" class="custom-input">
                <input class="input" v-model.number="rechargeYuan" type="number" min="1" step="0.01" :placeholder="$t('輸入金額（{0}）', [getBaseCurrency().code])" autofocus />
              </div>
              <div class="muted" style="margin-top: 6px;">{{ $t('限額') }} {{ formatBaseMoney(rechargeMeta?.min_amount || 1000) }} ~ {{ formatBaseMoney(rechargeMeta?.max_amount || 500000) }}</div>
              <div v-if="supplierGiftOf(rechargeYuan) > 0" style="margin-top: 6px; font-size: 13px; color: #16a34a; font-weight: 600;"> {{ $t('本單到賬') }} {{ formatBaseMoney(Math.round((rechargeYuan || 0) * 100) + supplierGiftOf(rechargeYuan)) }}{{ $t('（本金 + 贈送）') }} </div>
            </div>

            <!-- 支付方式 -->
            <div v-if="!rechargeRedirect && !rechargeQrcode" class="recharge-section">
              <div class="recharge-label">{{ $t('支付方式') }}</div>
              <PayChannelGrid :options="supplierPayOptions" :channel="rechargeChannel" :method="rechargeMethod" @select="(channel, method) => { rechargeChannel = channel; rechargeMethod = method; }"><template #empty>{{ $t('暫無可用的供貨充值方式，請聯繫管理員') }}</template></PayChannelGrid>
            </div>

            <div v-if="rechargeError" style="color: #dc2626; font-size: 13px; margin: 8px 0;">{{ uiText(rechargeError) }}</div>

            <PaymentBreakdown :quote="(rechargeRedirect || rechargeQrcode) ? paidQuote : quote" :loading="!rechargeRedirect && !rechargeQrcode && quoteLoading" :error="quoteError" recharge @retry="refreshQuote" />
            <!-- 支付跳轉 -->
            <div v-if="rechargeRedirect" style="margin-top: 12px;">
              <button class="btn" style="width:100%" @click="openRechargeCheckout">{{ $t('去支付（跳轉收銀台）') }}</button>
              <div class="muted" style="text-align: center; margin-top: 6px;">{{ $t('支付完成後餘額自動到賬') }}</div>
            </div>
            <div v-else-if="rechargeQrcode" style="margin-top: 12px; text-align: center;">
              <img :src="rechargeQrcode" :alt="$t('支付二維碼')" style="width: 180px; border-radius: 10px;" />
              <div class="muted" style="margin-top: 6px;">{{ $t('請使用對應 App 掃碼支付，完成後餘額自動到賬') }}</div>
            </div>

            <!-- 主按鈕 -->
            <button
              v-if="!rechargeRedirect && !rechargeQrcode"
              class="recharge-submit"
              :disabled="recharging || !rechargeYuan || rechargeYuan <= 0 || !rechargeChannel || !quote || quoteLoading"
              @click="doRecharge"
            >
              {{ recharging ? $t('創建支付單…') : quote ? $t('立即支付 {0}', [formatPaymentAmount(quote)]) : $t('等待計算金額') }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import QRCode from 'qrcode';
import { submitPaymentForm } from "@/utils/payment-form";
import PayChannelGrid from '@/components/PayChannelGrid.vue';
import PaymentBreakdown from '@/components/PaymentBreakdown.vue';
import { usePaymentQuote } from '@/composables/payment-quote';
import type { PaymentQuote } from '@/api';
import { flattenPayOptions } from '@/composables/pay-options';
import { computed, onMounted, ref } from 'vue';
import {
  listMySupplierAccounts, submitSupplierApplication, getSupplierCredentials,
  regenerateSupplierSecret, cancelSupplierApplication, createSupplierRecharge,
  setSupplierIPWhitelist, fetchPaymentChannels, type ChannelItem,
  type SupplierAccount, type SupplierCredentials,
} from '@/api';
import { api, formatMoney, getBaseCurrency, formatBaseMoney, formatPaymentAmount } from '@/api/client';

const protocols = [
  { value: 'zcard', label: 'ZCard', get desc() { return $t("另一套 ZCard 系統對接（X-Supply 四頭簽名）"); } },
  { value: 'dujiao_next', label: 'dujiao-next', get desc() { return $t("對方 dujiao-next 站對接（Dujiao-Next 三頭簽名）"); } },
  { value: 'acg_faka', label: 'acg-faka', get desc() { return $t("對方 acg-faka 站對接（共享店鋪原生協議）"); } },
];

const accounts = ref<SupplierAccount[]>([]);
const loading = ref(false);
const submitting = ref(false);
const formError = ref('');
const form = ref({ protocol: 'zcard', display_name: '', contact: '', apply_reason: '', notify_url: '' });

const credOpenId = ref<number>(0);
const credLoading = ref(false);
const credentials = ref<SupplierCredentials | null>(null);
const regeneratedSecret = ref('');
const copied = ref('');

// ── IP 白名單管理（審核通過後的賬户；空 = 所有 IP 放行）──
const whitelistInput = ref('');
const whitelistSaving = ref(false);
const whitelistError = ref('');

function validIPOrCIDR(v: string): boolean {
  const s = v.trim();
  if (!s) return false;
  if (s.includes('/')) {
    const [ip, mask] = s.split('/');
    const m = Number(mask);
    if (!ip || !Number.isInteger(m) || m < 0 || m > (ip.includes(':') ? 128 : 32)) return false;
    return validIPOrCIDR(ip);
  }
  return /^(\d{1,3}\.){3}\d{1,3}$/.test(s) || s.includes(':'); // IPv4 點分或 IPv6（含冒號粗校驗，後端精確校驗）
}

async function saveWhitelist(a: SupplierAccount, ips: string[]) {
  whitelistSaving.value = true;
  whitelistError.value = '';
  try {
    const { data, error } = await setSupplierIPWhitelist(a.id, ips);
    if (error) {
      whitelistError.value = error;
      return;
    }
    // 本地同步（列表態 + 展開態）
    a.ip_whitelist = data?.ip_whitelist || ips;
    const row = accounts.value.find((x) => x.id === a.id);
    if (row) row.ip_whitelist = a.ip_whitelist;
    whitelistInput.value = '';
  } finally {
    whitelistSaving.value = false;
  }
}

async function addWhitelistIP(a: SupplierAccount) {
  const ip = whitelistInput.value.trim();
  if (!ip) return;
  if (!validIPOrCIDR(ip)) {
    whitelistError.value = $t("格式不正確：請填精確 IP（1.2.3.4）或網段（10.0.0.0/24）");
    return;
  }
  const next = [...(a.ip_whitelist || [])];
  if (next.includes(ip)) {
    whitelistError.value = $t("該 IP 已在白名單中");
    return;
  }
  if (next.length >= 20) {
    whitelistError.value = $t("白名單最多 20 條");
    return;
  }
  await saveWhitelist(a, [...next, ip]);
}

async function removeWhitelistIP(a: SupplierAccount, ip: string) {
  if (!confirm($t("確認從白名單移除 {0}？", [ip]))) return;
  await saveWhitelist(a, (a.ip_whitelist || []).filter((x) => x !== ip));
}

const origin = typeof location !== 'undefined' ? location.origin : '';

// ── 充值（收銀彈窗）──
const rechargeOpen = ref(false);
const rechargeTarget = ref<SupplierAccount | null>(null);
const rechargeYuan = ref<number | null>(null);
const presetTiers = ref<number[]>([100, 200, 500, 1000, 2000]);
const supplierGiftTiers = ref<{ amount: number; gift_balance: number }[]>([]);
const focusCustom = ref(false);
const rechargeChannels = ref<ChannelItem[]>([]);
const rechargeChannel = ref('');
const rechargeMethod = ref('');
const paidQuote = ref<PaymentQuote | null>(null);
const { quote, quoteLoading, quoteError, refreshQuote } = usePaymentQuote(() =>
  rechargeChannel.value && rechargeYuan.value && rechargeYuan.value > 0 ? {
    channel: rechargeChannel.value, method: rechargeMethod.value, scene: 'supply_recharge', amount_cents: Math.round(rechargeYuan.value * 100),
  } : null,
);
const supplierPayOptions = computed(() => flattenPayOptions(rechargeChannels.value.filter(c => c.driver !== 'wallet')));
const recharging = ref(false);
const rechargeError = ref('');
const rechargeRedirect = ref('');
const rechargeParams = ref<Record<string, string> | null>(null);
function openRechargeCheckout() {
  try {
    if (rechargeParams.value) submitPaymentForm(rechargeRedirect.value, rechargeParams.value);
    else window.open(rechargeRedirect.value, '_blank', 'noopener');
  } catch {
    rechargeError.value = $t("支付參數異常，請重新選擇支付方式");
  }
}
const rechargeQrcode = ref('');
const rechargeMeta = ref<{ min_amount: number; max_amount: number } | null>(null);
const rechargeDone = ref(false);
const rechargeDoneAmount = ref(0);

async function openRecharge(a: SupplierAccount) {
  supplierGiftTiers.value = [];
  presetTiers.value = [100, 200, 500, 1000, 2000];
  rechargeTarget.value = a;
  rechargeYuan.value = presetTiers.value[1] ?? 100; // 默認第二檔
  focusCustom.value = false;
  rechargeError.value = '';
  rechargeRedirect.value = '';
  rechargeParams.value = null;
  rechargeQrcode.value = '';
  rechargeDone.value = false;
  rechargeOpen.value = true;
  // 支付渠道 + 供貨充值限額（獨立配置組 supplier_recharge，與錢包充值隔離）
  const [ch, cfg] = await Promise.all([fetchPaymentChannels('supply_recharge'), api.get<{ entries: { key: string; value_json: string }[] }>('/config')]);
  rechargeChannels.value = ch.data?.channels || [];
  rechargeChannel.value = supplierPayOptions.value[0]?.channel || '';
  rechargeMethod.value = supplierPayOptions.value[0]?.method || '';
  const find = (k: string) => cfg.data?.entries?.find((e) => e.key === k)?.value_json;
  const min = find('supplier_recharge.min_amount');
  const max = find('supplier_recharge.max_amount');
  if (min && max) {
    rechargeMeta.value = { min_amount: Number(JSON.parse(min)), max_amount: Number(JSON.parse(max)) };
  }
  const gt = find('supplier_recharge.gift_tiers');
  if (gt) {
    try {
      const arr = JSON.parse(gt);
      if (Array.isArray(arr) && arr.length) {
        supplierGiftTiers.value = arr
          .filter((t: any) => Number(t?.amount) > 0)
          .map((t: any) => ({ amount: Number(t.amount), gift_balance: Number(t.gift_balance) || 0 }))
          .sort((a: { amount: number }, b: { amount: number }) => a.amount - b.amount);
        // 配置了贈送檔位：預設檔位對齊配置（買家按檔位充值享贈送）
        presetTiers.value = supplierGiftTiers.value.map((t) => t.amount / 100);
        rechargeYuan.value = presetTiers.value[1] ?? presetTiers.value[0];
      }
    } catch { /* 非法配置忽略 */ }
  }
}

// 當前金額命中的贈送（精確匹配檔位；自定義金額未命中 = 無贈送）
function supplierGiftOf(yuan: number | null): number {
  if (yuan === null || !supplierGiftTiers.value.length) return 0;
  const cents = Math.round(yuan * 100);
  return supplierGiftTiers.value.find((t) => t.amount === cents)?.gift_balance || 0;
}

function closeRecharge() {
  rechargeOpen.value = false;
  rechargeTarget.value = null;
}

async function doRecharge() {
  if (recharging.value || !quote.value || quoteLoading.value) return;
  if (!rechargeTarget.value) return;
  if (!rechargeYuan.value || rechargeYuan.value <= 0) {
    rechargeError.value = $t("請輸入充值金額");
    return;
  }
  if (!rechargeChannel.value) {
    rechargeError.value = $t("請選擇支付方式");
    return;
  }
  recharging.value = true;
  rechargeError.value = '';
  rechargeRedirect.value = '';
  rechargeParams.value = null;
  rechargeQrcode.value = '';
  const { data, error } = await createSupplierRecharge(rechargeTarget.value.id, {
    amount_cents: Math.round(rechargeYuan.value * 100),
    channel: rechargeChannel.value,
    method: rechargeMethod.value || undefined,
    quote_key: quote.value.quote_key,
  });
  recharging.value = false;
  if (error || !data) {
    rechargeError.value = error || $t("創建失敗");
    await refreshQuote();
    return;
  }
  paidQuote.value = data.quote || quote.value;
  // 支付載荷三形態（與充值/支付頁同構）
  if (data.type === 'redirect') {
    let url = data.payload;
    try { url = JSON.parse(data.payload).url || url; } catch { /* 原文即 URL */ }
    rechargeRedirect.value = url;
  } else if (data.type === 'qrcode') {
    let content = data.payload;
    try { content = JSON.parse(data.payload).code_url || content; } catch { /* 原文即二維碼內容 */ }
    try {
      rechargeQrcode.value = content.startsWith('https://') || content.startsWith('http://') || content.startsWith('data:image')
        ? content
        : await QRCode.toDataURL(content, { width: 220, margin: 1, errorCorrectionLevel: 'M' });
    } catch { rechargeError.value = $t("無法生成支付二維碼，請重新發起支付"); }
  } else if (data.type === 'params') {
    try {
      const p = JSON.parse(data.payload);
      rechargeRedirect.value = p.url || '';
      rechargeParams.value = p.params || {};
      rechargeError.value = p.url ? '' : $t("支付參數異常");
    } catch {
      rechargeError.value = $t("支付參數異常");
    }
  }
  // 輪詢賬户餘額（支付完成後刷新）
  pollBalance(rechargeTarget.value.id);
}

let balanceTimer: ReturnType<typeof setInterval> | null = null;
function pollBalance(id: number) {
  if (balanceTimer) clearInterval(balanceTimer);
  const started = Date.now();
  balanceTimer = setInterval(async () => {
    const { data } = await listMySupplierAccounts();
    if (!data) return;
    const acc = data.accounts.find((a) => a.id === id);
    if (acc) {
      const before = rechargeTarget.value?.balance_cache || 0;
      if (acc.balance_cache > before) {
        rechargeDone.value = true;
        rechargeDoneAmount.value = acc.balance_cache - before;
        if (balanceTimer) clearInterval(balanceTimer);
        await load();
      }
    }
    // 5 分鐘超時停止輪詢
    if (Date.now() - started > 5 * 60 * 1000 && balanceTimer) {
      clearInterval(balanceTimer);
    }
  }, 3000);
}

function protocolLabel(p: string) {
  return ({ zcard: 'ZCard', dujiao_next: 'dujiao-next', acg_faka: 'acg-faka' } as any)[p] || p;
}
function statusLabel(s: string) {
  return ({ get applying() { return $t("待審核"); }, get approved() { return $t("已通過"); }, get rejected() { return $t("已駁回"); }, get disabled() { return $t("已禁用"); } } as any)[s] || s;
}
function statusStyle(s: string) {
  return ({
    applying: 'background: #fef3c7; color: #b45309;',
    approved: 'background: #dcfce7; color: #15803d;',
    rejected: 'background: #fee2e2; color: #b91c1c;',
    disabled: 'background: #e5e7eb; color: #4b5563;',
  } as any)[s] || '';
}
function fmt(ts: number) {
  return ts ? new Date(ts * 1000).toLocaleString(localeTag.value) : '-';
}
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    copied.value = text.length > 30 ? 'secret' : 'key';
    setTimeout(() => (copied.value = ''), 1500);
  } catch { /* 剪貼板不可用時忽略 */ }
}

async function load() {
  loading.value = true;
  try {
    const { data } = await listMySupplierAccounts();
    accounts.value = data?.accounts || [];
  } finally {
    loading.value = false;
  }
}

async function submit() {
  formError.value = '';
  if (!form.value.display_name.trim()) {
    formError.value = $t("請填寫站點/店鋪名");
    return;
  }
  // 回調地址歸一：裸域名自動補 https://；http/https 均支持
  let notifyURL = form.value.notify_url.trim();
  if (notifyURL && !notifyURL.includes('://')) notifyURL = `https://${notifyURL}`;
  if (notifyURL && !/^https?:\/\//.test(notifyURL)) {
    formError.value = $t("回調地址僅支持 http/https（或直接填域名）");
    return;
  }
  submitting.value = true;
  try {
    const { error } = await submitSupplierApplication({
      protocol: form.value.protocol,
      display_name: form.value.display_name.trim(),
      contact: form.value.contact.trim(),
      apply_reason: form.value.apply_reason.trim(),
      notify_url: notifyURL,
    });
    if (error) {
      formError.value = error;
      return;
    }
    form.value.display_name = '';
    form.value.contact = '';
    form.value.apply_reason = '';
    form.value.notify_url = '';
    await load();
  } finally {
    submitting.value = false;
  }
}

async function showCredentials(a: SupplierAccount) {
  if (credOpenId.value === a.id) {
    credOpenId.value = 0;
    credentials.value = null;
    return;
  }
  credOpenId.value = a.id;
  regeneratedSecret.value = '';
  credLoading.value = true;
  try {
    const { data, error } = await getSupplierCredentials(a.id);
    if (error) {
      alert(error);
      credOpenId.value = 0;
      return;
    }
    credentials.value = data;
  } finally {
    credLoading.value = false;
  }
}

async function regenerate(a: SupplierAccount) {
  if (!confirm($t("確認重置「{0}」的密鑰？重置後舊密鑰立即失效。", [a.display_name]))) return;
  const { data, error } = await regenerateSupplierSecret(a.id);
  if (error) {
    alert(error);
    return;
  }
  credentials.value = { ...credentials.value!, api_secret: data!.api_secret };
  regeneratedSecret.value = data!.api_secret;
}

async function cancel(a: SupplierAccount) {
  if (!confirm($t("確認撤銷「{0}」的申請？", [a.display_name]))) return;
  const { error } = await cancelSupplierApplication(a.id);
  if (error) {
    alert(error);
    return;
  }
  await load();
}

onMounted(load);
</script>

<style scoped>
.supplier-page, .account-item, .account-title { min-width: 0; overflow-wrap: anywhere; }
.account-head { display:flex; align-items:center; justify-content:space-between; flex-wrap:wrap; gap:12px; }
.account-title { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.account-actions { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.account-actions .btn { min-height:44px; display:inline-flex; align-items:center; justify-content:center; white-space:nowrap; }
.account-balance { font-size:14px; }
.cred-row { display:grid; grid-template-columns:72px minmax(0,1fr) auto; align-items:center; gap:8px; margin-bottom:10px; }
.cred-row code { min-width:0; white-space:pre-wrap; overflow-wrap:anywhere; }
.supplier-page :deep(.muted) { color:#64748b; }
@media(max-width:640px) {
 .account-head, .account-actions { width:100%; }
 .account-actions { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); }
 .account-balance { grid-column:1/-1; }
 .account-actions .btn { padding:8px; }
 .account-title b { flex-basis:100%; }
 .account-title .tag { margin-left:0 !important; }
 .cred-row { grid-template-columns:minmax(0,1fr) auto; }
 .cred-row > span { grid-column:1/-1; }
 .apply-form-grid, .protocol-grid { grid-template-columns:minmax(0,1fr) !important; }
}

/* ── 提交新申請表單（大廠表單規範：分區標籤 + 卡片式單選 + 16px 節奏）── */
.apply-card { margin-bottom: 16px; }
.apply-title { font-size: 16px; font-weight: 700; color: #111827; margin-bottom: 12px; }
.field-label { font-size: 13px; font-weight: 600; color: #374151; margin-bottom: 8px; }
.req { color: #ef4444; font-style: normal; margin-left: 2px; }
.field { margin-bottom: 16px; }
.field:last-of-type { margin-bottom: 0; }
.field label { display: block; font-size: 13px; color: #6b7280; margin-bottom: 6px; }
.field-hint { margin-top: 6px; }
.apply-form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 0 16px; }

/* 協議卡片：隱藏原生 radio，自定義圓點 + 選中藍框 */
.protocol-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; margin-bottom: 20px; }
.protocol-option {
  position: relative;
  display: flex; flex-direction: column; gap: 4px;
  border: 1.5px solid #e5e6e8; border-radius: 10px;
  padding: 14px 16px; cursor: pointer;
  transition: border-color 0.15s, background 0.15s, box-shadow 0.15s;
  background: #fff;
}
.protocol-option:hover { border-color: var(--zc-primary-muted); }
.protocol-option.active {
  border-color: var(--zc-primary); background: var(--zc-primary-soft);
  box-shadow: 0 0 0 1px var(--zc-primary) inset;
}
.protocol-radio { position: absolute; opacity: 0; pointer-events: none; }
.protocol-dot {
  position: absolute; top: 16px; right: 14px;
  width: 16px; height: 16px; border-radius: 50%;
  border: 2px solid #d1d5db; background: #fff;
  transition: border-color 0.15s;
}
.protocol-dot::after {
  content: ''; position: absolute; inset: 2px;
  border-radius: 50%; background: var(--zc-primary);
  transform: scale(0); transition: transform 0.12s;
}
.protocol-option.active .protocol-dot { border-color: var(--zc-primary); }
.protocol-option.active .protocol-dot::after { transform: scale(1); }
.protocol-head { font-size: 14px; color: #1f2329; padding-right: 22px; }
.protocol-desc { line-height: 1.6; }

.apply-footer { display: flex; align-items: center; gap: 12px; margin-top: 4px; flex-wrap: wrap; }
.apply-submit { padding: 10px 32px; font-size: 15px; }

/* 充值收銀彈窗（大廠交互：蒙層 + 圓角卡片 + 餘額卡 + 檔位網格 + 主按鈕） */
.recharge-mask {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.55);
  backdrop-filter: blur(2px);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
}
.recharge-modal {
  width: 100%;
  max-width: 420px;
  background: #fff;
  border-radius: 16px;
  box-shadow: 0 24px 64px rgba(15, 23, 42, 0.25);
  overflow: hidden;
  animation: recharge-in 0.18s ease-out;
}
@keyframes recharge-in {
  from { opacity: 0; transform: translateY(12px) scale(0.98); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.recharge-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 18px 0;
}
.recharge-close {
  border: none;
  background: #f3f4f6;
  color: #6b7280;
  width: 28px;
  height: 28px;
  border-radius: 50%;
  cursor: pointer;
  font-size: 13px;
  line-height: 1;
}
.recharge-close:hover { background: #e5e7eb; }
.balance-hero {
  margin: 14px 18px 0;
  border-radius: 12px;
  background: linear-gradient(135deg, var(--zc-primary-strong), var(--zc-primary) 60%, var(--zc-primary-bright));
  color: #fff;
  padding: 16px 18px;
}
.recharge-body { padding: 14px 18px 18px; }
.recharge-section { margin-bottom: 14px; }
.recharge-label {
  font-size: 13px;
  font-weight: 600;
  color: #374151;
  margin-bottom: 8px;
}
.tier-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
}
.tier-card {
  border: 1px solid #e5e6e8;
  background: #fff;
  border-radius: 10px;
  padding: 10px 4px;
  cursor: pointer;
  font-size: 15px;
  font-weight: 700;
  color: #1f2329;
  transition: all 0.12s;
}
.tier-card:hover { border-color: var(--zc-primary-muted); }
.tier-card.active {
  border-color: var(--zc-primary);
  background: var(--zc-primary-soft);
  color: var(--zc-primary);
  box-shadow: 0 0 0 1px var(--zc-primary) inset;
}
.tier-symbol { font-size: 12px; font-weight: 500; margin-right: 2px; }
.custom-input { margin-top: 8px; }
.recharge-submit {
  width: 100%;
  margin-top: 6px;
  padding: 13px;
  border: none;
  border-radius: 10px;
  background: var(--zc-primary);
  color: #fff;
  font-size: 15px;
  font-weight: 700;
  cursor: pointer;
  transition: background 0.15s;
}
.recharge-submit:hover:not(:disabled) { background: var(--zc-primary-hover); }
.recharge-submit:disabled { opacity: 0.5; cursor: not-allowed; }
.recharge-done { text-align: center; padding: 18px 0 6px; }

/* IP 白名單條目 chip */
.ip-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--zc-primary-soft);
  border: 1px solid var(--zc-primary-border);
  border-radius: 6px;
  padding: 3px 4px 3px 10px;
  font-size: 13px;
}
.ip-chip code { font-size: 12.5px; color: var(--zc-primary-hover); }
.ip-chip-x {
  border: none;
  background: var(--zc-primary-tint);
  color: var(--zc-primary-hover);
  width: 18px;
  height: 18px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 11px;
  line-height: 1;
}
.ip-chip-x:hover { background: var(--zc-primary-muted); }
</style>
