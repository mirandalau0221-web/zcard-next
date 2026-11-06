<script setup lang="ts">
// 渠道管理：上游貨源連接 / 供貨賬號（下游客戶，含 dujiao/acg 兼容賬號）/ 採購單。
// 權限：supply:read（貨源）、supplier:read（供貨賬號）、procurement:read（採購單）。
// 支持 ?tab= 深鏈（首頁待辦「待審對接申請」直達審核視圖）。
import { ref, defineAsyncComponent, watch } from "vue";
import { useRoute } from "vue-router";
import { NCard, NTabs, NTabPane } from "naive-ui";
import { checkAuth } from "@/directives";
import ConnectionsTab from "./components/connections-tab.vue";
const SupplierAccountsTab = defineAsyncComponent(() => import("./components/supplier-accounts-tab.vue"));
const SMSTab = defineAsyncComponent(() => import("./components/sms-tab.vue"));
const ProcurementTab = defineAsyncComponent(() => import("./components/procurement-tab.vue"));

defineOptions({ name: "ChannelManagement" });

const route = useRoute();
const visibleTabs = [
  { key: "connections", auth: "supply:read" },
  { key: "suppliers", auth: "supplier:read" },
  { key: "procurement", auth: "procurement:read" },
 {key:"sms",auth:"procurement:read"},
].filter((t) => checkAuth(t.auth));

const activeTab = ref<string>(
  visibleTabs.some((t) => t.key === route.query.tab) ? String(route.query.tab) : (visibleTabs[0]?.key || "connections"),
);
watch(() => route.query.tab, tab => { if (visibleTabs.some(t => t.key === tab)) activeTab.value = String(tab); });
</script>

<template>
  <div class="min-h-500px">
    <NCard title="渠道管理">
      <NTabs v-model:value="activeTab" type="line">
        <NTabPane v-if="checkAuth('supply:read')" name="connections" tab="貨源渠道">
          <ConnectionsTab />
        </NTabPane>
        <NTabPane v-if="checkAuth('supplier:read')" display-directive="show:lazy" name="suppliers" tab="供貨賬號">
          <SupplierAccountsTab />
        </NTabPane>
        <NTabPane v-if="checkAuth('procurement:read')" display-directive="show:lazy" name="procurement" tab="採購單">
          <ProcurementTab />
        </NTabPane>
      <NTabPane v-if="checkAuth('procurement:read')" display-directive="show:lazy" name="sms" tab="接碼任務"><SMSTab /></NTabPane>
      </NTabs>
    </NCard>
  </div>
</template>
