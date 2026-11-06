<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { t as $t, uiText } from '@/i18n';
import { authState } from '@/auth';
import { api, formatMoney, getToken } from '@/api/client';
import { createPayment, fetchPaymentChannels, type Product } from '@/api';
import { buySocial, socialLineCount, socialQuote, socialSchema, socialSchemaSupported, type SocialSchema, type SocialQuote } from '@/api/social';
import { newRequestId } from '../../../packages/request-id';
const props=defineProps<{product:Product}>();
const router=useRouter();
const schema=ref<SocialSchema>();
const values=ref<Record<string,string>>({}),errors=ref<Record<string,string>>({});
const quote=ref<SocialQuote>(),error=ref(''),busy=ref(false),loading=ref(false),now=ref(Date.now());
type Intent={quote_id:string;request_id:string;amount_cents:number;order_no?:string;channel?:string};
const pending=ref<Intent>();
const loggedIn=computed(()=>authState.loggedIn || !!getToken());
const key=computed(()=>`zcard_social_purchase:${authState.username}:${props.product.id}`);
const supported=computed(()=>!!schema.value && socialSchemaSupported(schema.value));
const canBuy=computed(()=>supported.value && schema.value?.purchasable && !busy.value && loggedIn.value);
const expired=computed(()=>!quote.value || Number(quote.value.expires_at)*1000<=now.value);
const quantityField=computed(()=>schema.value?.quantity.mode==='explicit'?schema.value.quantity.field || 'quantity':'');
const presets=computed(()=>{
 const q=schema.value?.quantity;if(!q || q.mode!=='explicit')return [];
 const f=schema.value?.fields.find(f=>f.key===quantityField.value);const min=Number(q.min??f?.min??1),max=Number(q.max??f?.max??Number.MAX_SAFE_INTEGER),step=Number(q.step??1),base=Number(q.step_base??0);
 return [...new Set([min,min*2,min*5])].filter(n=>Number.isSafeInteger(n) && n>=min && n<=max && n>=base && (n-base)%step===0);
});
const derivedCount=computed(()=>schema.value?.quantity.mode==='derived_lines'?lineCount(schema.value.quantity.lines_field || ''):undefined);
function lineCount(key:string){const q=schema.value?.quantity;return socialLineCount(values.value[key] || '',q?.mode==='derived_lines' && q.lines_field===key?q.line_policy:undefined)}
let closed=false,generation=0,timer:ReturnType<typeof setInterval>|undefined;
function member(){return {key:key.value,token:getToken(),generation}}
function same(m:ReturnType<typeof member>){return !closed && m.key===key.value && m.token===getToken() && m.generation===generation}
function saveIntent(value?:Intent){pending.value=value;try{if(value)sessionStorage.setItem(key.value,JSON.stringify(value));else sessionStorage.removeItem(key.value)}catch{}}
function restore(){pending.value=undefined;try{const v=JSON.parse(sessionStorage.getItem(key.value)||'null');if(v?.quote_id && v?.request_id && Number(v.amount_cents)>0)pending.value=v}catch{}}
async function load(){
 if(!loggedIn.value)return;const m=member();loading.value=true;error.value='';
 const r=await socialSchema(props.product.id);if(!same(m))return;
 loading.value=false;if(r.data){schema.value=r.data;values.value={};quote.value=undefined}else error.value=r.error || $t('無法讀取服務規則');
}
function update(key:string,value:string){values.value={...values.value,[key]:value};quote.value=undefined;errors.value={...errors.value,[key]:''}}
function parameters(){const out:Record<string,string>={};for(const f of schema.value?.fields || []){const v=values.value[f.key];if(v!==undefined && (v!=='' || f.required))out[f.key]=v}return out}
function validate():boolean{
 const result:Record<string,string>={};
 for(const f of schema.value?.fields || []){
  const v=values.value[f.key] ?? '';
  if(f.required && v.trim()===''){result[f.key]=$t('請填寫此項');continue}
  if(v==='')continue;
  if(f.max_length && [...v].length>f.max_length)result[f.key]=$t('內容超過允許長度');
  if(f.type==='integer'){
   const n=Number(v);if(!/^-?\d+$/.test(v) || !Number.isSafeInteger(n) || (f.min!==undefined && n<Number(f.min)) || (f.max!==undefined && n>Number(f.max)))result[f.key]=$t('請輸入範圍內的整數');
  }
  if(f.type==='lines'){const count=lineCount(f.key);if((f.min_items!==undefined && count<Number(f.min_items)) || (f.max_items!==undefined && count>Number(f.max_items)))result[f.key]=$t('填寫行數不符合服務範圍')}
 }
 const q=schema.value?.quantity;
 if(q?.mode==='explicit'){
  const key=q.field || 'quantity',n=Number(values.value[key]);
  if((q.min!==undefined && n<Number(q.min)) || (q.max!==undefined && n>Number(q.max)))result[key]=$t('請輸入範圍內的整數');
  if(q.step!==undefined && (Number(q.step)<=0 || (n-Number(q.step_base || 0))%Number(q.step)!==0))result[key]=$t('服務數量不符合允許的步長');
 }
 if(q?.mode==='range_per_event' && Number(values.value[q.min_field || ''])>Number(values.value[q.max_field || '']))result[q.max_field || 'max']=$t('最小數量不能大於最大數量');
 errors.value=result;return Object.keys(result).length===0;
}
async function confirmPrice(){
 if(!canBuy.value || pending.value || !schema.value || !validate())return;
 const m=member();busy.value=true;error.value='';
 const r=await socialQuote(props.product.id,schema.value.revision,parameters());if(!same(m))return;
 busy.value=false;if(r.data){quote.value=r.data;now.value=Date.now()}else error.value=r.error || $t('報價失敗，請稍後重試');
}
async function purchase(){
 if(busy.value || !loggedIn.value)return;
 if(!pending.value){if(!quote.value || expired.value){error.value=$t('報價已過期，請重新確認價格');return}saveIntent({quote_id:quote.value.quote_id,request_id:newRequestId(),amount_cents:Number(quote.value.amount_cents)})}
 const m=member(),intent=pending.value!;busy.value=true;error.value='';
 if(!intent.order_no){
  const r=await buySocial(props.product.id,intent.quote_id,intent.request_id);if(!same(m))return;
  if(!r.data){busy.value=false;error.value=r.error || $t('請確認原購買結果後重試');return}
  if(Number(r.data.total_cents)!==intent.amount_cents){busy.value=false;error.value=$t('原訂單金額與報價不一致，請聯繫客服核對');return}
  intent.order_no=r.data.order_no;saveIntent(intent);
 }
 const current=await api.get<{status:string}>(`/orders/${encodeURIComponent(intent.order_no)}`);if(!same(m))return;
 if(!current.data){busy.value=false;error.value=current.error || $t('無法讀取原訂單，請稍後重試');return}
 if(['canceled','expired'].includes(current.data.status)){saveIntent();quote.value=undefined;busy.value=false;error.value=$t('原訂單已取消或過期，請重新報價');return}
 if(current.data.status!=='pending_payment'){saveIntent();busy.value=false;await router.push(`/order/${encodeURIComponent(intent.order_no)}`);return}
 if(!intent.channel){const channels=await fetchPaymentChannels();if(!same(m))return;const wallet=channels.data?.channels?.find(c=>c.driver==='wallet' && !Number(c.fee || 0));if(!wallet){busy.value=false;error.value=channels.error || $t('請聯繫商戶啓用免手續費餘額支付');return}intent.channel=wallet.code;saveIntent(intent)}
 const paid=await createPayment(intent.order_no,intent.channel);if(!same(m))return;
 busy.value=false;if(paid.error){error.value=paid.error;return}
 saveIntent();await router.push(`/order/${encodeURIComponent(intent.order_no)}`);
}
watch([key,loggedIn],()=>{generation++;busy.value=false;loading.value=false;values.value={};schema.value=undefined;quote.value=undefined;errors.value={};error.value='';if(loggedIn.value){restore();void load()}else pending.value=undefined});
onMounted(()=>{restore();void load();timer=setInterval(()=>now.value=Date.now(),1000)});
onBeforeUnmount(()=>{closed=true;generation++;if(timer)clearInterval(timer)});
</script>
<template>
 <section class="social-product">
  <div class="social-summary card">
   <p class="social-eyebrow">{{ $t('在線服務') }}</p><h1>{{ product.name }}</h1>
   <p class="muted">{{ $t('按服務要求填寫資料，確認總價後購買。在訂單詳情中查看進度。') }}</p>
   <div v-if="schema" class="social-support"><span>{{schema.cancel?$t('支持申請取消'):$t('當前服務不支持取消')}}</span><span>{{schema.refill?$t('支持申請補量'):$t('當前服務不支持補量')}}</span></div>
   <p class="muted">{{ $t('取消或部分完成的退款須經供貨確認，實際退款金額將在訂單頁顯示。') }}</p>
  </div>
  <div class="card social-form">
   <template v-if="!loggedIn"><h2>{{ $t('填寫服務信息') }}</h2><p class="muted">{{ $t('登錄後填寫資料、確認價格並使用會員餘額付款。') }}</p><router-link class="btn" :to="{path:'/login',query:{redirect:`/product/${product.id}`}}">{{ $t('登錄購買') }}</router-link></template>
   <p v-else-if="loading" role="status">{{ $t('正在讀取服務規則…') }}</p>
   <template v-else-if="schema">
    <h2>{{ $t('填寫服務信息') }}</h2>
    <p v-if="!supported || !schema.purchasable" class="social-unavailable" role="status">{{uiText(schema.unavailable_reason || $t('當前服務暫不可購買，請聯繫商戶'))}}</p>
    <form v-if="supported" @submit.prevent="confirmPrice">
     <fieldset :disabled="busy || !!pending || !schema.purchasable">
      <div v-for="field in schema.fields" :key="field.key" class="social-field">
       <label :for="`social-${product.id}-${field.key}`">{{field.label}}<span v-if="field.required"> {{ $t('（必填）') }}</span></label>
       <div v-if="field.key===quantityField && presets.length" class="social-presets"><span>{{ $t('快捷數量') }}</span><button v-for="n in presets" :key="n" type="button" :aria-pressed="values[field.key]===String(n)" @click="update(field.key,String(n))">{{n}}</button></div>
       <textarea v-if="field.type==='lines'" :id="`social-${product.id}-${field.key}`" :value="values[field.key] || ''" rows="6" :maxlength="field.max_length" :placeholder="field.placeholder || $t('每行填寫一項，重複內容會保留')" :aria-invalid="!!errors[field.key]" :aria-describedby="`social-help-${field.key}`" @input="update(field.key,($event.target as HTMLTextAreaElement).value)" />
       <select v-else-if="field.type==='enum'" :id="`social-${product.id}-${field.key}`" :value="values[field.key] || ''" :aria-invalid="!!errors[field.key]" @change="update(field.key,($event.target as HTMLSelectElement).value)"><option value="">{{ $t('請選擇') }}</option><option v-for="option in field.options" :key="option" :value="option">{{option}}</option></select>
       <input v-else :id="`social-${product.id}-${field.key}`" :type="field.type==='integer'?'number':field.type==='url'?'url':'text'" :value="values[field.key] || ''" :min="field.min" :max="field.max" :step="field.type==='integer'?'any':undefined" :maxlength="field.max_length" :placeholder="field.placeholder || (field.type==='date'?'DD/MM/YYYY':'')" :aria-invalid="!!errors[field.key]" :aria-describedby="`social-help-${field.key}`" @input="update(field.key,($event.target as HTMLInputElement).value)" />
       <small :id="`social-help-${field.key}`" class="muted"><template v-if="field.type==='integer' && (field.min!==undefined || field.max!==undefined)">{{ $t('允許範圍') }} {{field.min ?? '—'}} – {{field.max ?? '—'}}</template><template v-else-if="field.type==='lines'">{{ $t('已填寫 {0} 行', [lineCount(field.key)]) }}</template><template v-else-if="field.type==='date'">{{ $t('日期格式：日/月/年') }}</template><template v-else-if="!field.required">{{field.key==='posts'?$t('留空表示不限制未來帖子數量'):$t('選填，留空時不向供貨提交此項')}}</template></small>
       <small v-if="errors[field.key]" class="error" role="alert">{{errors[field.key]}}</small>
      </div>
     </fieldset>
     <p v-if="derivedCount!==undefined" class="muted">{{ $t('本次服務數量由填寫內容計算：{0}', [derivedCount]) }}</p>
     <button v-if="!pending" class="btn secondary" type="submit" :disabled="!canBuy">{{busy?$t('確認中…'):$t('確認總價')}}</button>
    </form>
    <div v-if="quote || pending" class="social-price" aria-live="polite"><span>{{ $t('本次總價') }}</span><strong>{{formatMoney(pending?.amount_cents ?? quote?.amount_cents ?? 0)}}</strong><small class="muted">{{pending?$t('已保存原購買請求，重試將繼續同一訂單'):expired?$t('報價已過期，請重新確認價格'):$t('此總價已包含本次填寫的服務配置')}}</small><button class="btn" :disabled="busy || (!pending && expired)" @click="purchase">{{busy?$t('處理中…'):pending?$t('繼續原訂單'):$t('餘額付款')}}</button><router-link v-if="pending?.order_no" :to="`/order/${pending.order_no}`">{{ $t('查看原訂單') }}</router-link></div>
   </template>
   <p v-if="error" class="error" role="alert">{{uiText(error)}}</p><button v-if="loggedIn && !schema && !loading" class="btn secondary" @click="load">{{ $t('重新讀取服務規則') }}</button>
  </div>
 </section>
