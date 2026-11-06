<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, formatMoney, getToken } from "@/api/client";
import { getBalance, fetchPaymentChannels, createPayment, type Product } from "@/api";
import { authState } from "@/auth";
import { newRequestId } from "../../../packages/request-id";
const props = defineProps<{ product: Product }>();
type Option = { id: string; name: string };
type Offer = { offer_id: string; name: string; price_cents: number; stock: number };
type Quote = {
  quote_id: string;
  amount_cents: number;
  currency: string;
  expires_at: number;
  offer_name: string;
};
type Buy = {
  quote_id: string;
  request_id: string;
  amount_cents: number;
  order_no?: string;
  channel?: string;
};
type Snapshot = {
  state: string;
  phone_number?: string;
  otp_code?: string;
  otp_message?: string;
  can_cancel?: boolean;
  can_finish?: boolean;
  expires_at?: string;
  cancel_available_at?: string;
  settlement_state?: string;
  refunded_amount_cents?: number;
  version?: number;
  phase?: string;
  operation_status?: string;
  operation_action?: string;
  operation_request_id?: string;
  refund_status?: string;
  message?: string;
};
type Order = {
  supply_order_id: string;
  offer_name?: string;
  amount: number;
  status: string;
  fulfillment?: { sms?: Snapshot };
};
type Action = { action: "cancel" | "finish"; operation_id: string };
const countries = ref<Option[]>([]),
  platforms = ref<Option[]>([]),
  offers = ref<Offer[]>([]),
  orders = ref<Order[]>([]);
const country = ref(""),
  platform = ref(""),
  keyword = ref(""),
  selected = ref("");
const page = ref(1),
  historyPage = ref(1),
  hasMore = ref(false),
  balance = ref<number>();
const offersLoading = ref(false),
  optionsLoading = ref(false),
  busy = ref(false),
  error = ref(""),
  notice = ref(""),
  historyError = ref(""),
  copied = ref("");
const loading = computed(() => offersLoading.value || optionsLoading.value);
const quote = ref<Quote>(),
  pending = ref<Buy>(),
  pendingActions = ref<Record<string, Action>>({});
const path = `/products/${encodeURIComponent(String(props.product.id))}/sms`;
const storageKey = computed(() => `zcard_sms_purchase:${authState.username}:${props.product.id}`);
const loggedIn = computed(() => authState.loggedIn || !!getToken());
const choice = computed(() => offers.value.find((o) => o.offer_id === selected.value));
const titles: Record<string, string> = {
  get allocating() { return $t("獲取號碼中"); },
  get waiting_sms() { return $t("等待短信"); },
  get sms_received() { return $t("已收到短信"); },
  get completed() { return $t("已結束"); },
  get canceled() { return $t("已取消"); },
  get expired() { return $t("已過期"); },
  get rejected() { return $t("取號失敗"); },
  get review() { return $t("結果待核對"); },
};
let closed = false,
  generation = 0,
  optionsGeneration = 0,
  historyGeneration = 0,
  timer: ReturnType<typeof setTimeout> | undefined,
  searchTimer: ReturnType<typeof setTimeout> | undefined;
