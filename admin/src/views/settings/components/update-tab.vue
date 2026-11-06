<script setup lang="ts">
// 在線更新 tab（doc/在線更新方案.md §10）：狀態卡 + 源配置 + changelog 彈窗 +
// 全流程進度 + 重啓等待自動刷新 + 回滾。遵循大廠交互：枚舉單選直出/緊湊卡片/膠囊狀態。
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NInput,
  NModal,
  NPopconfirm,
  NProgress,
  NRadioButton,
  NRadioGroup,
  NSpace,
  NTag,
  useMessage,
  useDialog
} from "naive-ui";
import { marked } from "marked";
import {
  applyUpdate,
  checkUpdate,
  fetchUpdateSourceConfig,
  fetchUpdateStatus,
  rollbackUpdate,
  updateUpdateSourceConfig,
  type UpdateCheckResult,
  type UpdateSourceConfig,
  type UpdateStatus
} from "@/service/api";

import { isUpdatePending, isUpdateComplete } from "./update-state";
import { isRestartConnectionError, restartWaitExpired } from "@/service/request/update-restart";

defineOptions({ name: "UpdateTab" });

const message = useMessage();
const dialog = useDialog();

// ── 狀態輪詢 ──
const status = ref<UpdateStatus | null>(null);
const checking = ref(false);
let pollTimer: ReturnType<typeof setTimeout> | null = null;
let waitingRestart = false; // restarting 後進入「等待服務恢復」模式
let waitStart = 0;
let disposed = false;
const connectionWaiting = ref(false);
const pollingStopped = ref(false);
let reloadTimer: ReturnType<typeof setTimeout> | null = null;

const PHASE_TEXT: Record<string, string> = {
  idle: "空閒",
  checking: "檢查更新中",
  backing_up: "備份數據庫中",
  downloading: "下載新版本中",
  applying: "應用更新中",
  restarting: "重啓服務中",
  verifying: "新版本健康檢查中",
  rolled_back: "已回滾上一版本",
  failed: "更新失敗"
};

const phaseText = computed(() => PHASE_TEXT[status.value?.phase || "idle"] || status.value?.phase);
const inFlight = computed(() => {
  const p = status.value?.phase;
  return !!p && ["checking", "backing_up", "downloading", "applying", "restarting", "verifying"].includes(p);
});

const isContainer = computed(() => status.value?.supervisor_kind === "docker");
const unknownContainerVersion = computed(() => isContainer.value && !/^v?\d+\.\d+\.\d+$/.test(status.value?.current_version || ""));
const containerHint = computed(() => status.value?.docker_update_hint || "請在原部署目錄運行 bash deploy/docker-install.sh --online，接入在線更新助手。");
const canApply = computed(() => !isContainer.value || status.value?.docker_update_ready === true);
const canRollback = computed(() => !isContainer.value || (canApply.value && status.value?.rollback_ready === true));

const supervisorTag = computed(() => {
  if (!status.value) return { type: "default" as const, label: "狀態未知" };
  const k = status.value.supervisor_kind;
  if (k === "docker") return { type: "info" as const, label: "Docker / 容器部署" };
  if (k === "none") return { type: "warning" as const, label: "裸進程運行（無管理器）" };
  if (k === "systemd") return { type: "success" as const, label: "systemd" };
  if (k === "supervisord" || k === "pm2") return { type: "success" as const, label: k };
  return { type: "info" as const, label: k || "未知" };
});

// 狀態接口失敗可見化（此前靜默 catch——401/403/網絡異常時頁面全 fallback，
// 與「數據為空」無法區分，誤導排查）
const statusError = ref("");
async function refreshStatusVisible() {
  try {
    const { data, error } = await fetchUpdateStatus(() => waitingRestart && !pollingStopped.value && !disposed);
    if (disposed) return;
    if (error || !data) throw error || new Error("狀態響應為空");
    status.value = data as any;
    statusError.value = "";
  } catch (e: any) {
    if (disposed) return;
    if (waitingRestart && isRestartConnectionError(e)) {
      connectionWaiting.value = true;
      if (!waitStart) waitStart = Date.now();
      if (!stopWaitingIfExpired()) schedulePoll();
      return;
    }
    statusError.value = e?.response?.status ? `HTTP ${e.response.status}` : String(e?.message || e);
  }
}

