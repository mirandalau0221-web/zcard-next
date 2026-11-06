<script setup lang="ts">
import AccountSecurity from "@/components/security/account-security.vue";
import { ref, reactive, computed, onMounted, onBeforeUnmount } from "vue";
import { useRoute, onBeforeRouteLeave } from "vue-router";
import { useI18n } from "vue-i18n";
import { fetchSettings, updateSettings, listCurrencies, fetchTemplates } from "@/service/api";
import type { TemplateItem } from "@/service/api";
import { NCheckbox, NCheckboxGroup, NRadioButton, NRadioGroup, NSpace, NTabs as OuterTabs, NTabPane as OuterTabPane } from "naive-ui";
import { checkAuth } from "@/directives";
import { resolveMediaUrl } from "@/utils/media";
import { initCurrency } from "@/utils/money";
import EmailTest from "./components/email-test.vue";
import TelegramSettings from "./components/telegram-settings.vue";
import CurrencyTab from "./components/currency-tab.vue";
import TopButtonField from "./components/top-button-field.vue";
import GiftTiersField from "./components/gift-tiers-field.vue";
import LinkListField from "./components/link-list-field.vue";
import AuditTab from "./components/audit-tab.vue";
import UpdateTab from "./components/update-tab.vue";
import ThemeSettingsModal from "./components/theme-settings-modal.vue";
import ThemePickerModal from "./components/theme-picker-modal.vue";

defineOptions({ name: "SettingsManagement" });

const route = useRoute();
// 外層 tab 受控：支持 ?tab=update 直達（header 更新徽標跳轉用）
const outerTab = ref((route.query.tab as string) || "settings");

const { te, t } = useI18n();
const loading = ref(false);
const activeGroup = ref("site");
const items = ref<any[]>([]);
const saving = ref(false);
const announcementEditor = ref<any | null>(null);
// 已修改鍵集合（"group.key"）；驅動底部保存按鈕可用態與批量提交。
const dirtyKeys = ref(new Set<string>());
const telegramDirty = ref(false);
const telegramSaving = ref(false);
const hasDirty = () => dirtyKeys.value.size > 0 || telegramDirty.value;
let switching = false;
async function switchGroup(group: string) {
  if (switching || loading.value || saving.value || telegramSaving.value || group === activeGroup.value) return;
  switching = true;
  try {
    if (hasDirty() && !(await confirmDiscard())) return;
    telegramDirty.value = false;
    activeGroup.value = group;
    await loadSettings();
    if (group === "i18n") await loadCurrencies();
  } finally { switching = false; }
}
async function switchOuter(tab: string) {
  if (switching || loading.value || saving.value || telegramSaving.value || tab === outerTab.value) return;
  switching = true;
  try {
    if (hasDirty()) {
      if (!(await confirmDiscard())) return;
      telegramDirty.value = false;
      await loadSettings();
    }
    outerTab.value = tab;
  } finally { switching = false; }
}
function beforeUnload(event: BeforeUnloadEvent) {
  if (hasDirty()) { event.preventDefault(); event.returnValue = ''; }
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload));
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload));
onBeforeRouteLeave(async () => !saving.value && !telegramSaving.value && (!hasDirty() || await confirmDiscard()));
// 貨幣選擇僅列啓用貨幣；讀取失敗保持下拉並提供重試。
const currencyOptions = ref<{ label: string; value: string }[]>([]);
const currenciesLoading = ref(false);
const currencyLoadError = ref("");
const currencyVersion = ref(0);
const localeOptions = [
  { label: "繁體中文", value: "zh_TW" },
  { label: "English", value: "en" },
];
const enabledLocaleOptions = computed(() => {
  const enabled = getVal(items.value.find((it) => it.group === "i18n" && it.key === "enabled_locales"));
  return localeOptions.filter((option) => Array.isArray(enabled) && enabled.includes(option.value));
});
function setEnabledLocales(item: any, enabled: string[]) {
  if (!enabled.length) {
    window.$message?.warning("至少啓用一種語言");
    return;
  }
  setVal(item, enabled);
  const defaultItem = items.value.find((it) => it.group === "i18n" && it.key === "default_locale");
  if (defaultItem && !enabled.includes(String(getVal(defaultItem)))) setVal(defaultItem, enabled[0]);
}

// 可用模板清單（template 組主題彈窗數據源）；無權限/失敗時為空 → 彈窗顯示安裝入口。
const templates = ref<TemplateItem[]>([]);