function restoreIntent() {
  pending.value = undefined;
  pendingActions.value = {};
  try {
    const v = JSON.parse(sessionStorage.getItem(storageKey.value) || "null");
    if (v?.quote_id && v?.request_id && Number(v.amount_cents) > 0) pending.value = v;
    const actions = JSON.parse(sessionStorage.getItem(storageKey.value + ":actions") || "{}");
    for (const [id, a] of Object.entries(actions) as [string, Action][]) {
      if (a?.operation_id && ["cancel", "finish"].includes(a.action)) pendingActions.value[id] = a;
    }
  } catch {
    /* Ignore invalid browser data. */
  }
}
function saveActions() {
  try {
    sessionStorage.setItem(storageKey.value + ":actions", JSON.stringify(pendingActions.value));
  } catch {}
}
type Session = { key: string; token: string | null };
function session(): Session {
  return { key: storageKey.value, token: getToken() };
}
function sameSession(value: Session) {
  return !closed && value.key === storageKey.value && value.token === getToken();
}
watch(storageKey, () => {
  generation++;
  optionsGeneration++;
  historyGeneration++;
  orders.value = [];
  offers.value = [];
  quote.value = undefined;
  balance.value = undefined;
  busy.value = false;
  optionsLoading.value = offersLoading.value = false;
  error.value = notice.value = historyError.value = "";
  if (timer) clearTimeout(timer);
  if (loggedIn.value) restoreIntent();
  else { pending.value = undefined; pendingActions.value = {}; }
  void loadOptions();
  void loadOffers();
  if (loggedIn.value) {
    void loadHistory();
    void loadBalance();
  }
});
watch(loggedIn, (value) => {
  if (!value) {
    orders.value = [];
    quote.value = undefined;
    pending.value = undefined;
    pendingActions.value = {};
    balance.value = undefined;
    busy.value = false;
    optionsLoading.value = offersLoading.value = false;
    error.value = notice.value = historyError.value = "";
    generation++;
    optionsGeneration++;
    historyGeneration++;
    if (timer) clearTimeout(timer);
    void loadOptions();
    void loadOffers();
  } else {
    restoreIntent();
    void loadHistory();
    void loadBalance();
  }
});
function savePending(value?: Buy) {
  pending.value = value;
  try {
    if (value) sessionStorage.setItem(storageKey.value, JSON.stringify(value));
    else sessionStorage.removeItem(storageKey.value);
  } catch {
    /* In-memory intent is retained when browser storage is unavailable. */
  }
}
async function loadBalance() {
  const member = session();
  const r = await getBalance();
  if (sameSession(member) && r.data) balance.value = r.data.available_cents;
}
async function loadOptions() {
  const version = ++optionsGeneration;
  optionsLoading.value = true;
  error.value = "";
  const r = await api.get<{ countries: Option[]; platforms: Option[] }>(`${path}/options`, {
    country_id: country.value || "0",
    keyword: keyword.value,
  });
  if (closed || version !== optionsGeneration) return;
  optionsLoading.value = false;
  if (r.error) {
    error.value = r.error;
    return;
  }
  countries.value = r.data?.countries || [];
  platforms.value = r.data?.platforms || [];
  if (platform.value && !platforms.value.some((p) => p.id === platform.value)) platform.value = "";
}
async function loadOffers() {
  const version = ++generation;
  offers.value = [];
  selected.value = "";
  quote.value = undefined;
  error.value = "";
  hasMore.value = false;
  if (!country.value || !platform.value) {
    offersLoading.value = false;
    return;
  }
  offersLoading.value = true;
  const r = await api.get<{ offers: Offer[]; has_more: boolean }>(`${path}/offers`, {
    country_id: country.value,
    platform_id: platform.value,
    page: page.value,
    page_size: 50,
  });
  if (closed || version !== generation) return;
  offersLoading.value = false;
  error.value = r.error || "";
  if (r.data) {
    offers.value = r.data.offers || [];
    hasMore.value = !!r.data.has_more;
  }
}
watch(country, () => {
  platform.value = "";
  page.value = 1;
  offers.value = [];
  selected.value = "";
  quote.value = undefined;
  void loadOptions();
});
watch(platform, () => {
  page.value = 1;
  void loadOffers();
});
watch(keyword, () => {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void loadOptions(), 300);
});
async function confirmQuote() {
  if (!loggedIn.value || !choice.value || busy.value) return;
  const version = generation,
    offer = choice.value;
  const displayedPrice = Number(offer.price_cents);
  const member = session();
  busy.value = true;
  error.value = "";
  const r = await api.post<Quote>(`${path}/quotes`, {
    offer_id: offer.offer_id,
    country_id: country.value,
    platform_id: platform.value,
    page: page.value,
    page_size: 50,
  });
  if (!sameSession(member)) return;
  busy.value = false;
  if (version !== generation || offer.offer_id !== selected.value) return;
  if (r.error) {
    error.value = r.error;
    return;
  }
  if (r.data) {
    quote.value = r.data;
    offer.price_cents = Number(r.data.amount_cents);
    if (Number(r.data.amount_cents) !== displayedPrice)
      notice.value = $t("價格已更新，請確認最新金額後支付。");
  }
}
async function buy() {
  if (!loggedIn.value || busy.value) return;
  if (!pending.value) {
    if (!quote.value || quote.value.expires_at <= Date.now() / 1000) {
      quote.value = undefined;
      error.value = $t("報價已過期，請重新確認價格。");
      return;
    }
    try {
      savePending({
        quote_id: quote.value.quote_id,
        request_id: newRequestId(),
        amount_cents: Number(quote.value.amount_cents),
      });
    } catch (e) {
      error.value = e instanceof Error ? e.message : $t("無法生成購買標識");
      return;
    }
  }
  const member = session();
  busy.value = true;
  error.value = "";
  const intent = pending.value!;
  // Recover the original order before retrying payment. A lost payment response
  // must never cause another retail order or another supplier purchase.
  if (!intent.order_no) {
    const created = await api.post<{ order_no: string; total_cents: number }>(
      "/orders",
      {
        items: [{ product_id: String(props.product.id), quantity: 1 }],
        sms_quote_id: intent.quote_id,
      },
      { "Idempotency-Key": intent.request_id },
    );
    if (!sameSession(member)) return;
    if (created.error || !created.data) {
      busy.value = false;
      error.value = $t("{0}。請確認原購買結果。", [created.error || $t("建單失敗")]);
      if (created.status === 400 || created.status === 404) savePending();
      return;
    }
    if (Number(created.data.total_cents) !== intent.amount_cents) {
      busy.value = false;
      error.value = $t("訂單金額與確認報價不一致，請聯繫客服核對。");
      return;
    }
    intent.order_no = created.data.order_no;
    savePending(intent);
  }
  const current = await api.get<{ status: string }>(
    `/orders/${encodeURIComponent(intent.order_no)}`,
  );
  if (!sameSession(member)) return;
  if (current.error || !current.data) {
    busy.value = false;
    error.value = $t("{0}。請確認原購買結果。", [current.error || $t("無法讀取訂單")]);
    return;
  }
  if (!["pending_payment", "pending"].includes(current.data.status)) {
    if (["canceled", "expired"].includes(current.data.status)) {
      savePending();
      quote.value = undefined;
      busy.value = false;
      error.value = $t("原訂單已取消或過期，請重新選價。");
      return;
    }
    await purchaseAccepted();
    return;
  }
  if (!intent.channel) {
    const channels = await fetchPaymentChannels();
    if (!sameSession(member)) return;
    const wallet = channels.data?.channels?.find(
      (c) => c.driver === "wallet" && !Number(c.fee || 0),
    );
    if (!wallet) {
      busy.value = false;
      error.value = channels.error || $t("本站尚未啓用免手續費會員餘額支付，請聯繫商戶。");
      return;
    }
    intent.channel = wallet.code;
    savePending(intent);
  }
  const r = await createPayment(intent.order_no, intent.channel);
  if (!sameSession(member)) return;
  busy.value = false;
  if (r.error) {
    // Includes insufficient balance and unknown results: retry this same order.
    error.value = $t("{0}。可充值後確認原購買結果，或取消未付款訂單後重選。", [r.error]);
    return;
  }
  if (r.data) await purchaseAccepted();
}
async function purchaseAccepted() {
  savePending();
  quote.value = undefined;
  busy.value = false;
  notice.value = $t("餘額付款已確認，號碼和短信將在“我的號碼”中顯示。");
  historyPage.value = 1;
  await loadHistory();
  await loadBalance();
}
async function cancelUnpaid() {
  if (!pending.value?.order_no || busy.value) return;
  const member = session();
  busy.value = true;
  const r = await api.post(`/orders/${encodeURIComponent(pending.value.order_no)}/cancel`, {});
  if (!sameSession(member)) return;
  busy.value = false;
  if (r.error) {
    error.value = $t("{0}。請確認原購買結果。", [r.error]);
    return;
  }
  savePending();
  quote.value = undefined;
  error.value = "";
  notice.value = $t("未付款訂單已取消，可以重新選價。");
}