// 歷史版本 changelog（manifest 權威源;排除已安裝版本——只展示比當前新的記錄未安裝前的全歷史）
const historyList = computed(() => status.value?.history || []);
const historyHtml = computed(() => {
  const out: Record<string, string> = {};
  for (const h of historyList.value) out[h.version] = sanitizeHtml(marked.parse(h.notes || "_（無説明）_") as string);
  return out;
});

// 下載百分比（統一出口——僅 downloading 階段有值；百分比由進度條 indicator 單點展示，
// 階段標題只顯示階段名，避免「下載新版本中 23% + 23%」雙顯）
const dlPercent = computed(() =>
  !isContainer.value && status.value?.phase === "downloading" ? status.value?.progress_percent || 0 : undefined
);

const sourceText = computed(() => {
  const s = status.value?.source || "";
  if (!s) return "—";
  if (s === "github") return "GitHub 直連";
  if (s.startsWith("static:")) return `自建源 ${s.slice(7)}`;
  return `加速鏡像 ${s}`;
});

// ── 狀態拉取（waitingRestart 模式：連接失敗=仍在重啓繼續等；恢復且版本到位=成功）──
function stopWaitingIfExpired() {
  if (!waitingRestart || !restartWaitExpired(waitStart, Date.now())) return false;
  waitingRestart = false;
  connectionWaiting.value = false;
  pollingStopped.value = true;
  statusError.value = "等待服務恢復已超過 5 分鐘，請檢查服務狀態後點擊重新連接。";
  message.error(statusError.value);
  return true;
}

async function refreshStatus() {
  if (disposed || pollingStopped.value || updateDone.value || stopWaitingIfExpired()) return;
  try {
    // 回調在錯誤發生時讀取最新狀態，覆蓋輪詢發出後才進入重啓階段的情況。
    const { data, error } = await fetchUpdateStatus(() => waitingRestart && !pollingStopped.value && !disposed);
    if (disposed || pollingStopped.value || updateDone.value) return;
    if (error || !data) throw error || new Error("狀態響應為空");
    status.value = data as any;
    statusError.value = "";
    connectionWaiting.value = false;
    enterWaitIfNeeded(status.value);
    if (status.value?.phase === "failed" || status.value?.phase === "rolled_back") {
      waitingRestart = false;
      waitStart = 0;
    }
    if (waitingRestart) {
      const st = status.value!;
      if (isUpdateComplete(st)) {
        waitingRestart = false;
        waitStart = 0;
        updateDone.value = true;
        const from = st.prev_version || "舊版本";
        message.success(`更新完成：${from} → ${st.current_version}`);
        reloadTimer = setTimeout(() => window.location.reload(), 3000);
        return;
      }
      // 下載/備份可能超過五分鐘；只計算重啓檢查或連續失聯的等待時間。
      if (["restarting", "verifying"].includes(st.phase)) {
        if (!waitStart) waitStart = Date.now();
      } else waitStart = 0;
      if (stopWaitingIfExpired()) return;
    }
  } catch (e: any) {
    if (disposed || pollingStopped.value || updateDone.value) return;
    if (waitingRestart && isRestartConnectionError(e)) {
      if (!waitStart) waitStart = Date.now();
      connectionWaiting.value = true;
      if (stopWaitingIfExpired()) return;
    } else {
      statusError.value = e?.response?.status ? `HTTP ${e.response.status}` : String(e?.message || e);
      // 權限、業務或程序錯誤不偽裝成重啓，也不連續彈錯。
      waitingRestart = false;
      connectionWaiting.value = false;
      pollingStopped.value = true;
      return;
    }
  }
  schedulePoll();
}

function schedulePoll() {
  if (disposed || pollingStopped.value || updateDone.value) return;
  if (pollTimer) clearTimeout(pollTimer);
  const interval = waitingRestart ? 2000 : inFlight.value ? 1500 : 8000;
  pollTimer = setTimeout(refreshStatus, interval);
}

async function reconnectStatus() {
  pollingStopped.value = false;
  statusError.value = "";
  waitStart = 0;
  // 重新連接只查詢狀態，不重複提交更新或回滾操作。
  enterWaitIfNeeded(status.value);
  await refreshStatus();
}

