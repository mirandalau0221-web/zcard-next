<script setup lang="ts">
import { uiText, t as $t, localeTag } from '@/i18n';

import { newRequestId } from "../../../packages/request-id";
import { ref, computed, watch, onMounted, nextTick } from "vue";
import { useRoute } from "vue-router";
import { authState, refreshAuth } from "@/auth";
import { getToken } from "@/api/client";
import {
  listLotteryActivities,
  getLotteryActivity,
  claimLotteryChances,
  drawLottery,
  listMyLotteryDraws,
  getMyLotteryDraw,
  lotteryStates,
  type LotteryActivity,
  type LotteryDraw,
} from "@/api/lottery";
const route = useRoute();
const loading = ref(false),
  drawing = ref(false),
  error = ref(""),
  activities = ref<LotteryActivity[]>([]),
  activity = ref<LotteryActivity | null>(null),
  remaining = ref<number | null>(null),
  records = ref<LotteryDraw[]>([]),
  total = ref(0),
  page = ref(1),
  result = ref<LotteryDraw | null>(null),
  dialog = ref<HTMLDialogElement | null>(null),
  copyText = ref($t("複製內容")),
  pendingKey = ref("");
const mine = computed(() => route.query.tab === "prizes");
const loginURL = computed(() => ({
  path: "/login",
  query: { redirect: route.fullPath },
}));
const safePrizes = computed(() => activity.value?.prizes || []);
const missed = computed(
  () =>
    Math.max(
      0,
      10000 - safePrizes.value.reduce((n, p) => n + (p.probability || 0), 0),
    ) / 100,
);
const time = (n?: number) => (n ? new Date(n * 1000).toLocaleString(localeTag.value) : "—");
const keyName = () =>
  `lottery-pending:${activity.value?.id}:${authState.username}`;