const groups = [
  { key: "site", label: "站點基礎" },
  { key: "template", label: "模板" },
  { key: "promo", label: "推薦位" },
  { key: "footer", label: "頁腳配置" },
  { key: "trade", label: "交易" },
  { key: "ticket", label: "工單 / TG通知" },
  { key: "security", label: "安全" },
  { key: "ops", label: "運維" },
  { key: "recharge", label: "充值" },
  { key: "supplier_recharge", label: "供貨充值" },
  { key: "points", label: "積分" },
  { key: "withdraw", label: "提現" },
  { key: "affiliate", label: "分銷設置" },
  { key: "supply", label: "貨源" },
  { key: "notify", label: "郵件短信" },
  { key: "service", label: "客户代碼" },
  { key: "i18n", label: "語言貨幣" },
];

// ── 長文本/JSON 類設置鍵：渲染 textarea（JSON 鍵格式化展示 + 解析校驗）──
const TEXTAREA_KEYS: Record<string, string[]> = {
  service: ["widget_script", "stats_script"],
  site: ["robots_custom"],
  footer: ["about", "contact"],
  notify: ["sms_template_register", "sms_template_reset"],
};

// ── 結構化鏈接列表鍵：[{text,url}] / [{icon,url}]（LinkListField 行編輯，杜絕手寫 JSON）──
const LINK_LIST_KEYS: Record<string, { icon?: boolean; textPlaceholder?: string; urlPlaceholder?: string; max?: number }> = {
  "footer.nav": { textPlaceholder: "導航文字", urlPlaceholder: "跳轉鏈接 https://… 或站內路徑 /products", max: 8 },
  "footer.social": { icon: true, urlPlaceholder: "主頁鏈接 https://…", max: 8 },
  "promo.nav_recommend": { textPlaceholder: "推薦文字", urlPlaceholder: "跳轉鏈接 https://… 或站內路徑 /products", max: 3 },
};

function linkListOf(item: any) {
  return LINK_LIST_KEYS[`${item.group}.${item.key}`];
}

// ── 多選類設置鍵：渲染 checkbox 勾選（數組值；其餘 options 鍵為單選）──
const MULTI_KEYS: Record<string, string[]> = {
  security: ["register_method"],
};

// ── 輸入框佔位提示（大廠模式：標籤保持簡短，填寫説明放進框內 placeholder）──
const INPUT_PLACEHOLDERS: Record<string, Record<string, string>> = {
  notify: {
    sms_sign: "阿里雲/騰訊雲填簽名名稱；七牛填簽名 ID",
    sms_sdk_app_id: "騰訊雲必填，其餘通道忽略",
    sms_template_code: "阿里雲/騰訊雲/七牛均需填寫",
    smtp_host: "如 smtp.example.com（不含 https:// 或端口）",
    smtp_user: "郵件服務器的登錄賬號，通常為完整郵箱地址",
    smtp_from: "如 hello@example.com；留空使用 SMTP 用户名",
  },
  footer: {
    about: "頁腳關於我們文案（留空不顯示該欄）",
    contact: "如 郵箱 support@example.com · QQ 羣 123456",
    icp: "如 京ICP備2026000000號-1（留空不顯示）",
    agreement: "用户協議內容或指向文章的 slug",
  },
};

function smtpHelpOf(item: any): string | undefined {
  if (item.group !== "notify") return undefined;
  if (item.key === "smtp_security") {
    const mode = getVal(item);
    if (mode === "plain") return "普通 SMTP 不加密，賬號密碼也可能明文傳輸；適用於明確要求此方式的可信網絡或中繼。常見端口 25，請以服務商配置為準。";
    if (mode === "tls") return "連接建立後立即進行 TLS 握手（支持 TLS 1.2 / 1.3），常見端口 465；也可使用服務商指定的其他端口。";
    if (mode === "starttls") return "先建立 SMTP 連接，再強制升級為 TLS；服務器不支持升級時會報錯。常見端口 587 或 25。";
    return "自動模式：465 使用 SSL/TLS，其他端口在服務器支持時升級為 STARTTLS。需要確定的連接行為時，請明確選擇相應方式。";
  }
  if (item.key === "smtp_auth") return "自動選擇服務器提供的 PLAIN、LOGIN 或 CRAM-MD5。免認證中繼請選擇「無需認證」；部分服務商要求填寫 SMTP 專用授權碼。";
  if (item.key === "smtp_tls_verify") return getVal(item)
    ? "校驗郵件服務器證書的可信鏈、域名和有效期。自簽名或證書鏈缺失會在測試發送中顯示原因。"
    : "已關閉證書校驗：TLS 仍加密傳輸，但不會驗證服務器身份。僅用於你信任的自建服務；普通 SMTP 不使用此選項。";
  return undefined;
}