// ── 檢查更新 ──
const checkResult = ref<UpdateCheckResult | null>(null);
const showConfirm = ref(false);
const changelogHtml = ref("");
// 彈窗多態：confirm=版本確認（取消/立即更新）；progress=分步進度；由 phase 自動切換
const modalStage = ref<"confirm" | "progress">("confirm");
// 完成態：四步全綠 + 「vA → vB」版本對展示（停留後自動刷新）
const updateDone = ref(false);
const autoChecked = ref(false); // 進 tab 自動檢查只做一次（重進頁面再觸發）

async function doCheck(silent = false) {
  if (checking.value || waitingRestart || updateDone.value || isUpdatePending(status.value)) return;
  checking.value = true;
  try {
    const { data, error } = await checkUpdate();
    if (error || !data) {
      await refreshStatusVisible();
      enterWaitIfNeeded(status.value);
      return;
    }
    // Refresh after Check so a polled checking phase is not mistaken for an update.
    await refreshStatusVisible();
    enterWaitIfNeeded(status.value);
    // A response started before an update must not reopen the confirmation.
    if (statusError.value || waitingRestart || updateDone.value || isUpdatePending(status.value)) return;
    checkResult.value = data;
    changelogHtml.value = sanitizeHtml(marked.parse(data.notes || "_（本版本未提供變更記錄）_") as string);
    if (data.has_update) {
      modalStage.value = "confirm";
      showConfirm.value = true; // 有新版：直接彈更新框（自動檢查與手動一致）
    } else if (!silent && unknownContainerVersion.value) {
      message.warning("當前鏡像未記錄正式版本，無法判斷是否最新。請重新構建鏡像。");
    } else if (!silent) {
      message.success(`已是最新版本 ${data.current_version}`);
    }
  } catch (e: any) {
    if (!silent) message.error(`檢查更新失敗：${e?.message || e}`);
  } finally {
    checking.value = false;
  }
}

// changelog 出自服務端驗籤 manifest（信任域內），仍剝腳本/事件屬性兜底
function sanitizeHtml(html: string) {
  return html
    .replace(/<script[\s\S]*?<\/script>/gi, "")
    .replace(/\son\w+\s*=\s*"[^"]*"/gi, "")
    .replace(/\son\w+\s*=\s*'[^']*'/gi, "")
    .replace(/javascript:/gi, "");
}

async function doApply() {
  if (!canApply.value) { message.info(containerHint.value); return; }
  updateDone.value = false;
  modalStage.value = "progress"; // 彈窗切換為分步進度（大廠範式：同一彈窗承接全流程）
  try {
    const { data, error } = await applyUpdate(checkResult.value?.latest_version);
    if (error || !data) throw error || new Error("更新響應為空");
    status.value = data;
    waitingRestart = false;
    pollingStopped.value = false;
    statusError.value = "";
    enterWaitIfNeeded(status.value);
    if (pollTimer) clearTimeout(pollTimer);
    refreshStatus();
  } catch (e: any) {
    showConfirm.value = false;
    message.error(`觸發更新失敗：${e?.message || e}`);
  }
}

// ── 分步進度（大廠更新器範式）：四步 + 當前步驟高亮 + 下載步百分比 ──
const STEPS = computed(() => [
  ...(isContainer.value
    ? [{ key: "downloading", label: "拉取鏡像" }, { key: "backing_up", label: "停機備份" }]
    : [{ key: "backing_up", label: "備份數據庫" }, { key: "downloading", label: "下載新版本" }]),
  { key: "applying", label: "應用更新" },
  { key: "restarting", label: "重啓服務" },
]);
const stepIndex = computed(() => {
  const ph = status.value?.phase || "";
  // verifying（新進程健康檢查）歸入重啓步；failed 停在當前步標紅
  const order = [...STEPS.value.map(s => s.key), "verifying"];
  const idx = order.indexOf(ph);
  return idx < 0 ? -1 : Math.min(idx, STEPS.value.length - 1);
});
const stepState = (i: number): "done" | "current" | "todo" | "error" => {
  if (updateDone.value) return "done";
  const ph = status.value?.phase;
  if (ph === "failed") return i === Math.max(stepIndex.value, 0) ? "error" : i < stepIndex.value ? "done" : "todo";
  if (i < stepIndex.value) return "done";
  if (i === stepIndex.value) return "current";
  return "todo";
};