function readPending() {
  try {
    pendingKey.value = sessionStorage.getItem(keyName()) || "";
  } catch {
    pendingKey.value = "";
  }
}
function savePending(key: string) {
  pendingKey.value = key;
  try {
    if (key) sessionStorage.setItem(keyName(), key);
    else sessionStorage.removeItem(keyName());
  } catch {
    /* 當前頁面仍保留請求標識 */
  }
}
let loadSequence = 0;
async function load() {
  const seq = ++loadSequence;
  loading.value = true;
  error.value = "";
  activity.value = null;
  remaining.value = null;
  try {
    if (getToken() && !authState.loaded) await refreshAuth();
    if (mine.value) {
      if (!getToken()) {
        records.value = [];
        total.value = 0;
        return;
      }
      const r = await listMyLotteryDraws(page.value);
      if (seq !== loadSequence) return;
      if (r.error) {
        error.value = r.error;
        return;
      }
      records.value = r.data?.items || [];
      total.value = Number(r.data?.total || 0);
      return;
    }
    if (route.params.id) {
      const id = Number(route.params.id);
      if (!Number.isSafeInteger(id) || id <= 0) {
        error.value = $t("活動不存在");
        return;
      }
      const r = await getLotteryActivity(id);
      if (seq !== loadSequence) return;
      if (r.error) {
        error.value = r.error;
        return;
      }
      activity.value = r.data;
      readPending();
      if (getToken() && activity.value?.status === "live") {
        const chances = await claimLotteryChances(id);
        if (seq !== loadSequence) return;
        if (chances.error) error.value = chances.error;
        else remaining.value = chances.data?.remaining || 0;
      }
    } else {
      const r = await listLotteryActivities();
      if (seq !== loadSequence) return;
      if (r.error) error.value = r.error;
      else activities.value = r.data?.items || [];
    }
  } finally {
    if (seq === loadSequence) loading.value = false;
  }
}
async function showResult(d: LotteryDraw) {
  result.value = d;
  copyText.value = $t("複製內容");
  await nextTick();
  dialog.value?.showModal();
}
async function draw() {
  if (!activity.value || drawing.value) return;
  drawing.value = true;
  error.value = "";
  const id = activity.value.id;
  const currentName = keyName();
  try {
    const key = pendingKey.value || newRequestId().replace(/-/g, "");
    savePending(key);
    const r = await drawLottery(id, key);
    if (r.error || !r.data) {
      error.value =
        (r.error || $t("抽獎結果暫時無法讀取")) +
        $t("；請重試上次抽獎或查看我的獎品。");
      return;
    }
    pendingKey.value = "";
    try {
      sessionStorage.removeItem(currentName);
    } catch {}
    if (activity.value?.id === id) {
      await showResult(r.data);
      const chances = await claimLotteryChances(id);
      if (!chances.error) remaining.value = chances.data?.remaining || 0;
      const current = await getLotteryActivity(id);
      if (current.data) activity.value = current.data;
    }
  } finally {
    drawing.value = false;
  }
}
async function view(no: string) {
  error.value = "";
  const r = await getMyLotteryDraw(no);
  if (r.error) error.value = r.error;
  else if (r.data) await showResult(r.data);
}
async function copy() {
  try {
    await navigator.clipboard.writeText(result.value?.content || "");
    copyText.value = $t("已複製");
  } catch {
    copyText.value = $t("請長按或選中內容複製");
  }
}
function close() {
  dialog.value?.close();
  result.value = null;
}
watch(
  () => route.fullPath,
  () => {
    page.value = 1;
    close();
    void load();
  },
);
onMounted(load);
</script>
<template>
  <div class="lottery-page">
    <div class="lottery-heading">
      <div>
        <p class="lottery-eyebrow">{{ $t('會員活動') }}</p>
        <h1>{{ mine ? $t('我的獎品') : activity?.name || $t('幸運抽獎') }}</h1>
      </div>
      <router-link
        :to="mine ? '/lottery' : '/lottery?tab=prizes'"
        class="lottery-secondary"
        >{{ mine ? $t('查看抽獎活動') : $t('我的獎品') }}</router-link
      >
    </div>
    <p v-if="error" class="lottery-message error" role="alert">{{ uiText(error) }}</p>
    <p v-if="loading" class="lottery-surface" role="status">{{ $t('正在加載…') }}</p>
    <template v-else-if="mine">
      <div v-if="!getToken()" class="lottery-surface">
        <p>{{ $t('登錄後查看你的中獎記錄和領取內容。') }}</p>
        <router-link :to="loginURL" class="lottery-primary"
          >{{ $t('登錄後查看') }}</router-link
        >
      </div>
      <template v-else
        ><div v-if="!records.length" class="lottery-surface"> {{ $t('暫無抽獎記錄，先去看看正在進行的活動吧。') }} </div>
        <article
          v-for="d in records"
          :key="d.draw_no"
          class="lottery-record lottery-surface"
        >
          <div>
            <span class="lottery-badge">{{
              lotteryStates[d.status] || $t('處理中')
            }}</span>
            <h2>{{ d.prize_name || $t('謝謝參與') }}</h2>
            <p>{{ d.activity_name }} · {{ time(d.created_at) }}</p>
            <p class="lottery-no">{{ d.draw_no }}</p>
          </div>
          <button class="lottery-secondary" @click="view(d.draw_no)">
            {{ d.status === "pending" ? $t('查看領取説明') : $t('查看詳情') }}
          </button>
        </article>
        <div v-if="total > 20" class="lottery-pager">
          <button
            :disabled="page <= 1"
            @click="
              page--;
              load();
            "
          > {{ $t('上一頁') }}</button
          ><span>{{ page }} / {{ Math.ceil(total / 20) }}</span
          ><button
            :disabled="page * 20 >= total"
            @click="
              page++;
              load();
            "
          > {{ $t('下一頁') }} </button>
        </div></template
      >
    </template>
    <template v-else-if="activity">
      <div class="lottery-surface lottery-banner">
        <img v-if="activity.image" :src="activity.image" :alt="$t('活動圖片')" />
        <p>{{ time(activity.start_at) }} — {{ time(activity.end_at) }}</p>
        <p>{{ lotteryStates[activity.status] || $t('活動未開放') }}</p>
        <p v-if="activity.description" class="lottery-description">
          {{ activity.description }}
        </p>
      </div>
      <section class="lottery-surface">
        <h2>{{ $t('活動獎品') }}</h2>
        <div class="lottery-prizes">
          <article v-for="p in safePrizes" :key="p.id" class="lottery-prize">
            <img v-if="p.image" :src="p.image" alt="" /><span
              v-else
              class="lottery-gift"
              aria-hidden="true"
              >{{ $t('禮') }}</span
            >
            <h3>{{ p.name }}</h3>
            <p>{{ $t('中獎概率') }} {{ ((p.probability || 0) / 100).toFixed(2) }}%</p>
            <small
              >{{ p.mode === "manual" ? $t('平台人工領取') : $t('系統自動發放') }} {{ $t('· 活動剩餘') }} {{ p.remaining || 0 }} {{ $t('份') }}</small
            >
          </article>
        </div>
        <p class="lottery-muted"> {{ $t('謝謝參與概率') }} {{ missed.toFixed(2) }}{{ $t('%。每次獨立抽獎，可重複中獎。') }} </p>
      </section>
      <section class="lottery-surface lottery-action" aria-live="polite">
        <template v-if="!getToken()"
          ><p>{{ $t('登錄後參與，次數按賬號記錄。') }}</p>
          <router-link :to="loginURL" class="lottery-primary"
            >{{ $t('登錄後參與') }}</router-link
          ></template
        ><template v-else
          ><p v-if="remaining !== null"> {{ $t('剩餘') }} <strong>{{ remaining }}</strong> {{ $t('次') }} </p>
          <p>
            {{
              activity.chance_mode === "daily"
                ? $t('每日贈送 {0} 次，當日有效（{1}）。', [activity.chance_count, activity.timezone])
                : activity.chance_mode === "manual"
                  ? $t('本活動由平台發放抽獎次數。')
                  : $t('活動期間每個賬號贈送 {0} 次。', [activity.chance_count])
            }}
          </p>
          <button
            class="lottery-primary"
            :disabled="
              drawing ||
              (!pendingKey && (activity.status !== 'live' || !remaining))
            "
            @click="draw"
          >
            {{
              drawing
                ? $t('正在確認結果…')
                : pendingKey
                  ? $t('重試上次抽獎')
                  : activity.status !== "live"
                    ? lotteryStates[activity.status] || $t('暫不可抽獎')
                    : remaining === 0
                      ? $t('抽獎次數已用完')
                      : $t('立即抽獎')
            }}
          </button>
          <p class="lottery-muted"> {{ $t('獎品不足時暫停抽獎；未生成結果不扣次數。') }} </p></template
        >
      </section>
      <router-link to="/lottery" class="lottery-secondary"
        >{{ $t('返回活動列表') }}</router-link
      >
    </template>
    <template v-else-if="!error"
      ><div v-if="!activities.length" class="lottery-surface"> {{ $t('暫時沒有開放的抽獎活動，已有獎品仍可在“我的獎品”中查看。') }} </div>
      <div class="lottery-activities">
        <router-link
          v-for="a in activities"
          :key="a.id"
          :to="`/lottery/${a.id}`"
          class="lottery-surface lottery-activity-card"
          ><img v-if="a.image" :src="a.image" alt="" /><span
            class="lottery-badge"
            >{{ lotteryStates[a.status] || $t('活動未開放') }}</span
          >
          <h2>{{ a.name }}</h2>
          <p>
            {{
              a.chance_mode === "daily"
                ? $t('每日贈送')
                : a.chance_mode === "manual"
                  ? $t('平台發放次數')
                  : $t('登錄參與贈送')
            }}{{ a.chance_mode === "manual" ? "" : $t('{0} 次', [a.chance_count]) }}
          </p>
          <span>{{ $t('查看活動 →') }}</span></router-link
        >
      </div></template
    >
    <dialog ref="dialog" class="lottery-result" @cancel="result = null">
      <template v-if="result"
        ><div class="lottery-result-heading">
          <h2>{{ result.status === "missed" ? $t('謝謝參與') : $t('恭喜中獎') }}</h2>
          <button :aria-label="$t('關閉中獎詳情')" @click="close">×</button>
        </div>
        <h3>{{ result.prize_name }}</h3>
        <p>{{ lotteryStates[result.status] }}</p>
        <p class="lottery-no">{{ $t('中獎編號：') }}{{ result.draw_no }}</p>
        <p v-if="result.status === 'pending'"> {{ $t('請按以下説明聯繫平台領取，提供中獎編號即可核對。') }} </p>
        <pre v-if="result.content" class="lottery-content">{{
          result.content
        }}</pre>
        <button v-if="result.content" class="lottery-secondary" @click="copy">
          {{ uiText(copyText) }}
        </button>
        <p class="lottery-muted">{{ $t('記錄已保存，可隨時從“我的獎品”查看。') }}</p>
        <button class="lottery-primary" @click="close"> {{ $t('我知道了') }} </button></template
      >
    </dialog>
  </div>
