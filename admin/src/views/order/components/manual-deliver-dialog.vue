<script setup lang="ts">
import { ref, watch, computed } from "vue";
import { NModal, NForm, NFormItem, NInput, NSelect, NButton, NAlert, NPopconfirm } from "naive-ui";
import OrderAnswers from '@/components/common/order-answers.vue';
import { fetchOrder, manualDeliver,startService, fetchProcurements, markProcurementManual, retryProcurement } from "@/service/api";

import { checkAuth } from "@/directives";

const props = defineProps<{ show: boolean; orderNo: string; defaultItemId?: number }>();
const emit = defineEmits<{ 'update:show': [boolean]; delivered: [] }>();
let requestSeq = 0;
const loading = ref(false);
const saving = ref(false);
const error = ref('');
const items = ref<any[]>([]);
const itemId = ref<number | null>(null);
const content = ref('');
const serviceContent=ref(''),deliveryKind=ref('service');
const selectedItem=computed(()=>items.value.find(it=>Number(it.id)===Number(itemId.value)));
const needsProcurementCheck = computed(() => selectedItem.value?.fulfillment_type === "upstream");
const procurements = ref<any[]>([]);
const procurementLoading = ref(false);
const procurementError = ref('');
const selectedProcurement = computed(() => procurements.value.find(p => String(p.order_item_id) === String(itemId.value)));
const procurementBlocked = computed(() => !!selectedProcurement.value && !['manual','rejected','refunding'].includes(selectedProcurement.value.status));
const canManageProcurement = computed(() => checkAuth('procurement:write'));
const canTransfer = computed(() => selectedProcurement.value && (['pending','submitted','polling'].includes(selectedProcurement.value.status) || (selectedProcurement.value.status === 'fulfilled' && selectedProcurement.value.delivery_incomplete)));
const canRecover = computed(() => canTransfer.value && Number(selectedProcurement.value?.received_cards) > 0);
const messageOf = (err: any, fallback: string) => err?.response?.data?.message || err?.message || fallback;
async function loadProcurements(seq = requestSeq) {
  if (!checkAuth('procurement:read')) return;
  procurementLoading.value = true; procurementError.value = '';
  const collected: any[] = [];
  try {
    for (let page = 1; ; page++) {
      const { data, error: err } = await fetchProcurements({order_no: props.orderNo, page, page_size: 100});
      if (seq !== requestSeq) return;
      if (err || !data) { procurementError.value = '讀取採購狀態失敗，請重試後補發'; return; }
      const rows = (data as any).procurements || [];
      collected.push(...rows);
      if (!rows.length || collected.length >= Number((data as any).total || 0)) break;
    }
    procurements.value = collected;
  } finally { if (seq === requestSeq) procurementLoading.value = false; }
}
async function transferManual() {
  if (!selectedProcurement.value || saving.value) return;
  saving.value = true; error.value = '';
  try {
    const {error: err} = await markProcurementManual(selectedProcurement.value.id, '管理員在訂單詳情核實上游結果後轉人工補發');
    if (err) { error.value = messageOf(err, '轉人工失敗，請刷新核對'); return; }
    await loadProcurements();
    emit('delivered');
  } finally { saving.value = false; }
}
async function recoverDelivery() {
  if (!selectedProcurement.value || saving.value) return;
  saving.value = true; error.value = '';
  try {
    const {error: err} = await retryProcurement(selectedProcurement.value.id);
    if (err) { error.value = messageOf(err, '重試交付失敗，請核實後轉人工'); return; }
    emit('update:show', false); emit('delivered');
    window.$message?.success('已處理交付恢復，請核對訂單狀態');
  } finally { saving.value = false; }
}
const isManual=computed(()=>selectedItem.value?.fulfillment_type==='manual');
async function start(){if(saving.value||!itemId.value)return;saving.value=true;try{const {error:err}=await startService(props.orderNo,itemId.value);if(!err){const {data}=await fetchOrder(props.orderNo);items.value=(data as any)?.items || [];emit('delivered');}}finally{saving.value=false}}
const logistics = ref('');
const remark = ref('');
const options = computed(() => items.value.map(it => ({
  label: `${it.name || '#' + it.product_id}${it.sku_name ? ' / ' + it.sku_name : ''} · 購買 ${it.quantity} 件${it.fulfillment_status === 'delivered' ? '（已發貨）' : ''}`,
  value: Number(it.id), disabled: it.fulfillment_status === 'delivered' || it.fulfillment_status === 'refunded',
})));
watch(() => [props.show, props.orderNo, props.defaultItemId], async () => {
  const seq = ++requestSeq;
  if (!props.show || !props.orderNo) return;
  loading.value = true;
  error.value = ''; items.value = []; itemId.value = null; procurements.value = []; procurementError.value = ''; procurementLoading.value = false;
  content.value = ''; logistics.value = ''; remark.value = '';serviceContent.value='';deliveryKind.value='service';
  try {
    const { data, error: err } = await fetchOrder(props.orderNo);
    if (seq !== requestSeq) return;
    if (err || !data) { error.value = '讀取訂單失敗，請關閉後重試'; return; }
    items.value = (data as any).items || [];
    itemId.value = props.defaultItemId
      ? options.value.find(it => it.value === Number(props.defaultItemId) && !it.disabled)?.value ?? null
      : options.value.find(it => !it.disabled)?.value ?? null;
    if (!itemId.value) error.value = "該採購商品已發貨或不可補發，請刷新採購單核對狀態";
    if (items.value.some(it => it.fulfillment_type === "upstream")) await loadProcurements(seq);
  } finally { if (seq === requestSeq) loading.value = false; }
});
async function submit() {
  if (saving.value || !itemId.value || (needsProcurementCheck.value && (procurementLoading.value || procurementError.value)) || procurementBlocked.value) return;
  if (!(isManual.value && deliveryKind.value==='service') && !content.value.trim() === !logistics.value.trim()) { error.value = '卡密內容與物流單號必須且只能填寫一項'; return; }
  saving.value = true; error.value = '';
  try {
    const { error: err } = await manualDeliver(props.orderNo, { order_item_id: itemId.value,service_content:isManual.value&&deliveryKind.value==='service'?serviceContent.value.trim():undefined, content: (!isManual.value||deliveryKind.value==='card')?content.value.trim() || undefined:undefined, logistics_no:!isManual.value?logistics.value.trim() || undefined:undefined, remark: remark.value.trim() || undefined });
    if (err) { error.value = messageOf(err, '補發未成功，請檢查後重試'); return; }
    window.$message?.success(isManual.value ? '服務已完成，買家可在原訂單查看交付結果' : '補發成功，買家可憑原訂單查詢');
    emit('update:show', false); emit('delivered');
  } finally { saving.value = false; }
}
</script>