/** 失敗重試：回到確認階段重新發起 */
async function retryFromFailed() {
  modalStage.value = "progress";
  try {
    const { data, error } = await applyUpdate(checkResult.value?.latest_version || status.value?.target_version);
    if (error || !data) throw error || new Error("更新響應為空");
    status.value = data;
    waitingRestart = false;
    pollingStopped.value = false;
    statusError.value = "";
    enterWaitIfNeeded(status.value);
    if (pollTimer) clearTimeout(pollTimer);
    refreshStatus();
  } catch (e: any) {
    showConfirm.value = false;
    message.error(`觸發更新失敗：${e?.message || e}`);
  }
}

async function doRollback() {
  if (!canRollback.value) { message.info("沒有數據庫結構兼容的上一版本可回退"); return; }
  try {
    const { data, error } = await rollbackUpdate();
    if (error || !data) throw error || new Error("回滾響應為空");
    status.value = data;
    waitingRestart = true;
    pollingStopped.value = false;
    statusError.value = "";
    waitStart = Date.now();
    message.info("已回滾，等待服務重啓…");
    if (pollTimer) clearTimeout(pollTimer);
    refreshStatus();
  } catch (e: any) {
    message.error(`回滾失敗：${e?.message || e}`);
  }
}

// ── 源配置 ──
const config = ref<UpdateSourceConfig | null>(null);
const accelsText = ref("");
const savingConfig = ref(false);

async function loadConfig() {
  config.value = await fetchUpdateSourceConfig();
  accelsText.value = (config.value.accelerators || []).join("\n");
}

async function saveConfig() {
  if (!config.value) return;
  const cfg: UpdateSourceConfig = {
    ...config.value,
    accelerators: accelsText.value
      .split("\n")
      .map(s => s.trim())
      .filter(Boolean)
  };
  if (cfg.mode === "static" && !cfg.static_base) {
    message.error("靜態源模式必須填寫基址（如 https://cdn.example.com/zcard）");
    return;
  }
  if (cfg.channel === "beta" && cfg.mode === "accel") {
    message.error("加速鏡像不支持 beta 通道（僅 GitHub 直連/自建源）");
    return;
  }
  savingConfig.value = true;
  try {
    await updateUpdateSourceConfig(cfg);
    config.value = cfg;
    message.success("更新源配置已保存");
  } catch (e: any) {
    message.error(`保存失敗：${e?.message || e}`);
  } finally {
    savingConfig.value = false;
  }
}

// 進入「等待恢復」模式的判據（頁面刷新/重開也能接上——等待態從服務端推導,
// 不依賴內存 flag）：
//  a) phase 處於進行中任一階段（含新進程健康檢查 verifying）
//  b) 目標版本已登記且尚未到位（重啓間隙/刷新後 update.state 延續 target）
// 進入即重開分步進度彈窗——用户刷新頁面回來直接看到更新進度而非乾等。
function enterWaitIfNeeded(st: UpdateStatus | null) {
  if (!st || waitingRestart || updateDone.value || pollingStopped.value || disposed) return;
  if (isUpdatePending(st) && (st.busy || st.phase !== "checking")) {
    waitingRestart = true;
    pollingStopped.value = false;
    statusError.value = "";
    waitStart = ["restarting", "verifying"].includes(st.phase) ? Date.now() : 0;
    modalStage.value = "progress";
    showConfirm.value = true;
  }
}

onMounted(async () => {
  await loadConfig().catch(() => {});
  if (disposed) return;
  await refreshStatusVisible();
  if (disposed) return;
  if (!statusError.value) schedulePoll();
  enterWaitIfNeeded(status.value);
  // 進入 tab 自動檢查（靜默失敗不打擾）；有新版本直接彈更新框。
  // 雙保險：inFlight 之外,目標版本在途（等待恢復模式/刷新重開場景）同樣不查——
  // 更新中觸發 Check 會打斷進度展示（另一標籤頁自動檢查即觸發,線上實錄）
  if (!autoChecked.value && !statusError.value && !waitingRestart && !isUpdatePending(status.value)) {
    autoChecked.value = true;
    doCheck(true);
  }
});
onBeforeUnmount(() => {
  disposed = true;
  if (pollTimer) clearTimeout(pollTimer);
  if (reloadTimer) clearTimeout(reloadTimer);
});