async function loadHistory() {
  if (closed || !loggedIn.value) return;
  const version = ++historyGeneration;
  if (timer) clearTimeout(timer);
  const r = await api.get<{
    orders: { order_no: string; offer_name: string; amount_cents: number; sms: Snapshot }[];
  }>(`${path}/sessions`, { page: historyPage.value });
  if (closed || version !== historyGeneration) return;
  historyError.value = r.error || "";
  if (r.data) {
    orders.value = (r.data.orders || []).map((o) => ({
      supply_order_id: o.order_no,
      offer_name: o.offer_name,
      amount: Number(o.amount_cents),
      status: o.sms?.state || "allocating",
      fulfillment: {
        sms: {
          ...o.sms,
          state: o.sms?.phase === "review" ? "review" : o.sms?.state || "allocating",
          settlement_state: o.sms?.refund_status === "succeeded" ? "refunded" : "paid",
          refunded_amount_cents: o.sms?.refund_status === "succeeded" ? Number(o.amount_cents) : 0,
        },
      },
    }));
    for (const o of orders.value) {
      const snap = o.fulfillment?.sms;
      const action = pendingActions.value[o.supply_order_id];
      if (
        (action &&
          snap?.operation_request_id === action.operation_id &&
          snap.operation_status &&
          snap.operation_status !== "pending") ||
        snap?.refund_status === "succeeded"
      )
        delete pendingActions.value[o.supply_order_id];
    }
    saveActions();
  }
  timer = setTimeout(() => void loadHistory(), document.hidden ? 30000 : r.error ? 10000 : 4000);
}
function changeHistory(n: number) {
  historyPage.value = n;
  orders.value = [];
  void loadHistory();
}
function changePage(n: number) {
  page.value = n;
  void loadOffers();
}
async function act(order: Order, action: "cancel" | "finish") {
  if (!loggedIn.value || busy.value) return;
  const existing = pendingActions.value[order.supply_order_id];
  if (existing && existing.action !== action) return;
  if (
    !existing &&
    !window.confirm(
      action === "cancel"
        ? $t("申請取消？上游確認退款後退還本次實付，受理不代表退款完成。")
        : $t("結束本次接碼？結束後停止接收短信，已有短信仍可查看。"),
    )
  )
    return;
  if (!existing) {
    try {
      pendingActions.value[order.supply_order_id] = { action, operation_id: newRequestId() };
    } catch (e) {
      error.value = e instanceof Error ? e.message : $t("無法生成操作標識");
      return;
    }
  }
  saveActions();
  const member = session();
  busy.value = true;
  const r = await api.post<Snapshot>(
    `/orders/${encodeURIComponent(order.supply_order_id)}/sms/${action}`,
    { request_id: pendingActions.value[order.supply_order_id].operation_id },
  );
  if (!sameSession(member)) return;
  busy.value = false;
  if (r.error) {
    error.value = r.error;
    if (r.status === 400 || r.status === 404) delete pendingActions.value[order.supply_order_id];
  } else if (r.data) {
    notice.value =
      r.data.operation_status === "pending"
        ? $t("操作已受理，正在等待服務確認。")
        : r.data.operation_status === "rejected"
          ? $t("服務未接受操作，請查看號碼當前狀態。")
          : r.data.operation_status === "review"
            ? $t("操作結果待核對，請憑訂單號聯繫客服。")
            : $t("操作已完成");
    if (
      r.data.operation_request_id === pendingActions.value[order.supply_order_id]?.operation_id &&
      r.data.operation_status !== "pending"
    )
      delete pendingActions.value[order.supply_order_id];
  }
  saveActions();
  await loadHistory();
  await loadBalance();
}
async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value);
    copied.value = $t("已複製");
  } catch {
    copied.value = $t("請手動選擇複製");
  }
}
function visible() {
  if (!document.hidden && loggedIn.value) {
    void loadHistory();
    void loadBalance();
  }
}
onMounted(() => {
  void loadOptions();
  if (loggedIn.value) {
    restoreIntent();
    void loadHistory();
    void loadBalance();
  }
  document.addEventListener("visibilitychange", visible);
});
onBeforeUnmount(() => {
  closed = true;
  generation++;
  optionsGeneration++;
  historyGeneration++;
  if (timer) clearTimeout(timer);
  if (searchTimer) clearTimeout(searchTimer);
  document.removeEventListener("visibilitychange", visible);
  orders.value = [];
});
</script>