function inputPlaceholderOf(item: any) {
  return INPUT_PLACEHOLDERS[item.group]?.[item.key];
}

function isTextareaKey(item: any) {
  if (TEXTAREA_KEYS[item.group]?.includes(item.key)) return true;
  // 公告文本類型：多行編輯（text/image/carousel 三態之一）
  return item.group === "ops" && item.key === "announcement" && announcementType() === "text";
}

function isMultiKey(item: any) {
  return !!MULTI_KEYS[item.group]?.includes(item.key);
}

/** JSON 對象/數組字段 → 格式化文本（供 textarea 編輯） */
function textareaValueOf(item: any) {
  const v = getVal(item);
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v ?? "");
  }
}

/** textarea 變更 → 寫入（JSON 鍵嘗試解析，解析失敗按原文本存儲） */
function setTextareaValue(item: any, text: string) {
  if (item.group === 'ops' && item.key === 'announcement') {
    setVal(item, text);
    return;
  }
  if (isTextareaKey(item)) {
    try {
      setVal(item, JSON.parse(text));
      return;
    } catch { /* 非 JSON 文本：按字符串存 */ }
  }
  setVal(item, text);
}

/** textarea 佔位提示（客服/短信模板配置專用） */
function textareaPlaceholderOf(item: any) {
  if (item.key === "widget_script") return "粘貼 Chatwoot/Crisp 等第三方客服完整嵌入代碼（含 <script> 標籤）——前台右下角懸浮球";
  if (item.key === "stats_script") return "粘貼百度統計/Google Analytics/51la 等統計代碼（含 <script> 標籤）——前台頁面最底部注入";
  if (item.key === "robots_custom") return "追加到 robots.txt 的規則（每行一條，如 Disallow: /member）；默認已放行全站並指向 sitemap";
  if (item.group === "ops" && item.key === "announcement") return "支持 Markdown：# 標題、**加粗**、列表、鏈接、圖片及表格；留空則回落公告文章";
  if (item.key === "sms_template_register" || item.key === "sms_template_reset") {
    return "短信模板內容（需與短信服務商控制枱的模板一致）；變量：{code} 驗證碼 {minutes} 有效分鐘 {site} 站點名";
  }
  return undefined;
}

// 顯示名分層解析：前端語言包（當前語言）→ 後端 label（中文兜底）→ key 本身。
function labelOf(item: any) {
  const k = `settings.${item.group}.${item.key}`;
  return te(k) ? t(k) : (item.label || item.key);
}

// ── 圖片類設置鍵：走素材庫選擇（MediaField 預覽 + 彈窗選擇/上傳）──
const IMAGE_KEYS: Record<string, string[]> = {
  site: ["logo"],
  ops: ["announcement"], // 僅 announcement_type 為 image/carousel 時是圖片
};

/** 公告類型（ops.announcement_type；缺省 text） */
function announcementType(): string {
  const typeItem = items.value.find((x) => x.group === "ops" && x.key === "announcement_type");
  const t = typeItem ? getVal(typeItem) : "text";
  return typeof t === "string" ? t : "text";
}

/** carousel 公告：多圖輪播（數組值入庫，區別於單圖字符串） */
function isCarouselKey(item: any) {
  return item.group === "ops" && item.key === "announcement" && announcementType() === "carousel";
}

function isImageKey(item: any) {
  if (!IMAGE_KEYS[item.group]?.includes(item.key)) return false;
  if (item.group === "ops" && item.key === "announcement") {
    return announcementType() === "image" || announcementType() === "carousel";
  }
  return true;
}

function imageValueOf(item: any) {
  const v = getVal(item);
  if (Array.isArray(v)) return v.map(String); // carousel 多圖數組
  return typeof v === "string" && v ? [v] : [];
}

function setImageValue(item: any, urls: string[]) {
  setVal(item, isCarouselKey(item) ? urls : urls[0] ?? "");
}

// 這些外觀項統一由主題自定義編輯；舊設置保留給未發佈主題和歷史客户端兼容。
const THEME_APPEARANCE_KEYS = new Set([
  "bg_image", "bg_image_mobile", "category_nav_style", "default_view",
  "per_row", "per_page", "sort_by", "show_stock", "show_sales",
]);

// ── 模板選擇（WP 主題式：模板組 pc/mobile_template → 彈窗選擇）──
const TEMPLATE_KEYS: Record<string, string[]> = {
  template: ["pc_template"],
};

function isTemplateKey(item: any) {
  return !!TEMPLATE_KEYS[item.group]?.includes(item.key);
}

