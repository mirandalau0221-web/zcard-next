<script setup lang="ts">
// 更新可用徽標（方案 §10 靜默檢查面）：登錄後/每 24h 一次靜默檢查（localStorage 節流），
// 有新版本 → header 紅點徽標，點擊直達設置頁「系統更新」tab。靜默容錯——無權限/失敗即隱藏。
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import { NBadge, NButton, NTooltip } from "naive-ui";
import { fetchUpdateStatus } from "@/service/api";
import { checkAuth } from "@/directives";

defineOptions({ name: "UpdateBadge" });

const router = useRouter();
const visible = ref(false);
const latest = ref("");

const LS_LAST_CHECK = "zcard.update.lastCheckAt";
const LS_DISMISSED = "zcard.update.dismissedVer";
const THROTTLE_MS = 24 * 60 * 60 * 1000;

onMounted(async () => {
  if (!checkAuth("system:update")) return;
  const last = Number(localStorage.getItem(LS_LAST_CHECK) || 0);
  if (Date.now() - last < THROTTLE_MS) return;
  localStorage.setItem(LS_LAST_CHECK, String(Date.now()));
  try {
    const { data }: any = await fetchUpdateStatus();
    if (!data?.has_update || !data?.latest_version) return;
    if (localStorage.getItem(LS_DISMISSED) === data.latest_version) return;
    latest.value = data.latest_version;
    visible.value = true;
  } catch {
    // 靜默檢查失敗不打擾（無權限/網絡抖動）
  }
});

function goUpdate() {
  visible.value = false;
  router.push({ path: "/settings", query: { tab: "update" } });
}
</script>

<template>
  <NTooltip v-if="visible" placement="bottom">
    <template #trigger>
      <NBadge dot type="success" processing>
        <NButton size="tiny" quaternary circle aria-label="發現新版本" @click="goUpdate">
          <template #icon>
            <icon-mdi-cloud-upload-outline />
          </template>
        </NButton>
      </NBadge>
    </template>
    發現新版本 {{ latest }}，點擊前往更新
  </NTooltip>
</template>
