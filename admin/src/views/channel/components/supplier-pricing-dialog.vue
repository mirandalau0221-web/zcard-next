<script setup lang="ts">
import { computed, h, ref, watch, onUnmounted } from 'vue';
import { NModal, NAlert, NButton, NForm, NFormItem, NSelect, NInputNumber, NDataTable, NPopconfirm, NTag } from 'naive-ui';
import { fetchSupplierPrices, upsertSupplierPrice, deleteSupplierPrice, fetchCategories, fetchProducts, fetchSkus } from '@/service/api';
import { currencyUnit, formatMoney, yuanToFen } from '@/utils/money';
import { checkAuth } from '@/directives';
const props = defineProps<{ show: boolean; account: any }>();
const emit = defineEmits<{ 'update:show': [boolean] }>();
const scope = ref('global'), discount = ref<number | null>(9), price = ref<number | null>(null);
const categoryId = ref<number | null>(null), productId = ref<number | null>(null), skuId = ref(0);
const rows = ref<any[]>([]), categories = ref<any[]>([]), products = ref<any[]>([]), skus = ref<any[]>([]);
const loading = ref(false), saving = ref(false), searching = ref(false), skuLoading = ref(false), error = ref('');
const editing = ref<number | null>(null);
const canWrite = () => checkAuth('supplier:write');
let seq = 0, searchSeq = 0, skuSeq = 0, timer: ReturnType<typeof setTimeout> | undefined;
const scopes = [{ value: 'global', label: '整站折扣' }, { value: 'category', label: '分類折扣' }, { value: 'product', label: '商品專屬價' }];
const categoryOptions = computed(() => {
  const byId = new Map(categories.value.map(c => [Number(c.id), c]));
  return categories.value.map(c => {
    const parts = [c.name], seen = new Set([Number(c.id)]); let parent = Number(c.parent_id);
    while (parent && !seen.has(parent) && byId.has(parent)) { seen.add(parent); const node = byId.get(parent); parts.unshift(node.name); parent = Number(node.parent_id); }
    return { label: `${parts.join(' / ')} · #${c.id}`, value: Number(c.id) };
  });
});
const productOptions = computed(() => products.value.map(p => ({ label: `${p.name} · #${p.id}`, value: Number(p.id) })));
const skuOptions = computed(() => [{ label: '商品默認價（全部規格）', value: 0 }, ...skus.value.map(s => ({ label: s.name || `規格 #${s.id}`, value: Number(s.id) }))]);
const summary = computed(() => scope.value === 'product'
  ? `該商品${skuId.value ? '所選規格' : ''}按固定價供貨，優先於分類及整站折扣。`
  : `按基礎供貨價的 ${discount.value ?? '—'} 折結算。例如基礎供貨價 ${formatMoney(10000)}，折後 ${formatMoney(Math.round(1000 * (discount.value || 0)))}。`);