// 當前模板展示名（templates 清單匹配；未收錄回顯原始值）
function currentTemplateName(item: any) {
  const v = getVal(item);
  const hit = templates.value.find((t: any) => t.key === v);
  return hit ? hit.name : String(v ?? "");
}

// 主題選擇彈窗狀態（目標字段 + 顯隱）
const themeOptions = reactive({show:false,key:'classic'});
const themePicker = reactive<{ show: boolean; item: any }>({ show: false, item: null });
function openThemePicker(item: any) {
  themePicker.item = item;
  themePicker.show = true;
}
function onThemeSelect(key: string) {
  // The picker persists activation before emitting; preserve other unsaved settings.
  if (!themePicker.item) return;
  themePicker.item.value_json = JSON.stringify(key);
  dirtyKeys.value.delete(`${themePicker.item.group}.${themePicker.item.key}`);
}

async function loadTemplates() {
  try {
    const { data, error } = await fetchTemplates();
    if (!error && (data as any)?.templates?.length) {
      templates.value = (data as any).templates;
    }
  } catch {
    // 無 settings:read 權限或接口異常：保持空列表，渲染回退文本輸入
  }
}

function getVal(item: any) {
  if (!item) return undefined;
  try {
    return JSON.parse(item.value_json);
  } catch {
    return item.value_json;
  }
}

/** 多選鍵取值（數組；舊單值字符串兼容為單元素數組） */
function multiValueOf(item: any) {
  const v = getVal(item);
  if (Array.isArray(v)) return v.map(String);
  return typeof v === "string" && v ? [v] : [];
}

/** 單選鍵取值（數組意外值取首元素——防舊數據污染渲染） */
function radioValueOf(item: any) {
  const v = getVal(item);
  if (Array.isArray(v)) return String(v[0] ?? "");
  return String(v ?? "");
}

function setVal(item: any, val: any) {
  item.value_json = JSON.stringify(val);
  dirtyKeys.value.add(`${item.group}.${item.key}`);
}

async function loadSettings() {
  loading.value = true;
  items.value = [];
  dirtyKeys.value.clear();
  try {
    const { data, error } = await fetchSettings(activeGroup.value);
    if (!error && data) {
      items.value = ((data as any).items || []).filter(
        (item: any) => (item.group !== "template" || !THEME_APPEARANCE_KEYS.has(item.key)) &&
          (item.group !== "notify" || (item.key !== "smtp" && item.key !== "telegram" && !item.key.startsWith("telegram_"))),
      );
      if (activeGroup.value === "notify") {
        const order = ["smtp_host", "smtp_port", "smtp_security", "smtp_auth", "smtp_user", "smtp_password", "smtp_from", "smtp_name", "smtp_tls_verify"];
        const rank = (key: string) => order.includes(key) ? order.indexOf(key) : order.length;
        items.value.sort((a, b) => rank(a.key) - rank(b.key));
      }
    }
  } finally {
    loading.value = false;
  }
}

// 丟棄未保存修改的確認（naive dialog 回調式 → Promise）。
function confirmDiscard(): Promise<boolean> {
  return new Promise((resolve) => {
    window.$dialog?.warning({
      title: "有未保存的修改",
      content: "切換分組將丟棄當前未保存的修改，確定繼續？",
      positiveText: "繼續切換",
      negativeText: "取消",
      onPositiveClick: () => resolve(true),
      onNegativeClick: () => resolve(false),
      onClose: () => resolve(false),
      onMaskClick: () => resolve(false),
    });
  });
}

async function loadCurrencies() {
  currenciesLoading.value = true;
  currencyLoadError.value = "";
  currencyOptions.value = [];
  try {
    const { data, error } = await listCurrencies();
    if (error || !(data as any)?.currencies) {
      currencyLoadError.value = "貨幣列表加載失敗，請檢查讀取權限後重試";
      return;
    }
    currencyOptions.value = ((data as any).currencies as any[])
      .filter((c: any) => c.enabled)
      .map((c: any) => ({ label: `${c.code}（${c.symbol ?? ""}）`, value: c.code }));
    if (!currencyOptions.value.length) currencyLoadError.value = "暫無啓用貨幣，請先在「貨幣」頁創建並啓用";
  } catch {
    currencyLoadError.value = "貨幣列表加載失敗，請重試";
  } finally {
    currenciesLoading.value = false;
  }
}

async function currenciesUpdated() {
  await loadCurrencies();
  if (activeGroup.value === "i18n") await loadSettings();
}

