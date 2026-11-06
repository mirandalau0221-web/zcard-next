<template>
  <!-- 客服代碼（service.widget_script）：第三方客服腳本自帶懸浮球，本站不渲染 UI -->
</template>

<script setup lang="ts">
import { loadPublicConfig } from '@/config';
import { onMounted } from 'vue';

/** 提取嵌入代碼中的 <script> 內容並執行（DOMParser 提取，防 XSS 依賴後端 sanitize/管理員自配） */
let injected = false;
function injectScript(html: string) {
  if (injected) return;
  injected = true;
  try {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const scripts = Array.from(doc.querySelectorAll('script'));
    if (!scripts.length) return;
    for (const s of scripts) {
      const code = s.textContent || '';
      if (!code.trim()) continue;
      const el = document.createElement('script');
      el.textContent = code;
      el.dataset.zcardServiceWidget = 'true';
      document.head.appendChild(el);
    }
  } catch { /* 腳本注入失敗忽略（客服功能降級為不可用） */ }
}

onMounted(async () => {
  try {
    const json = await loadPublicConfig();
    const entry = (json?.entries || []).find((e: any) => e.key === 'service.widget_script');
    let script = '';
    if (entry) {
      try { script = JSON.parse(entry.value_json); } catch { script = entry.value_json; }
    }
    if (typeof script === 'string' && script.trim()) injectScript(script);
  } catch { /* 配置接口失敗：客服不顯示 */ }
});
</script>
