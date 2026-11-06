<script setup lang="ts">
import { t as $t } from '@/i18n';

import { ref, onMounted } from 'vue';
import type { MyLevelReply } from '@/api';
import { formatMoney } from '@/api/client';
import { levelDiscount, levelThreshold } from '@/composables/member-level';
defineProps<{ level: MyLevelReply }>();
const expanded = ref(false);
onMounted(() => { expanded.value = matchMedia('(min-width: 768px)').matches; });
</script>
<template>
  <section v-if="level.levels?.length || level.private_level || level.has_referral_level" class="card level-benefits" :aria-label="$t('會員等級與折扣')">
    <header><div><h3>{{ $t('會員等級與折扣') }}</h3><p class="muted">{{ $t('當前：') }}{{ level.current?.name || (level.private_level ? $t('專屬會員') : $t('普通會員')) }} · {{ level.private_level || level.current?.display_mode === 'contact' ? $t('聯繫客服') : levelDiscount(level.current?.discount ?? 10000) }}</p></div>
      <span v-if="level.next" class="muted">{{ $t('下一等級：') }}{{ level.next.name }}</span></header>
    <p v-if="level.source === 'manual'" class="muted">{{ $t('當前等級由後台指定。') }}</p>
    <p v-else-if="level.source === 'referral'" class="muted">{{ $t('已通過推薦註冊獲得會員等級，無需先滿足充值或消費門檻。') }}</p>
    <p v-else-if="level.has_referral_level" class="muted">{{ $t('已保留推薦註冊資格，當前自動等級的會員折扣優先適用。') }}</p>
    <p v-if="level.next" class="next-benefit"><b>{{ $t('{0}：{1}', [level.next.name, level.next.display_mode === 'contact' ? $t('聯繫客服') : levelDiscount(level.next.discount)]) }}</b><span>{{ levelThreshold(level.next) }}</span></p>
    <details v-if="level.levels?.length" :open="expanded" @toggle="expanded = ($event.target as HTMLDetailsElement).open">
      <summary>{{ expanded ? $t('收起等級列表') : $t('查看全部 {0} 個等級與折扣', [level.levels.length]) }}</summary>
      <div class="level-list">
      <article v-for="item in level.levels" :key="item.id || item.name" class="level-item" :class="{ current: !!item.id && item.id === level.current?.id }">
        <div class="level-name"><b>{{ item.name }}</b><span v-if="!!item.id && item.id === level.current?.id" class="level-current">{{ $t('當前等級') }}</span></div>
        <strong>{{ item.display_mode === 'contact' ? $t('聯繫客服') : levelDiscount(item.discount) }}<small v-if="item.display_mode !== 'contact'">{{ $t('會員折扣') }}</small></strong>
        <p>{{ levelThreshold(item) }}</p>
      </article>
      </div>
    </details>
    <p class="muted level-note">{{ $t('已累計充值') }} {{ formatMoney(level.recharged_cents) }} {{ $t('· 已累計消費') }} {{ formatMoney(level.consumed_cents) }}{{ $t('。充值與消費按各等級條件累計，商品實際優惠以結算價為準。') }}</p>
  </section>
</template>
<style scoped>
.level-benefits header { display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:8px; margin-bottom:14px; }
h3 { margin:0; font-size:16px; } header p { margin:6px 0 0; }
.next-benefit { display:flex; flex-direction:column; gap:6px; padding:10px 12px; background:var(--zc-primary-soft); border-radius:8px; margin:0 0 8px; font-size:13px; }
.next-benefit span { color:#6b7280; font-size:12px; }
summary { cursor:pointer; min-height:44px; align-content:center; color:var(--zc-primary); font-size:13px; }
.level-list { display:grid; grid-template-columns:repeat(auto-fit,minmax(200px,1fr)); gap:12px; }
.level-item { border:1px solid var(--border-color,#e5e7eb); border-radius:10px; padding:16px; min-width:0; }
.level-item.current { border-color:var(--zc-primary); background:var(--zc-primary-soft); }
.level-name { display:flex; flex-wrap:wrap; align-items:center; gap:8px; }
.level-current { font-size:11px; color:var(--zc-primary); }
.level-item strong { display:block; font-size:24px; color:var(--zc-primary); margin:12px 0; }
.level-item small { font-size:12px; font-weight:400; color:#6b7280; margin-left:8px; }
.level-item p { font-size:12px; line-height:1.7; margin:0; overflow-wrap:anywhere; }
.level-note { margin:12px 0 0; font-size:12px; line-height:1.7; }
@media(max-width:480px) { .level-list { grid-template-columns:minmax(0,1fr); } .level-item { padding:12px; } }
</style>