<template>
  <div class="sms-product">
    <nav class="sms-crumb">
      <router-link to="/">{{ $t('首頁') }}</router-link><span>/</span><span>{{ product.name }}</span>
    </nav>
    <header>
      <h1>{{ product.name }}</h1>
      <p class="muted">{{ $t('選擇國家與服務，確認價格後獲取號碼。購買、收碼與取消都在這裏完成。') }}</p>
    </header>
    <div class="sms-layout">
      <section class="sms-panel" aria-labelledby="sms-select-title">
        <h2 id="sms-select-title">{{ $t('選擇號碼') }}</h2>
        <p v-if="!loggedIn" class="sms-empty"> {{ $t('可直接篩選國家、服務並查看價格，購買時需要登錄。') }} </p>
          <p v-if="loggedIn" class="sms-wallet"> {{ $t('可用餘額：') }}<strong>{{ balance === undefined ? $t('加載中') : formatMoney(balance) }}</strong
            ><router-link to="/member">{{ $t('充值') }}</router-link>
          </p>
          <p v-if="!product.sms_sales_enabled" class="sms-message"> {{ $t('暫未開放新購，已購買號碼仍可收碼和查看。') }} </p>
          <fieldset :disabled="busy || !!pending">
            <label for="sms-country">{{ $t('國家 / 地區') }}</label>
            <select id="sms-country" v-model="country">
              <option value="">{{ $t('請選擇國家或地區') }}</option>
              <option v-for="c in countries" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
            <label for="sms-search">{{ $t('搜索平台 / 服務') }}</label
            ><input
              id="sms-search"
              v-model="keyword"
              type="search"
              :placeholder="$t('平台名稱或服務 ID')"
            />
            <label for="sms-platform">{{ $t('平台 / 服務') }}</label
            ><select id="sms-platform" v-model="platform" :disabled="!country">
              <option value="">{{ $t('請選擇平台 / 服務') }}</option>
              <option v-for="p in platforms" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <p v-if="loading" class="muted" role="status">{{ $t('正在加載可用選項…') }}</p>
            <button
              v-if="error && !loading"
              type="button"
              class="btn"
              @click="country && platform ? loadOffers() : loadOptions()"
            > {{ $t('重試加載') }} </button>
            <div
              v-if="offers.length"
              class="sms-offers"
              role="radiogroup"
              :aria-label="$t('可用號碼報價')"
            >
              <label
                v-for="o in offers"
                :key="o.offer_id"
                class="sms-offer"
                :class="{ selected: selected === o.offer_id }"
                ><input
                  v-model="selected"
                  type="radio"
                  name="sms-offer"
                  :value="o.offer_id"
                  :disabled="o.stock === 0"
                  @change="quote = undefined"
                /><span
                  >{{ o.name
                  }}<small>{{
                    o.stock === -1 ? $t('可用數量不限') : $t('可用 {0} 個', [o.stock])
                  }}</small></span
                ><strong>{{ formatMoney(o.price_cents) }}</strong></label
              >
            </div>
            <p v-else-if="country && platform && !loading && !error" class="sms-empty"> {{ $t('當前篩選暫無可用號碼，可更換國家或服務。') }} </p>
            <div v-if="country && platform" class="sms-pagination">
              <button
                type="button"
                class="btn"
                :disabled="page <= 1 || loading"
                @click="changePage(page - 1)"
              > {{ $t('上一頁') }}</button
              ><span>{{ $t('第') }} {{ page }} {{ $t('頁') }}</span
              ><button
                type="button"
                class="btn"
                :disabled="!hasMore || loading"
                @click="changePage(page + 1)"
              > {{ $t('下一頁') }} </button>
            </div>
            <p class="muted">{{ $t('每次購買 1 個號碼。可用時長及取消規則以購買後的服務説明為準。') }}</p>
          </fieldset>
          <div v-if="quote || pending" class="sms-total">
            <span>{{ $t('本次實付') }}</span
            ><strong>{{ formatMoney(pending?.amount_cents ?? quote?.amount_cents ?? 0) }}</strong>
          </div>
          <p v-if="quote && !pending" class="muted"> {{ $t('報價有效至') }} {{ new Date(Number(quote.expires_at) * 1000).toLocaleTimeString(localeTag) }}
          </p>
          <router-link
            v-if="!loggedIn"
            class="btn btn-primary sms-submit"
            :to="{ path: '/login', query: { redirect: `/product/${product.id}` } }"
          >{{ $t('登錄後購買') }}</router-link>
          <button
            v-else-if="pending"
            type="button"
            class="btn btn-primary sms-submit"
            :disabled="busy"
            @click="buy"
          >
            {{ busy ? $t('確認中…') : $t('確認原購買結果') }}
          </button>
          <button
            v-else-if="quote"
            type="button"
            class="btn btn-primary sms-submit"
            :disabled="busy || !product.sms_sales_enabled"
            @click="buy"
          >
            {{ busy ? $t('購買中…') : $t('餘額支付 {0}', [formatMoney(quote.amount_cents)]) }}
          </button>
          <button
            v-else
            type="button"
            class="btn btn-primary sms-submit"
            :disabled="busy || loading || !choice || !product.sms_sales_enabled"
            @click="confirmQuote"
          >
            {{ busy ? $t('確認價格中…') : $t('確認價格') }}
          </button>
        <p v-if="error" role="alert" class="sms-error">{{ uiText(error) }}</p>
        <button
          v-if="pending?.order_no"
          class="btn secondary"
          :disabled="busy"
          @click="cancelUnpaid"
        > {{ $t('取消未付款訂單後重選') }} </button>
        <p v-if="notice" role="status" class="sms-message">{{ notice }}</p>
      </section>
      <section class="sms-panel" aria-labelledby="sms-receive-title">
        <div class="sms-section-heading">
          <h2 id="sms-receive-title">{{ $t('接收短信 / 我的號碼') }}</h2>
          <button v-if="loggedIn" type="button" class="btn" @click="loadHistory">{{ $t('刷新') }}</button>
        </div>
        <p v-if="historyError" role="alert" class="sms-error">{{ uiText(historyError) }}</p>
        <div v-if="!orders.length" class="sms-empty sms-receive-empty">
          <span class="sms-message-icon" aria-hidden="true">✉</span>
          <p>{{ $t('購買成功後，號碼與短信將顯示在這裏。') }}</p>
          <p>{{ $t('已購買的號碼可在這裏繼續查看。') }}</p>
        </div>
        <article v-for="o in orders" :key="o.supply_order_id" class="sms-session">
          <div class="sms-section-heading">
            <strong>{{ o.offer_name || $t('訂單 {0}', [o.supply_order_id]) }}</strong
            ><span class="sms-state">{{
              titles[o.fulfillment?.sms?.state || ""] || $t('確認結果中')
            }}</span>
          </div>
          <p class="muted">{{ $t('實付') }} {{ formatMoney(o.amount) }} {{ $t('· 訂單') }} {{ o.supply_order_id }}</p>
          <template v-if="o.fulfillment?.sms">
            <div v-if="o.fulfillment.sms.phone_number" class="sms-copy">
              <strong>{{ o.fulfillment.sms.phone_number }}</strong
              ><button type="button" class="btn" @click="copy(o.fulfillment.sms.phone_number)"> {{ $t('複製號碼') }} </button>
            </div>
            <div v-if="o.fulfillment.sms.otp_code" class="sms-copy">
              <strong class="sms-code">{{ o.fulfillment.sms.otp_code }}</strong
              ><button type="button" class="btn" @click="copy(o.fulfillment.sms.otp_code)"> {{ $t('複製驗證碼') }} </button>
            </div>
            <p v-if="o.fulfillment.sms.otp_message" class="sms-text">
              {{ o.fulfillment.sms.otp_message }}
            </p>
            <p v-if="o.fulfillment.sms.expires_at" class="muted"> {{ $t('有效至') }} {{ new Date(o.fulfillment.sms.expires_at).toLocaleString(localeTag) }}
            </p>
            <p v-if="o.fulfillment.sms.message" class="muted">{{ o.fulfillment.sms.message }}</p>
            <p v-if="o.fulfillment.sms.refund_status === 'pending'" class="muted"> {{ $t('正在退還本次實付，請等待餘額確認。') }} </p>
            <p v-if="o.fulfillment.sms.settlement_state === 'refunded'" class="sms-refund"> {{ $t('已退還') }} {{ formatMoney(o.fulfillment.sms.refunded_amount_cents || 0) }}
            </p>
            <p v-else-if="o.fulfillment.sms.settlement_state !== 'paid'" class="muted"> {{ $t('結算狀態：') }}{{ o.fulfillment.sms.settlement_state }}
            </p>
            <div class="sms-actions">
              <button
                v-if="pendingActions[o.supply_order_id]"
                type="button"
                class="btn"
                :disabled="busy"
                @click="act(o, pendingActions[o.supply_order_id].action)"
              > {{ pendingActions[o.supply_order_id].action === "cancel" ? $t('確認取消結果') : $t('確認結束結果') }} </button>
              <template v-else
                ><button
                  v-if="o.fulfillment.sms.can_cancel"
                  type="button"
                  class="btn"
                  :disabled="busy"
                  @click="act(o, 'cancel')"
                > {{ $t('取消接碼') }}</button
                ><button
                  v-if="o.fulfillment.sms.can_finish"
                  type="button"
                  class="btn"
                  :disabled="busy"
                  @click="act(o, 'finish')"
                > {{ $t('結束接碼') }} </button></template
              >
            </div>
          </template>
        </article>
        <div v-if="loggedIn && (orders.length || historyPage > 1)" class="sms-pagination">
          <button
            type="button"
            class="btn"
            :disabled="historyPage <= 1"
            @click="changeHistory(historyPage - 1)"
          > {{ $t('較新號碼') }}</button
          ><span>{{ $t('第') }} {{ historyPage }} {{ $t('頁') }}</span
          ><button
            type="button"
            class="btn"
            :disabled="orders.length < 20"
            @click="changeHistory(historyPage + 1)"
          > {{ $t('較早號碼') }} </button>
        </div>
        <p v-if="copied" role="status" class="muted">{{ copied }}</p>
      </section>
    </div>
    <section v-if="product.description" class="sms-panel sms-description">
      <h2>{{ $t('服務説明') }}</h2>
      <div class="rich-content" v-html="product.description"></div>
    </section>
  </div>