</template>
<style scoped>
.lottery-page {
  max-width: var(--zc-content-width, 1240px);
  margin: auto;
  padding: 24px 16px 32px;
  color: #253047;
}
.lottery-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 24px;
}
.lottery-heading h1 {
  font-size: 28px;
  margin: 4px 0;
}
.lottery-eyebrow {
  color: var(--zc-primary);
  font-size: 13px;
  margin: 0;
}
.lottery-surface {
  background: #fff;
  border: 1px solid #e4e9f0;
  border-radius: var(--zc-card-radius, 14px);
  padding: 24px;
  margin-bottom: 18px;
}
.lottery-surface h2 {
  font-size: 19px;
  margin: 0 0 14px;
}
.lottery-banner > img {
  width: 100%;
  max-height: 260px;
  object-fit: cover;
  border-radius: 10px;
}
.lottery-description {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.lottery-prizes {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
}
.lottery-prize {
  border: 1px solid #e8eaf2;
  border-radius: 12px;
  padding: 20px 14px;
  text-align: center;
  background: #fafcff;
  overflow-wrap: anywhere;
}
.lottery-prize img {
  height: 72px;
  width: 72px;
  object-fit: contain;
  margin: auto;
}
.lottery-prize h3 {
  font-size: 16px;
  margin: 12px 0 8px;
}
.lottery-prize p {
  font-size: 14px;
}
.lottery-prize small,
.lottery-muted {
  color: #5c687a;
  font-size: 13px;
  line-height: 1.6;
}
.lottery-gift {
  display: inline-grid;
  place-items: center;
  width: 64px;
  height: 64px;
  background: #edf3ff;
  border-radius: 18px;
  font-size: 26px;
  color: var(--zc-primary);
}
.lottery-primary,
.lottery-secondary {
  display: inline-flex;
  justify-content: center;
  align-items: center;
  min-height: 44px;
  border-radius: 9px;
  padding: 10px 18px;
  font: inherit;
  text-decoration: none;
  cursor: pointer;
  box-sizing: border-box;
}
.lottery-primary {
  background: var(--zc-primary);
  border: 1px solid transparent;
  color: #fff;
}
.lottery-secondary {
  background: #fff;
  border: 1px solid #d6deeb;
  color: #334155;
}
.lottery-primary:disabled {
  background: #cbd5e1;
  color: #475569;
  cursor: not-allowed;
}
.lottery-action {
  text-align: center;
}
.lottery-action strong {
  font-size: 24px;
  color: var(--zc-primary);
}
.lottery-action .lottery-primary {
  min-width: 220px;
}
.lottery-activities {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.lottery-activity-card {
  color: inherit;
  text-decoration: none;
}
.lottery-activity-card > img {
  width: 100%;
  height: 150px;
  object-fit: cover;
  border-radius: 8px;
  margin-bottom: 16px;
}
.lottery-badge {
  display: inline-block;
  color: #3659b2;
  background: #eff4ff;
  padding: 4px 9px;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 10px;
}
.lottery-record {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.lottery-record p {
  font-size: 14px;
  color: #5c687a;
}
.lottery-no {
  word-break: break-all;
  font-size: 13px;
}
.lottery-message {
  padding: 16px;
  border-radius: 10px;
  background: #fff3e8;
  color: #8c4214;
}
.lottery-result {
  margin: auto;
  width: min(520px, calc(100vw - 32px));
  box-sizing: border-box;
  border: 0;
  border-radius: 16px;
  padding: 24px;
  max-height: 85vh;
  overflow: auto;
  color: #253047;
}
.lottery-result::backdrop {
  background: #15223688;
}
.lottery-result-heading {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.lottery-result-heading button {
  border: 0;
  background: transparent;
  min-width: 44px;
  min-height: 44px;
  font-size: 28px;
  cursor: pointer;
}
.lottery-content {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  background: #f4f7fb;
  padding: 16px;
  border-radius: 10px;
  font-family: inherit;
  user-select: text;
}
.lottery-result .lottery-primary {
  display: flex;
  width: 100%;
  margin-top: 18px;
}
.lottery-pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
}
.lottery-pager button {
  min-height: 44px;
  padding: 8px 16px;
}
@media (max-width: 640px) {
  .lottery-page {
    padding: 18px 12px;
  }
  .lottery-heading h1 {
    font-size: 23px;
  }
  .lottery-surface {
    padding: 18px;
  }
  .lottery-prizes {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  .lottery-prize {
    padding: 16px 10px;
  }
  .lottery-activities {
    grid-template-columns: 1fr;
  }
  .lottery-record {
    align-items: flex-start;
    flex-direction: column;
  }
  .lottery-action .lottery-primary {
    width: 100%;
  }
  .lottery-heading {
    gap: 8px;
  }
}
</style>