// status 變化時捕捉 restarting
watch(
  () => status.value?.phase,
  ph => {
    if (ph === "restarting") enterWaitIfNeeded(status.value);
  }
);
</script>

<template>
  <div class="update-tab">
    <NCard size="small" title="系統更新" class="mb-3">
      <template #header-extra>
        <NSpace :size="8" align="center">
          <NTag :type="supervisorTag.type" size="small" round>{{ supervisorTag.label }}</NTag>
          <NTag size="small" round :bordered="false">源：{{ sourceText }}</NTag>
        </NSpace>
      </template>

      <div class="stat-grid">
        <div class="stat">
          <div class="stat-label">當前版本</div>
          <div class="stat-value">{{ status?.current_version || "—" }}</div>
        </div>
        <div class="stat">
          <div class="stat-label">最新版本</div>
          <div class="stat-value">
            {{ status?.latest_version || "—" }}
            <NTag v-if="status?.has_update" type="success" size="small" round class="ml-1">有新版</NTag>
          </div>
        </div>
        <div class="stat">
          <div class="stat-label">狀態</div>
          <div class="stat-value">
            <NTag :type="status?.phase === 'failed' ? 'error' : inFlight ? 'info' : 'default'" size="small" round>
              {{ phaseText }}
            </NTag>
          </div>
        </div>
        <div class="stat">
          <div class="stat-label">操作</div>
          <div class="stat-value">
            <NSpace :size="8">
              <NButton size="tiny" type="primary" :loading="checking" :disabled="inFlight || waitingRestart" @click="doCheck()">檢查更新</NButton>
              <NPopconfirm v-if="canRollback" @positive-click="doRollback">
                <template #trigger>
                  <NButton size="tiny" :disabled="inFlight" quaternary type="warning">回滾上一版</NButton>
                </template>
                {{ isContainer ? "備份後切回上一鏡像並重啓服務？數據庫不會還原。" : "回滾到 zcard.prev 並重啓服務？" }}
              </NPopconfirm>
            </NSpace>
          </div>
        </div>
      </div>

      <NAlert v-if="!isContainer && status && status.backup_ready === false" type="warning" class="mb-3" :bordered="false">
        <b>備份工具未就緒</b>
        <div class="mt-1">{{ status.backup_hint }}</div>
        <div class="mt-1 text-xs opacity-70">更新前會強制備份數據庫（數據安全優先，不可跳過）；裝好工具後本提示自動消失。</div>
      </NAlert>

      <NAlert v-if="statusError" type="error" class="mb-3" :bordered="false">
        <b>狀態獲取失敗（{{ statusError }}）</b>
        <NButton size="small" class="ml-2" @click="reconnectStatus">重新連接</NButton>
        <div class="mt-1 text-xs opacity-70">
          system:update 為超管專屬權限——請確認當前賬號為超級管理員；HTTP 401/403=權限或登錄態，
          5xx=服務異常，網絡錯誤=反代/服務未起。F12 → Network → update/status 可看原始響應。
        </div>
      </NAlert>

      <NAlert v-if="isContainer" type="info" :show-icon="true" class="mt-3" :bordered="false">
        當前為 Docker / 容器部署，升級和回退均通過鏡像完成。{{ containerHint }}
        <div v-if="unknownContainerVersion" class="mt-1">當前鏡像未記錄正式版本，無法判斷是否最新；重新構建後將顯示實際安裝的版本號。</div>
      </NAlert>

      <NAlert v-if="status?.supervisor_kind === 'none'" type="warning" :show-icon="true" class="mt-3" :bordered="false">
        當前為裸進程（nohup）運行：更新可正常執行，但新版本無法啓動時<b>沒有自動回滾兜底</b>。建議改用
        systemd（install.sh）或寶塔「進程守護管理器」部署後使用在線更新。
      </NAlert>

      <NAlert v-if="status?.phase === 'failed'" type="error" class="mt-3" :bordered="false">
        <b>更新失敗：</b>{{ status?.error_message }}
        <div class="mt-1 text-xs opacity-70">請按錯誤信息檢查當前運行版本和備份。數據庫已遷移或狀態不明時，請先核對恢復方案，再重試或回退。</div>
      </NAlert>

      <!-- 進行中進度 -->
      <div v-if="inFlight" class="progress-block mt-3">
        <div class="progress-title">{{ phaseText }}</div>
        <NProgress
          type="line"
          :percentage="dlPercent"
          :indeterminate="dlPercent === undefined"
          :rail-height="10"
          :border-radius="5"
          :show-indicator="dlPercent !== undefined"
          processing
        />
        <div v-if="!pollingStopped && (connectionWaiting || status?.phase === 'restarting' || status?.phase === 'verifying')" class="mt-2 text-xs opacity-70">
          服務正在重啓，請稍候。正在等待連接恢復，確認新版本就緒後本頁會自動刷新。
        </div>
        <div v-if="status?.phase === 'backing_up'" class="mt-2 text-xs opacity-70">
          {{ isContainer ? "已暫停應用，正在備份數據卷及數據庫；完成後啓動新版本。" : "正在備份數據庫；備份失敗將中止更新。" }}
        </div>
      </div>
    </NCard>

    <NCard v-if="historyList.length" size="small" title="版本歷史" class="mb-3">
      <div class="history-list">
        <div v-for="h in historyList" :key="h.version" class="history-item">
          <div class="history-head">
            <NTag size="small" round :type="h.version === status?.current_version ? 'success' : 'default'">
              {{ h.version }}{{ h.version === status?.current_version ? "（當前）" : "" }}
            </NTag>
            <span class="history-date">{{ h.issued_at }}</span>
            <NTag v-if="h.channel === 'beta'" size="small" round type="warning" :bordered="false">beta</NTag>
          </div>
          <div class="history-notes" v-html="historyHtml[h.version]" />
        </div>
      </div>
    </NCard>

    <NCollapse class="mb-3">
      <NCollapseItem name="source" title="更新源配置（大陸服務器默認 auto 即可）">
        <div v-if="config" class="config-form">
          <div class="field">
            <div class="field-label">源模式</div>
            <NRadioGroup v-model:value="config.mode" size="small">
              <NRadioButton value="auto" title="直連探測 GitHub，不通自動走加速（大陸推薦）">auto 自動</NRadioButton>
              <NRadioButton value="github">GitHub 直連</NRadioButton>
              <NRadioButton value="accel">加速鏡像</NRadioButton>
              <NRadioButton value="static">自建靜態源</NRadioButton>
            </NRadioGroup>
          </div>
          <div v-if="config.mode === 'github' || config.mode === 'accel'" class="field">
            <div class="field-label">發行倉庫</div>
            <NInput v-model:value="config.repo" size="small" placeholder="owner/repo" style="max-width: 360px" />
          </div>
          <div v-if="config.mode === 'accel' || config.mode === 'auto'" class="field">
            <div class="field-label">加速鏡像列表（一行一個；auto 模式直連不通時按序競速）</div>
            <NInput
              v-model:value="accelsText"
              type="textarea"
              :rows="3"
              size="small"
              style="max-width: 480px"
              placeholder="https://gh-proxy.com&#10;https://ghfast.top&#10;https://ghproxy.net"
            />
          </div>
          <div v-if="config.mode === 'static'" class="field">
            <div class="field-label">靜態源基址</div>
            <NInput
              v-model:value="config.static_base"
              size="small"
              style="max-width: 480px"
              placeholder="https://cdn.example.com/zcard（其下平鋪 update.json 與二進制產物）"
            />
          </div>
          <div v-if="!isContainer" class="field">
            <div class="field-label">
              進程管理器
              <span class="text-xs opacity-55">（自動探測不準時手動指定——決定更新重啓的分流方式）</span>
            </div>
            <NRadioGroup v-model:value="config.supervisor" size="small">
              <NRadioButton value="auto" title="父進程鏈/環境變量自動探測">auto 探測</NRadioButton>
              <NRadioButton value="systemd">systemd</NRadioButton>
              <NRadioButton value="supervisord">supervisord</NRadioButton>
              <NRadioButton value="none" title="nohup 裸跑：更新失敗無自動兜底">裸跑</NRadioButton>
            </NRadioGroup>
          </div>
          <div class="field">
            <div class="field-label">更新通道</div>
            <NRadioGroup v-model:value="config.channel" size="small">
              <NRadioButton value="stable">stable 穩定</NRadioButton>
              <NRadioButton value="beta" :disabled="config.mode === 'accel'">beta 嚐鮮</NRadioButton>
            </NRadioGroup>
            <span v-if="config.mode === 'accel'" class="text-xs opacity-60 ml-2">加速鏡像不支持 beta（走 GitHub API）</span>
          </div>
          <div class="mt-2">
            <NButton size="small" type="primary" :loading="savingConfig" @click="saveConfig">保存配置</NButton>
          </div>
        </div>
      </NCollapseItem>
    </NCollapse>

    <!-- 更新彈窗（多態）：確認（取消/立即更新）→ 分步進度（同一彈窗承接全流程） -->
    <NModal
      :show="showConfirm"
      preset="card"
      style="width: min(640px, 94vw)"
      :title="modalStage === 'confirm' ? '發現新版本' : updateDone ? '更新完成' : '正在更新'"
      :mask-closable="false"
      :close-on-esc="false"
      @update:show="(v: boolean) => (showConfirm = v)"
    >
      <!-- 階段一：版本確認 -->
      <template v-if="modalStage === 'confirm'">
        <div class="version-line">
          <NTag size="small" round>{{ checkResult?.current_version }}</NTag>
          <span class="arrow">→</span>
          <NTag size="small" round type="success">{{ checkResult?.latest_version }}</NTag>
          <NTag size="small" round :bordered="false" class="ml-2">{{ checkResult?.channel }} · {{ checkResult?.source || sourceText }}</NTag>
        </div>
        <NAlert v-if="isContainer" type="info" class="my-3">{{ containerHint }}</NAlert>
        <div class="changelog" v-html="changelogHtml" />
      </template>

      <!-- 階段二：分步進度（含完成態橫幅） -->
      <template v-else>
        <div v-if="updateDone" class="done-banner">
          <div class="done-title">✓ 更新完成</div>
          <div class="done-vers">
            <NTag size="small" round>{{ status?.prev_version || "舊版本" }}</NTag>
            <span class="arrow">→</span>
            <NTag size="small" round type="success">{{ status?.current_version }}</NTag>
          </div>
          <div class="done-hint">頁面將在 3 秒後自動刷新…</div>
        </div>
        <div class="steps" :class="{ 'steps-done': updateDone }">
          <div v-for="(s, i) in STEPS" :key="s.key" class="step" :class="stepState(i)">
            <span class="step-dot">
              <template v-if="stepState(i) === 'done'">✓</template>
              <template v-else-if="stepState(i) === 'error'">✕</template>
              <template v-else-if="!isContainer && stepState(i) === 'current' && s.key === 'downloading'">{{ status?.progress_percent ?? 0 }}%</template>
              <template v-else>{{ i + 1 }}</template>
            </span>
            <span class="step-label">{{ s.label }}</span>
          </div>
        </div>
        <NProgress
          v-if="status?.phase === 'downloading'"
          type="line"
          :percentage="status?.progress_percent || 0"
          :indeterminate="isContainer"
          :rail-height="8"
          :border-radius="4"
          :show-indicator="false"
          processing
          class="mt-2"
        />
        <div v-if="!updateDone" class="mt-3 text-center text-13px opacity-70">
          <template v-if="pollingStopped && statusError">
            {{ statusError }}
            <div class="mt-2"><NButton size="small" @click="reconnectStatus">重新連接</NButton></div>
          </template>
          <template v-else-if="connectionWaiting || status?.phase === 'restarting' || status?.phase === 'verifying'">
            服務正在重啓，請稍候。正在等待連接恢復，確認新版本就緒後本頁會自動刷新。
          </template>
          <template v-else-if="status?.phase === 'failed'">
            更新失敗：{{ status?.error_message }}
            <div class="mt-2 flex justify-center gap-8px">
              <NButton size="tiny" @click="retryFromFailed">重試</NButton>
              <NButton v-if="canRollback" size="tiny" type="warning" quaternary @click="doRollback">回滾上一版</NButton>
              <NButton size="tiny" quaternary @click="showConfirm = false">關閉</NButton>
            </div>
          </template>
          <template v-else>{{ isContainer ? "任務由升級助手持續執行；服務切換期間會暫時失聯，請等待恢復。" : "更新任務正在執行，請等待結果。" }}</template>
        </div>
      </template>

      <template #footer>
        <NSpace v-if="modalStage === 'confirm'" justify="end">
          <NButton size="small" @click="showConfirm = false">{{ canApply ? "取消" : "知道了" }}</NButton>
          <NButton v-if="canApply" size="small" type="primary" :disabled="inFlight" @click="doApply">立即更新</NButton>
        </NSpace>
        <div v-else class="text-center text-12px opacity-50">zcard 在線更新</div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.update-tab {
  max-width: 900px;
}
.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}
.stat-label {
  font-size: 12px;
  opacity: 0.6;
  margin-bottom: 4px;
}
.stat-value {
  font-size: 15px;
  font-weight: 500;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
}
.progress-block {
  padding: 12px;
  border-radius: 6px;
  background: var(--n-color-embedded, rgba(128, 128, 128, 0.08));
}
.progress-title {
  display: flex;
  justify-content: space-between;
  font-size: 13px;
  margin-bottom: 8px;
}
.pct {
  font-variant-numeric: tabular-nums;
}
.config-form .field {
  margin-bottom: 14px;
}
.field-label {
  font-size: 13px;
  margin-bottom: 6px;
  opacity: 0.75;
}
.version-line {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.arrow {
  font-size: 18px;
  opacity: 0.6;
}
.done-banner {
  text-align: center;
  padding: 18px 8px 10px;
}
.done-title {
  font-size: 18px;
  font-weight: 600;
  color: #18a058;
  margin-bottom: 10px;
}
.done-vers {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.done-hint {
  font-size: 12px;
  opacity: 0.6;
}

.steps {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 4px 4px;
}
.step {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.step-dot {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  border: 1.5px solid rgba(128, 128, 128, 0.35);
  color: rgba(128, 128, 128, 0.8);
  transition: all 0.2s;
}
.step.done .step-dot {
  border-color: #18a058;
  background: rgba(24, 160, 88, 0.12);
  color: #18a058;
}
.step.current .step-dot {
  border-color: var(--primary-color, #2080f0);
  background: rgba(32, 128, 240, 0.1);
  color: var(--primary-color, #2080f0);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.step.error .step-dot {
  border-color: #d03050;
  background: rgba(208, 48, 80, 0.12);
  color: #d03050;
}
.step-label {
  font-size: 12px;
  opacity: 0.75;
  white-space: nowrap;
}
.step.current .step-label {
  opacity: 1;
  font-weight: 500;
}

.changelog {
  max-height: 46vh;
  overflow: auto;
  font-size: 14px;
  line-height: 1.7;
  padding: 4px 8px;
  border-radius: 6px;
  background: rgba(128, 128, 128, 0.07);
}
.changelog :deep(h1),
.changelog :deep(h2),
.changelog :deep(h3) {
  font-size: 15px;
  margin: 12px 0 6px;
}
.changelog :deep(ul),
.changelog :deep(ol) {
  padding-left: 20px;
}
.changelog :deep(code) {
  padding: 1px 5px;
  border-radius: 4px;
  background: rgba(128, 128, 128, 0.15);
}
.history-list {
  max-height: 420px;
  overflow: auto;
}
.history-item {
  padding: 10px 4px;
  border-bottom: 1px solid rgba(128, 128, 128, 0.14);
}
.history-item:last-child {
  border-bottom: none;
}
.history-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.history-date {
  font-size: 12px;
  opacity: 0.55;
}
.history-notes {
  font-size: 13px;
  line-height: 1.7;
  color: inherit;
}
.history-notes :deep(ul) {
  padding-left: 18px;
}
.history-notes :deep(h1),
.history-notes :deep(h2),
.history-notes :deep(h3) {
  font-size: 14px;
  margin: 6px 0 4px;
}
</style>