function resetForm() { editing.value = null; productId.value = null; categoryId.value = null; skuId.value = 0; price.value = null; discount.value = 9; error.value = ''; }
async function load() {
  const current = seq, id = props.account?.id;
  loading.value = true;
  const { data, error: err } = await fetchSupplierPrices(id);
  if (current !== seq) return;
  loading.value = false;
  if (err) { error.value = '讀取定價規則失敗，請重試'; return; }
  rows.value = (data as any)?.prices || [];
}
async function searchProducts(keyword = '') {
  const current = ++searchSeq; searching.value = true;
  const { data, error: err } = await fetchProducts({ keyword, page: 1, page_size: 30, status: -1 });
  if (current !== searchSeq) return;
  searching.value = false;
  if (err) { error.value = '商品搜索失敗，請重試'; return; }
  products.value = (data as any)?.products || [];
}
function onSearch(keyword: string) { if (timer) clearTimeout(timer); ++searchSeq; timer = setTimeout(() => searchProducts(keyword), 250); }
watch(productId, async id => {
  const current = ++skuSeq; skus.value = []; if (!editing.value) skuId.value = 0;
  if (!id) { skuLoading.value = false; return; }
  skuLoading.value = true;
  const { data, error: err } = await fetchSkus(id);
  if (current !== skuSeq) return;
  skuLoading.value = false;
  if (err) { error.value = '規格讀取失敗，請重新選擇商品'; return; }
  skus.value = (data as any)?.skus || (data as any)?.items || [];
});
watch(() => [props.show, props.account?.id], async () => {
  const current = ++seq; ++searchSeq; ++skuSeq;
  if (!props.show) { if (timer) clearTimeout(timer); return; }
  rows.value = []; resetForm(); scope.value = 'global';
  void load(); void searchProducts();
  const { data, error: err } = await fetchCategories();
  if (current !== seq) return;
  if (err) { error.value = '分類讀取失敗，請關閉面板後重試'; return; }
  categories.value = (data as any)?.categories || [];
});
onUnmounted(() => { ++seq; ++searchSeq; ++skuSeq; if (timer) clearTimeout(timer); });
function edit(row: any) {
  editing.value = row.id; scope.value = row.scope || 'product'; error.value = '';
  categoryId.value = Number(row.category_id) || null;
  if (row.product_id && !products.value.some(p => Number(p.id) === Number(row.product_id))) products.value.unshift({ id: row.product_id, name: row.product_name || `商品 #${row.product_id}` });
  productId.value = Number(row.product_id) || null; skuId.value = Number(row.sku_id) || 0;
  price.value = Number(row.price) / 100; discount.value = Number(row.discount_bps) / 1000;
}
async function save() {
  if (saving.value || !canWrite()) return;
  error.value = '';
  if (scope.value === 'product' && (!productId.value || !price.value || price.value <= 0)) { error.value = '請選擇商品並填寫有效的專屬價'; return; }
  if (scope.value === 'category' && !categoryId.value) { error.value = '請選擇要設置折扣的分類'; return; }
  if (scope.value !== 'product' && (!discount.value || discount.value <= 0 || discount.value > 10)) { error.value = '折扣須大於 0 且不超過 10 折'; return; }
  saving.value = true;
  try {
    const { error: err } = await upsertSupplierPrice({ account_id: props.account.id, scope: scope.value,
      product_id: scope.value === 'product' ? productId.value! : 0, sku_id: scope.value === 'product' ? skuId.value : 0,
      category_id: scope.value === 'category' ? categoryId.value! : 0,
      price: scope.value === 'product' ? yuanToFen(price.value!) : 0,
      discount_bps: scope.value === 'product' ? 0 : Math.round(discount.value! * 1000),
    });
    if (err) { error.value = '保存失敗，請檢查提示後重試'; return; }
    window.$message?.success('定價規則已生效，後續報價與新訂單按此結算'); editing.value = null; await load();
  } finally { saving.value = false; }
}
async function remove(row: any) {
  const { error: err } = await deleteSupplierPrice(row.id);
  if (err) return;
  if (editing.value === row.id) resetForm();
  window.$message?.success('規則已刪除，按下一優先級定價'); await load();
}
const columns = computed(() => [
  { title: '範圍', key: 'scope', width: 112, render: (r: any) => h(NTag, { bordered: false, size: 'small' }, { default: () => scopes.find(s => s.value === (r.scope || 'product'))?.label || '商品專屬價' }) },
  { title: '適用對象', key: 'target', minWidth: 180, render: (r: any) => r.scope === 'global' ? '全站商品（含後續新增）' : r.scope === 'category' ? `${r.category_name || '#' + r.category_id}（含子分類）` : `${r.product_name || '商品 #' + r.product_id}${r.sku_id ? ' / ' + (r.sku_name || '規格 #' + r.sku_id) : ' / 商品默認價'}` },
  { title: '供貨價格', key: 'price', width: 112, render: (r: any) => r.scope === 'global' || r.scope === 'category' ? `${Number(r.discount_bps) / 1000} 折` : formatMoney(r.price) },
  { title: '操作', key: 'actions', width: 128, render: (r: any) => canWrite() ? h('div', { style: 'display:flex;align-items:center;gap:16px' }, [h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => edit(r) }, { default: () => '編輯' }), h(NPopconfirm, { onPositiveClick: () => remove(r) }, { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '刪除' }), default: () => '刪除後按下一優先級規則或基礎供貨價結算，確定刪除？' })]) : null },
]);
</script>
<template>
  <NModal :show="show" preset="card" :title="`供貨定價 · ${account?.name || ''}`" class="supplier-pricing" style="width: min(960px, calc(100vw - 24px)); max-height: 92vh; overflow: auto" :mask-closable="!saving" :closable="!saving" @update:show="v => !saving && emit('update:show', v)">
    <p class="pricing-intro">為該供貨賬號設置價格。分類和整站折扣自動覆蓋以後新增的商品，已創建訂單金額保持不變。</p>
    <NAlert type="info" :bordered="false" class="mb-16px">優先級：規格專屬價 → 商品專屬價 → 最近一級分類折扣 → 整站折扣 → 基礎供貨價。規則不疊加。</NAlert>
    <div class="pricing-scopes" aria-label="定價範圍">
      <NButton v-for="s in scopes" :key="s.value" :type="scope === s.value ? 'primary' : 'default'" :aria-pressed="scope === s.value" :disabled="saving || !!editing" @click="scope = s.value; error = ''">{{ s.label }}</NButton>
    </div>
    <div class="pricing-editor">
      <NForm label-placement="top" :disabled="saving || !canWrite()">
        <NFormItem v-if="scope === 'category'" label="選擇分類（包含全部子分類）" required><NSelect v-model:value="categoryId" :options="categoryOptions" filterable clearable :disabled="!!editing" placeholder="搜索分類名稱，支持查看完整層級" /></NFormItem>
        <NFormItem v-if="scope === 'product'" label="選擇商品" required><NSelect v-model:value="productId" :options="productOptions" filterable remote clearable :disabled="!!editing" :loading="searching" placeholder="輸入商品名稱搜索" @search="onSearch" /></NFormItem>
        <NFormItem v-if="scope === 'product'" label="適用規格"><NSelect v-model:value="skuId" :options="skuOptions" :loading="skuLoading" :disabled="!productId || !!editing" /></NFormItem>
        <NFormItem v-if="scope === 'product'" :label="`專屬供貨價（${currencyUnit()}）`" required><NInputNumber v-model:value="price" :min="0.01" :precision="2" class="w-full" /></NFormItem>
        <NFormItem v-else label="供貨折扣（折）" required><NInputNumber v-model:value="discount" :min="0.01" :max="10" :precision="2" :step="0.1" class="w-full" placeholder="例如 9 表示九折" /></NFormItem>
        <NAlert v-if="error" type="error" class="mb-12px">{{ error }}</NAlert>
        <div class="rule-actions"><NButton v-if="canWrite()" type="primary" :loading="saving" :disabled="loading || skuLoading" @click="save">{{ editing ? '保存修改' : '保存規則' }}</NButton><NButton :disabled="saving" @click="resetForm">{{ editing ? '取消編輯' : '清空' }}</NButton></div>
      </NForm>
      <aside class="pricing-preview"><strong>{{ editing ? '編輯已有規則' : '計價説明' }}</strong><p>{{ summary }}</p><p v-if="scope !== 'product'">基礎供貨價取商品設置中的售價，不按成本價計算，也不疊加會員折扣或促銷。</p><p v-if="scope === 'category'">選中分類及子分類均生效。子分類另有規則時，優先使用更具體的分類折扣。</p><p v-if="scope === 'global'">適用於未設置商品專屬價或分類折扣的商品。</p><p>相同範圍再次保存會更新原規則；刪除後恢復下一優先級定價。金額四捨五入到分，折後最低為 0.01 {{ currencyUnit() }}。</p></aside>
    </div>
    <div class="pricing-list-head"><strong>已生效規則（{{ rows.length }}）</strong><NButton text :disabled="loading" @click="load">刷新</NButton></div>
    <NDataTable :columns="columns" :data="rows" :loading="loading" :scroll-x="600" :max-height="280" size="small" />
    <template #footer><div class="flex justify-end"><NButton :disabled="saving" @click="emit('update:show', false)">完成</NButton></div></template>
  </NModal>
</template>
<style scoped>
.supplier-pricing { width: min(960px, calc(100vw - 24px)); max-height: 92vh; overflow: auto; }
.pricing-intro { margin: 0 0 16px; }
.pricing-scopes { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 20px; }
.pricing-scopes :deep(button) { min-height: 44px; flex: 1; }
.pricing-editor { display: grid; grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr); gap: 24px; }
.pricing-preview { padding: 16px; border: 1px solid var(--n-border-color); border-radius: 8px; line-height: 1.7; align-self: start; }
.pricing-list-head { display: flex; justify-content: space-between; align-items: center; margin: 20px 0 12px; }
.rule-actions { display: flex; align-items: center; gap: 16px; }
@media (max-width: 640px) { .pricing-editor { grid-template-columns: 1fr; gap: 12px; } .rule-actions :deep(button) { min-height: 44px; } }
</style>