</template>
<style scoped>
.social-product{display:grid;grid-template-columns:minmax(0,.8fr) minmax(0,1.2fr);gap:24px;align-items:start}.social-summary,.social-form{padding:24px;min-width:0}.social-eyebrow{color:var(--zc-primary,#2563eb);font-size:13px;margin:0 0 8px}.social-summary h1{font-size:28px;line-height:1.35;margin:0 0 16px;overflow-wrap:anywhere}.social-summary p{line-height:1.7}.social-support{display:flex;flex-wrap:wrap;gap:8px;padding:16px 0}.social-support span{border:1px solid var(--border-color,#ddd);border-radius:6px;padding:6px 10px;font-size:13px}.social-form h2{font-size:20px;margin:0 0 20px}fieldset{border:0;padding:0;margin:0;min-width:0}.social-field{display:grid;gap:8px;margin:0 0 20px}.social-field label{font-weight:600;line-height:1.5}.social-field label span{font-weight:400;font-size:13px}.social-field input,.social-field textarea,.social-field select{box-sizing:border-box;min-height:44px;width:100%;border:1px solid var(--border-color,#cbd5e1);border-radius:8px;background:var(--color-bg,#fff);color:inherit;font:inherit;padding:10px 12px}.social-field textarea{resize:vertical;line-height:1.6}.social-field [aria-invalid=true]{border-color:var(--color-danger,#b42318)}.social-field small{line-height:1.5}.social-presets{display:flex;gap:8px;align-items:center;flex-wrap:wrap;font-size:13px}.social-presets button{min-height:44px;padding:8px 12px;border:1px solid var(--border-color,#cbd5e1);border-radius:8px;color:inherit;background:transparent}.social-presets button[aria-pressed=true]{border-color:var(--zc-primary,#2563eb);color:var(--zc-primary,#2563eb)}.social-price{margin-top:24px;padding-top:20px;border-top:1px solid var(--border-color,#ddd);display:grid;gap:12px}.social-price strong{font-size:28px;font-variant-numeric:tabular-nums}.social-price .btn{min-height:46px}.social-unavailable{line-height:1.6}.error{overflow-wrap:anywhere}@media(max-width:760px){.social-product{grid-template-columns:1fr;gap:16px}.social-summary,.social-form{padding:20px}.social-summary h1{font-size:24px}}
</style>
