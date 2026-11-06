<script setup lang="ts">
import { ref } from "vue";
import { recoveryCodes } from "./state";
const saved = ref(false);
function download() {
  const url = URL.createObjectURL(
    new Blob(
      [
        "ZCard 後台兩步驗證恢復碼\n每個恢復碼只能使用一次，請離線保管。\n\n" +
          recoveryCodes.value.join("\n"),
      ],
      { type: "text/plain;charset=utf-8" },
    ),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = "zcard-recovery-codes.txt";
  a.click();
  URL.revokeObjectURL(url);
}
function close() {
  recoveryCodes.value = [];
  saved.value = false;
}
</script>
<template>
  <NModal
    :show="recoveryCodes.length > 0"
    :mask-closable="false"
    :close-on-esc="false"
    preset="card"
    title="保存恢復碼"
    :closable="false"
    style="width: 520px; max-width: calc(100vw - 24px)"
  >
    <NSpace vertical :size="16">
      <NAlert type="success">驗證器已綁定，舊後台會話已失效。請保存恢復碼後重新登錄。</NAlert>
      <p>恢復碼僅展示這一次。手機丟失時，可用密碼和任意一個未使用的恢復碼登錄。不要分享給他人。</p>
      <pre
        class="overflow-auto rounded p-12px"
        style="background: var(--n-color-modal); user-select: text"
        >{{ recoveryCodes.join("\n") }}</pre
      >
      <NButton @click="download">下載恢復碼</NButton>
      <NCheckbox v-model:checked="saved">我已安全保存恢復碼</NCheckbox>
      <NButton type="primary" :disabled="!saved" @click="close">完成，返回登錄</NButton>
    </NSpace>
  </NModal>
</template>
