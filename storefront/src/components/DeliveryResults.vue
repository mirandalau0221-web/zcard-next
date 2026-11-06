<script setup lang="ts">
import { t as $t } from '@/i18n';

import { ref } from 'vue';
import type { DeliveryItem } from '@/api';
defineProps<{ items: DeliveryItem[] }>();
const copied = ref('');
async function copy(item: DeliveryItem) { try {await navigator.clipboard.writeText(item.content);copied.value=String(item.delivery_id || item.item_id);} catch {copied.value=$t("複製失敗，請手動選擇內容");} }
</script>
<template>
 <div class="delivery-results">
  <article v-for="(it,i) in items" :key="it.delivery_id || `${it.item_id}-${i}`" class="delivery-item">
   <b>{{ it.product_name || $t('交付結果') }}<span v-if="it.sku_name"> · {{ it.sku_name }}</span></b>
   <small>{{ it.kind === 'service' ? $t('服務已完成') : it.kind === 'logistics' ? $t('物流信息') : it.kind === 'link' ? $t('交付內容') : $t('卡密') }}</small>
   <pre>{{ it.content }}</pre><button class="btn secondary" @click="copy(it)">{{ copied === String(it.delivery_id || it.item_id) ? $t('已複製') : $t('複製內容') }}</button>
  </article>
  <p v-if="copied === $t('複製失敗，請手動選擇內容')" role="status">{{ copied }}</p>
 </div>
</template>
<style scoped>
.delivery-results{display:grid;gap:12px}.delivery-item{border:1px solid var(--border-color,#e5e7eb);padding:14px;border-radius:10px;min-width:0}.delivery-item small{display:block;margin-top:6px;color:var(--text-muted,#6b7280)}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;line-height:1.7}.btn{min-height:44px}
</style>