<template>
  <NModal :show="show" @update:show="v => !saving && emit('update:show', v)" preset="card" :title="isManual ? '處理人工服務' : '人工補發'" style="width: min(640px, calc(100vw - 24px))">
    <NAlert v-if="!isManual" type="info" class="mb-16px">補發內容歸屬所選商品，每行一條卡密，數量須與待發數量一致。買家使用原訂單取貨，無需重新下單或付款。</NAlert>
    <NAlert v-if="error" type="error" class="mb-12px">{{ error }}</NAlert>
    <NForm label-placement="top" :disabled="loading || saving">
      <NFormItem label="訂單號"><NInput :value="orderNo" disabled /></NFormItem>
      <NFormItem label="補發商品"><NSelect v-model:value="itemId" :options="options" :disabled="!!defaultItemId" :loading="loading" placeholder="請選擇商品" /></NFormItem>
      <NAlert v-if="needsProcurementCheck && procurementError" type="error" class="mb-12px">{{ procurementError }} <NButton text @click="loadProcurements()">重試</NButton></NAlert>
      <NAlert v-if="needsProcurementCheck && checkAuth('procurement:read') && !loading && !procurementLoading && !procurementError && !selectedProcurement" type="info" class="mb-12px">未找到該商品的採購單。請先核實上游是否扣款或出貨，再填寫補發內容。</NAlert>
      <NAlert v-if="procurementBlocked" type="warning" class="mb-12px">
        該商品關聯採購單 #{{ selectedProcurement.id }}。採購仍在處理或已返回結果，請先核對上游，避免重複發貨。
        <div v-if="canManageProcurement" class="mt-8px flex gap-8px flex-wrap">
          <NButton v-if="canRecover" :loading="saving" @click="recoverDelivery">重試已有卡密交付</NButton>
          <NPopconfirm v-if="canTransfer" @positive-click="transferManual"><template #trigger><NButton :disabled="saving">核實後轉人工</NButton></template>確認已核實上游扣款及出貨結果，需要停止自動交付並人工補發？</NPopconfirm>
        </div>
        <p v-else>請讓有采購管理權限的管理員核實並轉人工。</p>
      </NAlert>
      <div v-if="selectedItem"><b>買家填寫資料</b><OrderAnswers :value="selectedItem.form_answers_json" /></div>
      <NAlert v-if="isManual" type="info" class="my-12px">先核對資料並開始處理，完成實際服務後再提交交付結果。內部備註不會顯示給買家。</NAlert>
      <NButton v-if="isManual && selectedItem?.fulfillment_status === 'pending'" class="mb-12px" :loading="saving" @click="start">開始處理</NButton>
      <NFormItem v-if="isManual" label="交付內容類型"><NSelect v-model:value="deliveryKind" :options="[{label:'服務完成説明',value:'service'},{label:'卡密',value:'card'}]" /></NFormItem>
      <NFormItem v-if="isManual && deliveryKind === 'service'" label="交付説明（買家可見）"><NInput v-model:value="serviceContent" type="textarea" :rows="5" :maxlength="4000" placeholder="如：已完成開通；能量交付可附交易編號" /></NFormItem>
      <NFormItem v-if="!isManual || deliveryKind === 'card'" label="卡密內容（每行一條）"><NInput v-model:value="content" type="textarea" :rows="6" placeholder="粘貼需要補發的卡密" /></NFormItem>
      <NFormItem v-if="!isManual" label="物流單號（與卡密內容二選一）"><NInput v-model:value="logistics" /></NFormItem>
      <NFormItem label="內部處理備註"><NInput v-model:value="remark" placeholder="例如：上游餘額不足，人工補卡" /></NFormItem>
    </NForm>
    <template #footer><div class="flex justify-end gap-8px"><NButton :disabled="saving" @click="emit('update:show', false)">取消</NButton><NButton type="primary" :loading="saving" :disabled="loading || (needsProcurementCheck && (procurementLoading || !!procurementError)) || procurementBlocked || !itemId || (isManual && (selectedItem?.fulfillment_status !== 'delivering' || (deliveryKind === 'service' && !serviceContent.trim())))" @click="submit">{{isManual ? '確認完成' : '確認補發'}}</NButton></div></template>
  </NModal>
</template>