</template>
<style scoped>
.sms-product {
  max-width: 1160px;
  margin: 0 auto;
  padding: 24px 0 48px;
  color: #1e293b;
}
.sms-crumb,
.sms-section-heading,
.sms-copy,
.sms-total,
.sms-pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.sms-crumb {
  justify-content: flex-start;
  font-size: 13px;
  color: #64748b;
  margin-bottom: 20px;
}
.sms-product h1 {
  font-size: 26px;
  font-weight: 650;
  margin: 0 0 10px;
}
.sms-product h2 {
  font-size: 18px;
  margin: 0 0 20px;
}
.sms-layout {
  display: grid;
  grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr);
  gap: 20px;
  margin-top: 24px;
  align-items: start;
}
.sms-panel {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  padding: 24px;
  min-width: 0;
}
.sms-wallet {
  display: flex;
  gap: 8px;
  align-items: center;
  margin: 0 0 20px;
}
.sms-wallet a {
  margin-left: auto;
}
.sms-product fieldset {
  border: 0;
  padding: 0;
  margin: 0;
  min-width: 0;
}
.sms-product fieldset > label {
  display: block;
  font-size: 13px;
  margin: 18px 0 8px;
}
.sms-product select,
.sms-product input[type="search"] {
  width: 100%;
  min-height: 44px;
  border: 1px solid #cbd5e1;
  border-radius: 7px;
  background: #fff;
  color: #1e293b;
  padding: 10px 12px;
  font-size: 14px;
}
.sms-product input:focus-visible,
.sms-product select:focus-visible,
.sms-product button:focus-visible {
  outline: 2px solid var(--zc-primary, #2563eb);
  outline-offset: 2px;
}
.sms-product .btn {
  min-height: 44px;
  display: inline-flex;
  justify-content: center;
  align-items: center;
  padding: 8px 14px;
}
.sms-offers {
  display: grid;
  gap: 8px;
  margin: 20px 0;
}
.sms-offer {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  cursor: pointer;
}
.sms-offer.selected {
  border-color: var(--zc-primary, #2563eb);
  background: var(--zc-primary-soft, #eff6ff);
}
.sms-offer span {
  flex: 1;
  overflow-wrap: anywhere;
}
.sms-offer small {
  display: block;
  color: #64748b;
  margin-top: 4px;
}
.sms-offer strong {
  white-space: nowrap;
}
.sms-submit {
  width: 100%;
  margin-top: 16px;
}
.sms-total {
  border-top: 1px solid #e2e8f0;
  margin-top: 20px;
  padding-top: 18px;
}
.sms-total strong {
  font-size: 22px;
}
.sms-empty {
  color: #64748b;
  font-size: 14px;
  line-height: 1.8;
}
.sms-receive-empty {
  text-align: center;
  padding: 48px 8px;
}
.sms-message-icon {
  display: inline-grid;
  place-items: center;
  width: 60px;
  height: 60px;
  border-radius: 50%;
  background: var(--zc-primary-soft, #eff6ff);
  color: var(--zc-primary, #2563eb);
  font-size: 28px;
  margin-bottom: 16px;
}
.sms-section-heading h2 {
  margin-bottom: 0;
}
.sms-section-heading {
  margin-bottom: 16px;
}
.sms-error {
  color: #b91c1c;
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.sms-message {
  padding: 12px;
  background: #eff6ff;
  border-radius: 8px;
  font-size: 14px;
  line-height: 1.6;
}
.sms-session {
  padding: 18px 0;
  border-top: 1px solid #e2e8f0;
  overflow-wrap: anywhere;
}
.sms-state {
  font-size: 12px;
  color: #64748b;
  flex-shrink: 0;
}
.sms-copy {
  margin: 14px 0;
}
.sms-code {
  font-size: 24px;
  letter-spacing: 2px;
}
.sms-text {
  white-space: pre-wrap;
  background: #f8fafc;
  padding: 12px;
  border-radius: 8px;
}
.sms-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.sms-pagination {
  margin-top: 16px;
  font-size: 13px;
}
.sms-refund {
  color: #15803d;
}
.sms-description {
  margin-top: 20px;
}
.sms-product .muted {
  font-size: 13px;
  color: #64748b;
  line-height: 1.7;
}
button:disabled {
  opacity: 0.55;
  cursor: default;
}
@media (max-width: 760px) {
  .sms-product {
    padding: 16px 0 32px;
  }
  .sms-layout {
    grid-template-columns: 1fr;
    gap: 16px;
  }
  .sms-panel {
    padding: 18px;
  }
  .sms-product h1 {
    font-size: 22px;
  }
  .sms-section-heading {
    flex-wrap: wrap;
  }
  .sms-offer {
    align-items: flex-start;
  }
  .sms-copy strong {
    min-width: 0;
    overflow-wrap: anywhere;
  }
}
</style>