// 表單級保存：一次提交本組全部已修改項（後端單事務原子寫入）。
async function saveAll() {
  if (!dirtyKeys.value.size) return;
  saving.value = true;
  try {
    const pending = items.value
      .filter((it) => dirtyKeys.value.has(`${it.group}.${it.key}`))
      .map((it) => ({ group: it.group, key: it.key, value_json: it.value_json }));
    const { data, error } = await updateSettings(pending);
    if (!error) {
      window.$message?.success("設置已保存");
      dirtyKeys.value.clear();
      if (pending.some((it) => it.group === "i18n" && it.key === "base_currency")) {
        await Promise.all([initCurrency(true), loadCurrencies()]);
        currencyVersion.value += 1;
      }
      if (data?.admin_base_path) window.location.replace(`${data.admin_base_path}/settings`);
    }
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  loadSettings();
  loadCurrencies();
  loadTemplates();
});
</script>

<template>
  <div class="min-h-500px">
    <NCard title="系統設置">
      <OuterTabs :value="outerTab" type="line" @update:value="switchOuter">
        <OuterTabPane name="settings" tab="參數設置">
          <NTabs :value="activeGroup" type="line" @update:value="switchGroup">
            <NTabPane v-for="g in groups" :key="g.key" :name="g.key" :tab="g.label" />
          </NTabs>

          <div v-if="loading" class="py-40px text-center">
            <NSpin size="large" />
          </div>

          <template v-else>
            <AccountSecurity v-if="activeGroup === 'security'" />
            <NAlert v-if="activeGroup === 'notify'" type="info" class="mb-16px max-w-760px" title="郵件連接與域名簽名">
              按郵件服務商提供的方式選擇普通 SMTP、SSL/TLS 或 STARTTLS，再保存配置並點擊「測試郵件發送」。
              DKIM 的 Selector 和 TXT 公鑰記錄配置在發件域名的 DNS，簽名私鑰由郵件服務器管理；它與 SMTP 連接方式分別配置。
              測試成功代表郵件服務器已接受，最終到達收件箱還需檢查投遞日誌和 SPF / DKIM / DMARC。
            </NAlert>
            <!-- 頁腳配置分區説明：每個設置項對應前台頁腳的哪個區塊（大廠模式：先給全局地圖再進表單） -->
            <div v-if="activeGroup === 'footer'" class="footer-map mt-12px">
              <div class="text-13px font-600 text-gray-700">頁腳設置 ↔ 前台位置對照</div>
              <div class="mt-6px grid gap-x-24px gap-y-4px text-12px text-gray-500 sm:grid-cols-2">
                <span>· <b>頁腳關於 / 社交鏈接</b> → 品牌欄（Logo 下方文案與圖標）</span>
                <span>· <b>頁腳導航</b> → 快速導航欄（未配置時顯示默認鏈接）</span>
                <span>· <b>聯繫方式</b> → 「聯繫我們」欄（留空整欄隱藏）</span>
                <span>· <b>用户協議 / ICP 備案號</b> → 底部版權行</span>
                <span class="sm:col-span-2 text-gray-400">· 「幫助中心」「會員服務」兩欄為系統內置導航，暫不支持自定義</span>
              </div>
            </div>

            <div v-if="activeGroup === 'template'" class="mt-12px text-13px text-gray-500">
              PC 和手機共用一個響應式主題。背景圖、分類樣式、商品佈局和庫存/銷量顯示統一在「主題自定義」中設置，修改或刪除後點擊「發佈生效」。切換主題請點擊「管理主題」；評價等業務開關仍在系統設置中管理。
            </div>

            <h3 v-if="activeGroup === 'ticket'" class="mt-16px font-600">工單設置</h3>
            <NForm label-placement="left" label-width="172" class="mt-16px max-w-760px settings-form" :class="{ 'settings-form-wide': ['ops', 'recharge', 'supplier_recharge'].includes(activeGroup), 'settings-form-stock': activeGroup === 'supply', 'settings-form-smtp': activeGroup === 'notify', 'settings-form-i18n': activeGroup === 'i18n' }">
              <NFormItem v-for="item in items" :key="item.key" :label="labelOf(item)">
                <div class="flex w-full items-center gap-8px">
                  <template v-if="linkListOf(item)">
                    <LinkListField
                      class="flex-1"
                      v-bind="linkListOf(item)"
                      :value="item.value_json"
                      @update="(v: any) => setVal(item, v)"
                    />
                  </template>
                  <template v-else-if="item.group === 'site' && item.key === 'top_button'">
                    <TopButtonField class="flex-1" :value="item.value_json" @update="(v: any) => setVal(item, v)" />
                  </template>
                  <template v-else-if="(item.group === 'recharge' || item.group === 'supplier_recharge') && item.key === 'gift_tiers'">
                    <GiftTiersField class="flex-1" :supplier="item.group === 'supplier_recharge'" :value="item.value_json" @update="(v: any) => setVal(item, v)" />
                  </template>
                  <template v-else-if="isTemplateKey(item)">
                    <div class="flex w-full flex-wrap items-center gap-8px">
                      <span class="min-w-0 flex-1 truncate text-13px">{{ currentTemplateName(item) }}</span>
                      <NButton size="small" @click="openThemePicker(item)">管理主題</NButton>
                      <NButton size="small" @click="themeOptions.key=getVal(item)||'classic';themeOptions.show=true">主題自定義</NButton>
                    </div>
                  </template>
                  <template v-else-if="isTextareaKey(item)">
                    <div class="w-full">
                    <NInput
                      :value="textareaValueOf(item)"
                      type="textarea"
                      :rows="item.group === 'ops' && item.key === 'announcement' ? 12 : item.key === 'widget_script' || item.key === 'stats_script' ? 6 : 4"
                      class="w-full"
                      :placeholder="textareaPlaceholderOf(item)"
                      @update:value="(v: string) => setTextareaValue(item, v)"
                    />
                    <div v-if="item.group === 'ops' && item.key === 'announcement'" class="mt-6px text-12px text-gray-500">
                      <div class="flex flex-wrap items-center justify-between gap-8px">
                        <span>支持 Markdown 格式。彈窗顯示排版後的內容，首頁公告條顯示文字摘要。</span>
                        <NButton size="small" @click="announcementEditor = item">大窗口編輯</NButton>
                      </div>
                    </div>
                    </div>
                  </template>
                  <template v-else-if="isCarouselKey(item)">
                    <MediaField
                      class="flex-1"
                      multiple
                      :value="imageValueOf(item)"
                      tip="推薦寬 1200 px × 高 400 px（3:1）；默認主題會隨屏幕裁切，文字和主體請居中，多張圖片建議尺寸一致。 可多選並調整順序。"
                      @update:value="(urls: string[]) => setImageValue(item, urls)"
                    />
                  </template>
                  <template v-else-if="isImageKey(item)">
                    <MediaField
                      class="flex-1"
                      :value="imageValueOf(item)"
                      :tip="item.group === 'ops' && item.key === 'announcement' ? '推薦寬 1200 px × 高 400 px（3:1）；默認主題會隨屏幕裁切，文字和主體請居中，多張圖片建議尺寸一致。' : '從素材庫選擇或上傳；選中後即時預覽'"
                      @update:value="(urls: string[]) => setImageValue(item, urls)"
                    />
                  </template>
                  <template v-else-if="isMultiKey(item) && item.options?.length">
                    <!-- 多選枚舉：checkbox 勾選（數組值入庫） -->
                    <NCheckboxGroup
                      :value="multiValueOf(item)"
                      class="flex-1"
                      @update:value="(v: (string | number)[]) => setVal(item, v.map(String))"
                    >
                      <NSpace wrap :size="12">
                        <NCheckbox v-for="o in item.options" :key="o.value" :value="o.value" :label="o.label" />
                      </NSpace>
                    </NCheckboxGroup>
                  </template>
                  <template v-else-if="item.group === 'notify' && ['smtp_security', 'smtp_auth'].includes(item.key)">
                    <NSelect :value="String(getVal(item) ?? 'auto')" class="min-w-0 flex-1" :options="item.options"
                      :input-props="{ 'aria-label': labelOf(item) }" @update:value="(v: string) => setVal(item, v)" />
                  </template>
                  <template v-else-if="item.group === 'i18n' && item.key === 'default_locale'">
                    <NSelect
                      :value="String(getVal(item) ?? '')"
                      class="min-w-0 flex-1"
                      :options="enabledLocaleOptions"
                      :input-props="{ 'aria-label': labelOf(item) }"
                      @update:value="(v: string) => setVal(item, v)"
                    />
                  </template>
                  <template v-else-if="item.group === 'i18n' && item.key === 'enabled_locales'">
                    <div class="min-w-0 flex-1">
                      <NSelect
                        :value="multiValueOf(item)"
                        multiple
                        :options="localeOptions"
                        :input-props="{ 'aria-label': labelOf(item) }"
                        @update:value="(v: string[]) => setEnabledLocales(item, v)"
                      />
                      <p class="mt-6px text-12px text-gray-500">啓用兩種語言後，默認主題顯示語言切換器；默認語言用於首次訪問。</p>
                    </div>
                  </template>
                  <template v-else-if="item.group === 'i18n' && (item.key === 'base_currency' || item.key === 'display_currency')">
                    <div class="min-w-0 flex-1">
                      <NSelect
                        :value="String(getVal(item) ?? '')"
                        filterable
                        :loading="currenciesLoading"
                        :disabled="currenciesLoading || !!currencyLoadError"
                        :input-props="{ 'aria-label': labelOf(item) }"
                        :options="item.key === 'display_currency' ? [{ label: '跟隨基礎貨幣（結算幣）', value: '' }, ...currencyOptions] : currencyOptions"
                        @update:value="(v: string) => setVal(item, v)"
                      />
                      <div v-if="currencyLoadError" role="alert" class="mt-6px flex flex-wrap items-center gap-8px text-12px text-red-600">
                        <span>{{ currencyLoadError }}</span><NButton size="tiny" @click="loadCurrencies">重試</NButton>
                      </div>
                      <p v-if="item.key === 'base_currency'" class="mt-6px text-12px text-gray-500">基礎貨幣也是默認結算貨幣。已有商品、訂單或資金記錄時禁止直接切換，現有價格與餘額需要專門遷移。</p>
                      <p v-else class="mt-6px text-12px text-gray-500">僅換算前台顯示金額，實際結算使用基礎貨幣。</p>
                    </div>
                  </template>
                  <template v-else-if="item.options?.length">
                    <!-- 低基數枚舉：單選按鈕組直接可見，免下拉展開 -->
                    <NRadioGroup
                      :value="radioValueOf(item)"
                      class="flex-1"
                      @update:value="(v: string) => setVal(item, v)"
                    >
                      <NSpace wrap :size="4">
                        <NRadioButton v-for="o in item.options" :key="o.value" :value="o.value">
                          {{ o.label }}
                        </NRadioButton>
                      </NSpace>
                    </NRadioGroup>
                  </template>
                  <template v-else-if="typeof getVal(item) === 'boolean'">
                    <NSwitch :value="getVal(item)" :aria-label="labelOf(item)" @update:value="(v: boolean) => setVal(item, v)" />
                  </template>
                  <template v-else-if="item.group === 'supply' && item.key === 'low_stock_threshold'">
                    <div class="min-w-0 flex-1">
                      <NInputNumber :value="getVal(item)" :min="1" :max="1000000" :precision="0" class="w-full max-w-200px" :input-props="{ 'aria-label': labelOf(item) }" @update:value="(v: number | null) => v !== null && setVal(item, v)" />
                      <p class="mt-6px text-12px text-gray-500">按實際發貨規格判斷。例如設置 5：庫存 0–4 件提醒，5 件不提醒。TG 推送在「工單 / TG通知」單獨開啓；加速檢查在貨源渠道的定時計劃中設置。</p>
                    </div>
                  </template>
                  <template v-else-if="item.group === 'security' && item.key === 'captcha_length'">
                    <div class="min-w-0 flex-1">
                      <NInputNumber :value="getVal(item)" :min="4" :max="8" :precision="0" class="w-full max-w-200px" :input-props="{ 'aria-label': labelOf(item), 'aria-describedby': 'captcha-length-help' }" @update:value="(v: number | null) => v !== null && setVal(item, v)" />
                      <p id="captcha-length-help" class="mt-6px text-12px text-gray-500">4–8 位，默認 4 位數字；保存後新生成的圖形驗證碼生效，適用於前台與後台登錄。</p>
                    </div>
                  </template>
                  <template v-else-if="typeof getVal(item) === 'number'">
                    <!-- 數字類設置：緊湊定寬輸入（大廠模式——數值框不佔滿整行，標籤後短輸入即可） -->
                    <NInputNumber
                      :value="getVal(item)"
                      class="w-200px"
                      @update:value="(v: number | null) => v !== null && setVal(item, v)"
                    />
                  </template>
                  <template v-else-if="item.group === 'site' && item.key === 'admin_path'">
                    <div class="flex-1">
                      <NInput :value="String(getVal(item) ?? '')" placeholder="例如 /manage-8x2k" @update:value="(v: string) => setVal(item, v)" />
                      <p class="mt-6px text-12px text-gray-500">保存後立即跳轉至新入口，原 /admin 入口關閉。留空使用啓動配置中的路徑（默認 /admin）。</p>
                    </div>
                  </template>
                  <template v-else>
                    <NInput
                      :value="String(getVal(item) ?? '')"
                      class="flex-1"
                      :type="item.secret ? 'password' : 'text'"
                      :show-password-on="item.secret ? 'click' : undefined"
                      :placeholder="inputPlaceholderOf(item)"
                      @update:value="(v: string) => setVal(item, v)"
                    />
                  </template>
                </div>
                <template v-if="smtpHelpOf(item)" #feedback>
                  <span class="text-13px leading-6">{{ smtpHelpOf(item) }}</span>
                </template>
              </NFormItem>
            </NForm>

            <div class="mt-24px flex items-center justify-end gap-8px border-t pt-16px max-w-760px">
              <span v-if="dirtyKeys.size" class="text-12px text-gray-400">{{ dirtyKeys.size }} 項修改未保存</span>
              <NButton
                v-auth="'settings:update'"
                type="primary"
                :loading="saving"
                :disabled="!dirtyKeys.size"
                @click="saveAll"
              >
                {{ activeGroup === 'ticket' ? '保存工單設置' : '保存更改' }}
              </NButton>
            </div>
            <EmailTest v-if="activeGroup === 'notify'" :unsaved="dirtyKeys.size > 0 || saving" />
            <TelegramSettings v-if="activeGroup === 'ticket'" @dirty="telegramDirty = $event" @busy="telegramSaving = $event" />
          </template>
        </OuterTabPane>
        <OuterTabPane v-if="checkAuth('settings:currency_read')" name="currency" tab="貨幣">
          <CurrencyTab :key="currencyVersion" @updated="currenciesUpdated" />
        </OuterTabPane>
        <OuterTabPane v-if="checkAuth('audit:read')" name="audit" tab="審計日誌">
          <AuditTab />
        </OuterTabPane>
        <OuterTabPane v-if="checkAuth('system:update')" name="update" tab="系統更新">
          <UpdateTab />
        </OuterTabPane>
      </OuterTabs>
    </NCard>

    <NModal :show="!!announcementEditor" preset="card" title="編輯公告" class="announcement-editor-modal"
      :style="{ width: 'min(1100px, calc(100vw - 32px))' }"
      @update:show="(show: boolean) => { if (!show) announcementEditor = null; }">
      <template v-if="announcementEditor">
        <NInput :value="textareaValueOf(announcementEditor)" type="textarea" class="announcement-editor-input"
          placeholder="輸入 Markdown 公告內容" :input-props="{ style: { height: '60vh', minHeight: '240px' } }"
          @update:value="(v: string) => setTextareaValue(announcementEditor, v)" />
        <div class="mt-12px flex flex-wrap items-center justify-between gap-12px">
          <span class="text-13px text-gray-500">支持 Markdown。完成編輯後，點擊頁面上的「保存更改」發佈。</span>
          <NButton type="primary" @click="announcementEditor = null">完成編輯</NButton>
        </div>
      </template>
    </NModal>

    <ThemeSettingsModal v-model:show="themeOptions.show" :theme-key="themeOptions.key" />
    <!-- 主題選擇彈窗（模板字段點擊「選擇主題」打開） -->
    <ThemePickerModal
      :show="themePicker.show"
      :current="themePicker.item ? getVal(themePicker.item) : undefined"
      @update:show="(v: boolean) => (themePicker.show = v)"
      @select="onThemeSelect"
      @installed="loadTemplates"
    />
  </div>
</template>

<style scoped>
.settings-form-wide { max-width: 1100px; }
@media (max-width: 640px) {
  .settings-form-wide :deep(.n-form-item) { grid-template-columns: minmax(0, 1fr); }
  .settings-form-stock :deep(.n-form-item), .settings-form-smtp :deep(.n-form-item), .settings-form-i18n :deep(.n-form-item) { grid-template-columns: minmax(0, 1fr); grid-template-areas: "label" "blank" "feedback"; grid-template-rows: auto auto auto; }
  .settings-form-stock :deep(.n-form-item-label), .settings-form-smtp :deep(.n-form-item-label), .settings-form-i18n :deep(.n-form-item-label) { display: flex; text-align: left; padding-bottom: 6px; }
  .settings-form-wide :deep(.n-form-item-label), .settings-form-stock :deep(.n-form-item-label), .settings-form-smtp :deep(.n-form-item-label), .settings-form-i18n :deep(.n-form-item-label) { justify-content: flex-start; }
}
/* 頁腳分區説明卡（淺藍信息底，與 naive 信息-alert 同語系） */
.footer-map {
  max-width: 760px;
  padding: 10px 14px;
  border: 1px solid #bfdbfe;
  border-radius: 8px;
  background: #eff6ff;
}

/* 長標籤（如「單 IP 待付款訂單上限」）換行時保持舒適行高並頂部對齊控件，
   避免默認垂直居中導致的多行標籤上下懸空 */
.settings-form :deep(.n-form-item-label) {
  white-space: normal;
  line-height: 1.5;
  padding-top: 9px;
  align-items: flex-start;
}
.settings-form :deep(.n-form-item-label__text) {
  white-space: normal;
}
</style>
