<script setup lang="ts">
import { useMediaQuery } from "@vueuse/core";
const narrowPaymentForm = useMediaQuery("(max-width: 639px)");
import SponsorSlots from "@/components/common/sponsor-slots.vue";
import UsageSwitches from "./components/usage-switches.vue";
type Usage = { allow_purchase?: boolean; allow_member_recharge?: boolean; allow_supply_recharge?: boolean };
// 支付渠道管理（ 重寫）——大廠交互：
// 卡片網格（品牌徽標/狀態 Tag/已配置判定）+ 勾選式添加（批量接入 + 引導配置）
// + schema 驅動配置彈窗（敏感字段留空不修改、回調地址一鍵複製、手續費區）。
// 設計參照：1.x sysadmin 卡片式 + Filament 配置彈窗（回調地址展示先例）。
import { useRoute } from "vue-router";
import { ref, reactive, computed, onMounted, h, watch } from "vue";
import {
  NButton,
  NPopconfirm,
  NSwitch,
  NModal,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NInputNumber,
  NRadioGroup,
  NRadio,
  NAlert,
  NEmpty,
  NSpin,
  NDivider,
  NTag,
  NCheckbox,
  useMessage,
} from "naive-ui";
import { fetchChannels, fetchDrivers, fetchFieldOptions, createChannel, updateChannel, deleteChannel } from "@/service/api";
import PaymentsTab from "./components/payments-tab.vue";
import MediaField from "@/components/common/media-picker/media-field.vue";
import { checkAuth } from "@/directives";
import { currencyUnit } from "@/utils/money";

defineOptions({ name: "PaymentChannelManagement" });
const route = useRoute();
const activeTab = ref(route.query.tab === "payments" && checkAuth("payment:read_detail") ? "payments" : "channels");
watch(() => route.query.tab, tab => { activeTab.value = tab === "payments" && checkAuth("payment:read_detail") ? "payments" : "channels"; });

const message = useMessage();

interface ConfigFieldSchema {
  key: string;
  label: string;
  type: string;
  required: boolean;
  placeholder: string;
  help: string;
  sensitive: boolean;
  dynamic: boolean;
  multiple: boolean;
  options: { label: string; value: string }[];
  default: string;
}
interface DriverMeta {
  code: string;
  name: string;
  icon: string;
  description: string;
  fields: ConfigFieldSchema[];
}
interface Recommendation { recommended?: boolean; recommend_label?: string; recommend_description?: string; }
interface ChannelRow extends Usage, Recommendation {
  sort?: number;
  fee_bearer?: string;
  id: number;
  name: string;
  code: string;
  driver: string;
  config_json: string;
  configured_fields: string[];
  callback_url: string;
  enabled: boolean;
  fee: number;
  fee_type: string;
  icon?: string;
  methods_json?: string;
}

// 支付方式（收銀台顧客看到的選項；params=網關路由參數）
interface MethodRow extends Usage, Recommendation {
  code: string;
  name: string;
  icon: string;
  iconArr: string[]; // 編輯態（MediaField 數組綁定；保存收成 icon）
  enabled: boolean;
  params: Record<string, string>;
}

// ── 品牌徽標（品牌色字母徽章；未知驅動回落通用卡標）──
const driverBadges: Record<string, { char: string; bg: string; color?: string }> = {
  alipay: { char: "支", bg: "linear-gradient(135deg,#1677ff,#0e5fd8)", color: "#fff" },
  wechat: { char: "微", bg: "linear-gradient(135deg,#07c160,#06ad56)", color: "#fff" },
  epay: { char: "易", bg: "linear-gradient(135deg,#8b5cf6,#7c3aed)", color: "#fff" },
  xunhupay: { char: "虎", bg: "linear-gradient(135deg,#ea580c,#c2410c)", color: "#fff" },
  upay: { char: "U", bg: "linear-gradient(135deg,#26a17b,#1d8a68)", color: "#fff" },
  bepusdt: { char: "₮", bg: "linear-gradient(135deg,#26a17b,#1d8a68)", color: "#fff" },
  epusdt: { char: "₮", bg: "linear-gradient(135deg,#26a17b,#1d8a68)", color: "#fff" },
  stripe: { char: "S", bg: "linear-gradient(135deg,#635bff,#5851ea)", color: "#fff" },
  paypal: { char: "P", bg: "linear-gradient(135deg,#003087,#012169)", color: "#fff" },
};
const badgeOf = (code: string) =>
  driverBadges[code] || { char: "💳", bg: "linear-gradient(135deg,#64748b,#475569)", color: "#fff" };

const loading = ref(false);
const saving = ref(false);
const channels = ref<ChannelRow[]>([]);
const drivers = ref<DriverMeta[]>([]);

// ── 添加渠道（勾選式批量接入）──
const addVisible = ref(false);
const driversLoading = ref(false);
const adding = ref(false);
const checkedDrivers = reactive<Record<string, boolean>>({});

// ── 配置彈窗 ──
const configVisible = ref(false);
const current = ref<ChannelRow | null>(null);
const currentFields = computed<ConfigFieldSchema[]>(() => {
  if (!current.value) return [];
  const d = drivers.value.find((x) => x.code === current.value?.driver);
  return d?.fields || [];
});
const form = reactive({
  sort: 0,
  name: "",
  enabled: true,
  allow_purchase: true,
  allow_member_recharge: true,
  allow_supply_recharge: true,
  fee_type: "fixed",
  fee: 0,
  fee_bearer: "merchant",
  recommended: false, recommend_label: "", recommend_description: "",
  values: {} as Record<string, any>,
  icon: [] as string[], // 渠道圖標（MediaField 單圖；空=回落內置徽標）
  methods: [] as MethodRow[], // 支付方式列表（聚合網關按方式收銀）
});

// BEpusdt selects networks on its own cashier; GM Pay uses local method rows.
const visibleFields = computed(() => currentFields.value.filter(f => {
  if (current.value?.driver !== "bepusdt") return true;
  const cashier = form.values.checkout_mode === "cashier";
  return (f.key !== "trade_type" || !cashier) && (f.key !== "currencies" || cashier);
}));

// 聚合網關（一個渠道多種支付方式）：epay 協議 type 路由 / USDT 按鏈選擇
const isAggregateDriver = computed(() => ["epay", "epusdt"].includes(current.value?.driver || ""));

// 方式參數選項（易支付協議 type / USDT network——網關參數域，靜態即可）
const EPAY_TYPES = [
  { label: "支付寶", value: "alipay" },
  { label: "微信支付", value: "wxpay" },
  { label: "QQ 錢包", value: "qqpay" },
  { label: "京東支付", value: "jdpay" },
  { label: "銀行", value: "bank" },
];
function selectedValues(value: unknown): string[] {
  return (Array.isArray(value) ? value : typeof value === "string" ? [value] : []).map(String).map(v => v.trim()).filter(Boolean);
}
function uniqueMethodCode(base: string, rows = form.methods): string {
  const used = new Set(rows.map(m => m.code));
  let code = base, suffix = 2;
  while (used.has(code)) code = `${base}-${suffix++}`;
  return code;
}
function cryptoMethod(network: string, token: string, rows = form.methods): MethodRow {
  const label = cryptoNetworks.value.find(o => o.value === network)?.label || network;
  return { code: uniqueMethodCode(`${token.toLowerCase()}-${network}`, rows), name: `${token} · ${label}`, icon: "", iconArr: [], enabled: true, params: { network, token } };
}
async function addMethod() {
  if (current.value?.driver !== "epusdt") {
    form.methods.push({ code: uniqueMethodCode("alipay"), name: "", icon: "", iconArr: [], enabled: true, params: { type: "alipay" } });
    return;
  }
  const epoch = optionsEpoch;
  const network = selectedValues(form.values.network)[0] || cryptoNetworks.value[0]?.value;
  if (!network) { message.warning("請先在 EP 後台啓用收款鏈和錢包，再刷新可用資產"); return; }
  const result = await loadMethodTokens(network);
  if (epoch !== optionsEpoch || !configVisible.value) return;
  const selected = selectedValues(form.values.token);
  const token = result.options.find(o => selected.includes(o.value))?.value || result.options[0]?.value || "";
  form.methods.push(cryptoMethod(network, token));
}
const generatingMethods = ref(false);
async function genDefaultMethods() {
  if (generatingMethods.value) return;
  if (current.value?.driver === "epay") {
    form.methods = [
      { code: "alipay", name: "支付寶", icon: "", iconArr: [], enabled: true, params: { type: "alipay" } },
      { code: "wxpay", name: "微信支付", icon: "", iconArr: [], enabled: true, params: { type: "wxpay" } },
    ];
    return;
  }
  if (current.value?.driver !== "epusdt") return;
  const epoch = optionsEpoch;
  generatingMethods.value = true;
  try {
    await refreshAssetFields();
    const selectedNetworks = selectedValues(form.values.network);
    const networks = selectedNetworks.length ? selectedNetworks : cryptoNetworks.value.map(o => o.value);
    const tokens = selectedValues(form.values.token);
    const rows: MethodRow[] = [];
    for (const network of networks) {
      const result = await loadMethodTokens(network);
      if (epoch !== optionsEpoch || !configVisible.value) return;
      if (dynamicOpts.network?.fallback || result.fallback) {
        message.warning("未能確認 EP 可用資產，請檢查網關後重試；現有支付方式已保留"); return;
      }
      for (const option of result.options) {
        if (!tokens.length || tokens.includes(option.value)) rows.push(cryptoMethod(network, option.value, rows));
      }
    }
    if (!rows.length) { message.warning("所選鏈與代幣沒有可用組合，請檢查 EP 的鏈、代幣和錢包配置"); return; }
    form.methods = rows;
  } finally { generatingMethods.value = false; }
}

// 動態選項（epusdt network/token——以網關 supported_assets 為準；失敗回落靜態 + 提示）
const dynamicOpts = reactive<Record<string, { options: { label: string; value: string }[]; fallback: boolean }>>({});
const dynamicLoading = reactive<Record<string, boolean>>({});
type AssetOptions = { options: { label: string; value: string }[]; fallback: boolean };
const methodTokens = reactive<Record<string, AssetOptions>>({});
const tokenRequests = new Map<string, Promise<AssetOptions>>();
let optionsEpoch = 0;
const fieldRequests: Record<string, number> = {};
const cryptoNetworks = computed(() => {
  const field = currentFields.value.find(f => f.key === "network");
  return field ? fieldOptionsOf(field) : [];
});
async function loadMethodTokens(network: string): Promise<AssetOptions> {
  if (methodTokens[network]) return methodTokens[network];
  const pending = tokenRequests.get(network);
  if (pending) return pending;
  const epoch = optionsEpoch;
  const request = (async () => {
    const { data, error } = await fetchFieldOptions("epusdt", "token", JSON.stringify({ api_url: form.values.api_url || "", network: [network] }));
    const result: AssetOptions = error || !data ? { options: [], fallback: true } : { options: data.options || [], fallback: !!data.fallback };
    if (epoch === optionsEpoch) methodTokens[network] = result;
    return result;
  })();
  tokenRequests.set(network, request);
  try { return await request; }
  finally { if (epoch === optionsEpoch) tokenRequests.delete(network); }
}
async function refreshAssetFields() {
  await Promise.all(currentFields.value.filter(f => f.dynamic).map(loadFieldOptions));
}
function resetAssetOptions() {
  optionsEpoch++;
  for (const target of [dynamicOpts, dynamicLoading, methodTokens]) for (const key of Object.keys(target)) delete target[key];
  tokenRequests.clear();
}
async function refreshAssets() {
  resetAssetOptions();
  await refreshAssetFields();
  if (current.value?.driver === "epusdt") await Promise.all(form.methods.map(m => m.params.network).filter(Boolean).map(loadMethodTokens));
}
watch(() => [configVisible.value, ...form.methods.map(m => m.params.network)], () => {
  if (configVisible.value && current.value?.driver === "epusdt") for (const m of form.methods) if (m.params.network) void loadMethodTokens(m.params.network);
});

/** 加載動態字段選項；partial 取當前表單非敏感值（api_url 等），network 變化聯動 token */
async function loadFieldOptions(f: ConfigFieldSchema) {
  const key = f.key;
  const epoch = optionsEpoch, requestId = (fieldRequests[key] || 0) + 1;
  fieldRequests[key] = requestId;
  dynamicLoading[key] = true;
  const partial: Record<string, string | string[]> = {};
  for (const fld of currentFields.value) {
    if (fld.sensitive) continue;
    const v = form.values[fld.key];
    if (Array.isArray(v)) {
      if (v.length > 0) partial[fld.key] = v;
    } else if (typeof v === "string" && v.trim()) {
      partial[fld.key] = v as string;
    }
  }
  const { data, error } = await fetchFieldOptions(current.value?.driver || "", key, JSON.stringify(partial));
  if (epoch !== optionsEpoch || requestId !== fieldRequests[key]) return;
  if (!error && data) {
    dynamicOpts[key] = { options: data.options || [], fallback: !!data.fallback };
  } else {
    dynamicOpts[key] = { options: f.options || [], fallback: true };
  }
  dynamicLoading[key] = false;
}

/** 字段選項（動態優先，未加載回落 schema 靜態 options） */
function fieldOptionsOf(f: ConfigFieldSchema) {
  if (f.dynamic && dynamicOpts[f.key]) return dynamicOpts[f.key].options;
  return f.options || [];
}

/** 輸入框佔位提示：幫助文字/已配置提醒併入 placeholder（輸入後自然消失，不再佔行） */
function fieldHint(f: ConfigFieldSchema) {
  const parts: string[] = [];
  if (f.sensitive && configuredKeys.value.has(f.key)) parts.push("已配置，留空保持不變");
  if (f.placeholder) parts.push(f.placeholder);
  if (f.help) parts.push(f.help);
  return parts.join(" · ") || undefined;
}

const driverOf = (code: string) => drivers.value.find((d) => d.code === code);
const isConfigured = (ch: ChannelRow) => ch.driver === "wallet" || (ch.configured_fields || []).length > 0;

async function loadList() {
  loading.value = true;
  try {
    const { data, error } = await fetchChannels();
    if (!error && data) channels.value = (data as any).channels || [];
  } finally {
    loading.value = false;
  }
}

async function loadDrivers() {
  const { data, error } = await fetchDrivers();
  if (!error && data) drivers.value = (data as any).drivers || [];
}

// ── 添加渠道：勾選驅動 → 批量創建 → 引導配置第一個 ──
function openAddDialog() {
  addVisible.value = true;
  driversLoading.value = true;
  checkedDrivers && Object.keys(checkedDrivers).forEach((k) => delete checkedDrivers[k]);
  loadDrivers().finally(() => (driversLoading.value = false));
}

const addableDrivers = computed(() =>
  drivers.value.filter((d) => d.code !== "wallet" || !channels.value.some((c) => c.driver === "wallet")),
);

function nextChannelIdentity(driver: DriverMeta, rows: ChannelRow[]) {
  const usedCodes = new Set(rows.map((ch) => ch.code));
  let sequence = 1;
  let code = driver.code;
  while (usedCodes.has(code) || (sequence === 1 && rows.some((ch) => ch.driver === driver.code))) {
    code = `${driver.code}-${++sequence}`;
  }
  return { code, name: sequence === 1 ? driver.name : `${driver.name} ${sequence}` };
}

async function handleAdd() {
  const picked = drivers.value.filter((d) => checkedDrivers[d.code]);
  if (picked.length === 0) {
    message.warning("請至少選擇一個支付渠道");
    return;
  }
  adding.value = true;
  const created: ChannelRow[] = [];
  try {
    for (const d of picked) {
      const identity = nextChannelIdentity(d, [...channels.value, ...created]);
      const { data, error } = await createChannel({
        ...identity,
        sort: Math.min(2147483647, Math.max(0, ...[...channels.value, ...created].map(ch => ch.sort || 0)) + 10),
        driver: d.code,
        config_json: "{}",
        enabled: d.code === "wallet",
        fee: 0,
        fee_type: "fixed",
        // 易支付默認掛支付寶+微信兩方式（配置彈窗可增刪改）
        methods_json:
          d.code === "epay"
            ? JSON.stringify([
                { code: "alipay", name: "支付寶", icon: "", enabled: true, params: { type: "alipay" } },
                { code: "wxpay", name: "微信支付", icon: "", enabled: true, params: { type: "wxpay" } },
              ])
            : "",
      });
      if (!error && data) created.push(data);
    }
  } finally {
    adding.value = false;
  }
  if (created.length === 0) {
    message.error("添加失敗，請重試");
    return;
  }
  addVisible.value = false;
  message.success(`已添加 ${created.length} 個渠道`);
  await loadList();
  // 引導式：自動打開第一個新渠道的配置彈窗補憑據
  openConfig(created[0]);
  form.enabled = true;
}

// ── 配置彈窗：schema 驅動表單 ──
const feeLabel = computed(() => (form.fee_type === "percent" ? "比例（%）" : `固定金額（${currencyUnit()}）`));

function openConfig(ch: ChannelRow) {
  resetAssetOptions();
  current.value = ch;
  form.name = ch.name;
  form.sort = ch.sort || 0;
  form.enabled = ch.enabled;
  form.allow_purchase = ch.allow_purchase !== false;
  form.allow_member_recharge = ch.allow_member_recharge !== false;
  form.allow_supply_recharge = ch.allow_supply_recharge !== false;
  form.fee_type = ch.fee_type || "fixed";
  form.fee_bearer = ch.driver === "wallet" ? "merchant" : ch.fee_bearer || "merchant";
  form.recommended = !!ch.recommended; form.recommend_label = ch.recommend_label || ""; form.recommend_description = ch.recommend_description || "";
  // fee：fixed=分 → 元；percent=萬分比 → 百分比（proto3 零值不輸出——undefined 兜底 0）
  form.fee = ch.driver === "wallet" ? 0 : Number(ch.fee || 0) / 100;
  form.values = {};
  // 脱敏回顯：非敏感字段顯值；敏感字段 **** → 顯示為空（留空=不修改）
  let echo: Record<string, any> = {};
  try {
    echo = JSON.parse(ch.config_json || "{}");
  } catch {
    echo = {};
  }
  for (const f of currentFields.value) {
    const v = echo[f.key] ?? (ch.driver === "upay" && f.key === "trade_types" ? echo.trade_type ?? f.default : ["bepusdt", "upay"].includes(ch.driver) ? f.default : undefined);
    if (f.type === "number" && v !== undefined && v !== "" && Number.isFinite(Number(v))) {
      form.values[f.key] = Number(v);
    } else if (ch.driver === "upay" && f.key === "trade_types") {
      form.values[f.key] = selectedValues(v);
    } else if (Array.isArray(v)) {
      form.values[f.key] = v; // 多選字段回顯
    } else if (ch.driver === "bepusdt" && f.multiple) {
      form.values[f.key] = [];
    } else if (typeof v === "string" && v !== "****" && v !== "") {
      form.values[f.key] = v;
    }
  }
  // 動態選項字段（epusdt network/token）：打開即拉取網關 supported_assets
  for (const f of currentFields.value) {
    if (f.dynamic) loadFieldOptions(f);
  }
  // 圖標與支付方式回填
  form.icon = ch.icon ? [ch.icon] : [];
  try {
    form.methods = (JSON.parse(ch.methods_json || "[]") as MethodRow[]).map((m) => ({
      recommended: !!m.recommended, recommend_label: m.recommend_label || "", recommend_description: m.recommend_description || "",
      code: m.code, name: m.name, icon: m.icon || "", iconArr: m.icon ? [m.icon] : [],
      enabled: m.enabled !== false, params: m.params || {},
      allow_purchase: m.allow_purchase !== false, allow_member_recharge: m.allow_member_recharge !== false, allow_supply_recharge: m.allow_supply_recharge !== false,
    }));
  } catch {
    form.methods = [];
  }
  configVisible.value = true;
}

const configuredKeys = computed(() => new Set(current.value?.configured_fields || []));

function moveMethod(index: number, offset: number) {
  const target = index + offset;
  if (target < 0 || target >= form.methods.length) return;
  const [row] = form.methods.splice(index, 1);
  form.methods.splice(target, 0, row);
}

function handleConfigSave() {
  if (!current.value || saving.value || generatingMethods.value) return;
  // 必填校驗：未配置過的必填字段必須填寫（數組=非空）
  for (const f of currentFields.value) {
    const v = form.values[f.key];
    const filled = Array.isArray(v) ? v.length > 0 : f.type === "number" ? typeof v === "number" && Number.isFinite(v) : !!String(v ?? "").trim();
    if (f.required && (!configuredKeys.value.has(f.key) || (current.value.driver === "upay" && f.key === "trade_types")) && !filled) {
      message.warning(`請填寫「${f.label}」`);
      return;
    }
  }
  // 構造憑據：非空且非掩碼的字段；敏感字段留空=不覆蓋（後端 **** 語義）
  const cfg: Record<string, any> = {};
  for (const f of currentFields.value) {
    const v = form.values[f.key];
    if (f.type === "number") {
      if (typeof v === "number" && Number.isFinite(v)) cfg[f.key] = v;
    } else if (Array.isArray(v)) {
      cfg[f.key] = v; // 空數組也提交，允許清空舊的幣種/網絡限制
    } else {
      const sv = String(v ?? "").trim();
      if (sv && sv !== "****") cfg[f.key] = sv;
    }
  }
  // 支付方式校驗（聚合網關）：code/name 必填且 code 不重複
  if (isAggregateDriver.value) {
    const seen = new Set<string>();
    for (const m of form.methods) {
      if (!m.code.trim() || !m.name.trim()) {
        message.warning("支付方式的標識與名稱必填");
        return;
      }
      if (seen.has(m.code.trim())) {
        message.warning(`支付方式標識「${m.code}」重複`);
        return;
      }
      if (current.value.driver === "epusdt" && m.enabled) {
        const network = (m.params.network || "").trim().toLowerCase();
        const token = (m.params.token || "").trim().toUpperCase();
        if (!network || !token) { message.warning("請選擇每個啓用支付方式的收款鏈和代幣"); return; }
        const assets = methodTokens[network];
        if ((dynamicOpts.network && !dynamicOpts.network.fallback && !cryptoNetworks.value.some(o => o.value === network)) || (assets && !assets.fallback && !assets.options.some(o => o.value === token))) {
          message.warning(`${m.name} 的鏈與代幣當前在 EP 不可用，請啓用對應資產和錢包後刷新`); return;
        }
        m.params.network = network; m.params.token = token;
      }
      seen.add(m.code.trim());
    }
  }
  const payload: Record<string, any> = {
    name: form.name.trim(),
    sort: form.sort ?? 0,
    enabled: form.enabled,
    allow_purchase: form.allow_purchase, allow_member_recharge: form.allow_member_recharge, allow_supply_recharge: form.allow_supply_recharge,
    fee_type: form.fee_type,
    fee_bearer: form.fee_bearer, recommended: form.recommended, recommend_label: form.recommend_label.trim(), recommend_description: form.recommend_description.trim(),
    fee: Math.round(form.fee * (form.fee_type === "percent" ? 100 : 100)),
    icon: form.icon[0] || "",
    methods_json: isAggregateDriver.value
      ? JSON.stringify(form.methods.map((m) => ({
          code: m.code.trim(), name: m.name.trim(), icon: (m.iconArr && m.iconArr[0]) || m.icon || "",
          enabled: m.enabled, params: m.params,
          recommended: !!m.recommended, recommend_label: (m.recommend_label || "").trim(), recommend_description: (m.recommend_description || "").trim(),
          allow_purchase: m.allow_purchase !== false, allow_member_recharge: m.allow_member_recharge !== false, allow_supply_recharge: m.allow_supply_recharge !== false,
        })))
      : "",
  };
  if (Object.keys(cfg).length > 0) payload.config_json = JSON.stringify(cfg);
  saving.value = true;
  updateChannel(current.value.id, payload)
    .then(({ error }) => {
      if (!error) {
        message.success("保存成功，立即生效");
        configVisible.value = false;
        loadList();
      }
    })
    .finally(() => (saving.value = false));
}

// ── 啓用/刪除 ──
async function handleToggle(row: ChannelRow, val: boolean) {
  const { error } = await updateChannel(row.id, { enabled: val });
  if (!error) {
    row.enabled = val;
    message.success(val ? "已啓用" : "已停用");
  }
}

async function handleDelete(row: ChannelRow) {
  const { error } = await deleteChannel(row.id);
  if (!error) {
    message.success("已刪除");
    loadList();
  }
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    message.success("已複製");
  } catch {
    message.error("複製失敗，請手動選擇複製");
  }
}

onMounted(() => {
  loadList();
  loadDrivers();
});
</script>

<template>
  <div class="min-h-500px">
    <!-- 頁頭：標題 + 説明 + 添加入口 -->
    <div class="payment-heading mb-16px">
      <div>
        <div class="text-16px font-600">支付渠道</div>
        <div class="text-12px opacity-60 mt-4px">接入支付渠道並在下方完成憑據配置，保存後立即生效</div>
      </div>
      <SponsorSlots class="payment-sponsors" />
      <NButton class="payment-add" v-auth="'payment:write'" type="primary" @click="openAddDialog">添加渠道</NButton>
    </div>

    <NTabs v-model:value="activeTab" type="line">
      <NTabPane name="channels" tab="渠道管理">
            <!-- 渠道卡片網格 -->
    <NSpin :show="loading">
      <div v-if="channels.length === 0 && !loading">
        <NEmpty description="尚未接入任何支付渠道" style="padding: 48px 0">
          <template #extra>
            <NButton v-auth="'payment:write'" type="primary" @click="openAddDialog">添加第一個支付渠道</NButton>
          </template>
        </NEmpty>
      </div>
      <div
        class="grid gap-16px"
        style="grid-template-columns: repeat(auto-fill, minmax(300px, 1fr))"
      >
        <div
          v-for="ch in channels"
          :key="ch.id"
          class="rounded-12px border border-#e5e7eb dark:border-#333 p-20px flex flex-col gap-12px transition-shadow hover:shadow-md"
        >
          <!-- 頭部：徽標 + 名稱 + 狀態 -->
          <div class="flex items-start gap-12px">
            <div
              class="w-44px h-44px rounded-12px flex items-center justify-center text-20px font-700 shrink-0"
              :style="{ background: badgeOf(ch.driver).bg, color: badgeOf(ch.driver).color }"
            >
              {{ badgeOf(ch.driver).char }}
            </div>
            <div class="flex-1 min-w-0">
              <div class="font-600 truncate">{{ ch.name }}</div><div class="text-12px opacity-60">排序：{{ ch.sort || 0 }}</div>
              <div class="text-12px opacity-70 truncate">{{ ch.driver === "epusdt" ? "GM Pay" : ch.driver === "bepusdt" ? "BEpusdt" : driverOf(ch.driver)?.name || ch.driver }} · {{ ch.code }}</div>
            </div>
            <NSwitch size="small" :value="ch.enabled" @update:value="(v: boolean) => handleToggle(ch, v)" />
          </div>

          <!-- 狀態 Tags：啓用 + 已配置 -->
          <div class="flex gap-8px">
            <NTag size="small" :type="ch.enabled ? 'success' : 'default'" :bordered="false">
              {{ ch.enabled ? "已啓用" : "已停用" }}
            </NTag>
            <NTag size="small" :type="isConfigured(ch) ? 'success' : 'warning'" :bordered="false">
              {{ ch.driver === "wallet" ? "無需憑據" : isConfigured(ch) ? "已配置" : "待配置" }}
            </NTag>
            <NTag v-if="(ch.fee || 0) > 0" size="small" type="info" :bordered="false">
              {{ ch.fee_type === "percent" ? `費率 ${((ch.fee || 0) / 100).toFixed(2)}%` : `手續費 ${((ch.fee || 0) / 100).toFixed(2)} ${currencyUnit()}` }} · {{ ch.fee_bearer === 'user' ? '用户承擔' : '商家承擔' }}
            </NTag>
          </div>

          <div class="flex flex-wrap gap-6px text-12px" aria-label="渠道用途">
            <NTag size="small" :bordered="false" :type="ch.allow_purchase !== false ? 'info' : 'default'">購買{{ ch.allow_purchase !== false ? '已開' : '已關' }}</NTag>
            <NTag v-if="ch.driver !== 'wallet'" size="small" :bordered="false" :type="ch.allow_member_recharge !== false ? 'info' : 'default'">會員充值{{ ch.allow_member_recharge !== false ? '已開' : '已關' }}</NTag>
            <NTag v-if="ch.driver !== 'wallet'" size="small" :bordered="false" :type="ch.allow_supply_recharge !== false ? 'info' : 'default'">供貨充值{{ ch.allow_supply_recharge !== false ? '已開' : '已關' }}</NTag>
          </div>
          <!-- 驅動説明 -->
          <div class="text-12px opacity-60 flex-1">{{ driverOf(ch.driver)?.description || ch.driver }}</div>

          <!-- 操作 -->
          <div class="flex items-center gap-8px pt-4px border-t border-#f1f5f9 dark:border-#333">
            <NButton v-auth="'payment:write'" size="small" type="primary" secondary class="flex-1" @click="openConfig(ch)">
              {{ isConfigured(ch) ? "配置" : "去配置" }}
            </NButton>
            <NPopconfirm :on-positive-click="() => handleDelete(ch)">
              <template #trigger>
                <NButton v-auth="'payment:delete'" size="small" type="error" secondary :disabled="['bepusdt', 'upay'].includes(ch.driver) && ch.enabled" :title="['bepusdt', 'upay'].includes(ch.driver) && ch.enabled ? '請先停用渠道再刪除' : undefined">刪除</NButton>
              </template>
              {{ ['bepusdt', 'upay'].includes(ch.driver) ? (ch.enabled ? '請先停用渠道再刪除。' : '刪除後將從渠道列表移除，歷史支付記錄和到賬通知仍會保留，確定刪除？') : '刪除後該渠道將無法繼續收款，確定刪除？' }}
            </NPopconfirm>
          </div>
        </div>
      </div>
    </NSpin>
      </NTabPane>
      <NTabPane v-if="checkAuth('payment:read_detail')" name="payments" tab="支付單/退款單">
        <PaymentsTab />
      </NTabPane>
    </NTabs>

    <!-- 添加渠道：勾選式批量接入 -->
    <NModal v-model:show="addVisible" preset="card" title="添加支付渠道" style="width: 560px; max-width: 94vw">
      <div class="text-12px opacity-60 mb-12px">同一支付類型可添加多個渠道，分別配置不同上游的網關、商户號和密鑰。新渠道默認停用，請配置後啓用。</div>
      <NSpin :show="driversLoading">
        <div class="flex flex-col gap-8px max-h-360px overflow-auto pr-4px">
          <NCheckbox
            v-for="d in addableDrivers"
            :key="d.code"
            v-model:checked="checkedDrivers[d.code]"
            class="rounded-8px border border-#e5e7eb dark:border-#333 px-12px py-10px"
            :class="{ 'opacity-40': !checkedDrivers[d.code] }"
          >
            <div class="flex items-center gap-10px">
              <div
                class="w-32px h-32px rounded-8px flex items-center justify-center text-14px font-700 shrink-0"
                :style="{ background: badgeOf(d.code).bg, color: badgeOf(d.code).color }"
              >
                {{ badgeOf(d.code).char }}
              </div>
              <div class="min-w-0">
                <div class="text-13px font-500">{{ d.name }}</div>
                <div v-if="channels.some((ch) => ch.driver === d.code)" class="text-12px">已添加 {{ channels.filter((ch) => ch.driver === d.code).length }} 個，可繼續添加獨立配置</div>
                <div class="text-12px opacity-50 truncate">{{ d.description }}</div>
              </div>
            </div>
          </NCheckbox>
          <div v-if="addableDrivers.length === 0" class="text-center text-12px opacity-50 py-16px">
            全部渠道均已接入
          </div>
        </div>
      </NSpin>
      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton @click="addVisible = false">取消</NButton>
          <NButton type="primary" :loading="adding" :disabled="Object.values(checkedDrivers).filter(Boolean).length === 0" @click="handleAdd">
            添加所選（{{ Object.values(checkedDrivers).filter(Boolean).length }}）
          </NButton>
        </div>
      </template>
    </NModal>

    <!-- 配置彈窗：schema 驅動 -->
    <NModal v-model:show="configVisible" preset="card" :title="`配置「${current?.name || ''}」`" style="width: 760px; max-width: 94vw; max-height: 92vh; overflow-y: auto">
      <div class="text-12px opacity-60 mb-16px">填寫該支付通道所需的參數，保存後立即生效</div>

      <NForm :label-placement="narrowPaymentForm ? 'top' : 'left'" label-width="110" class="config-form">
        <NFormItem label="渠道名稱">
          <NInput v-model:value="form.name" placeholder="渠道顯示名稱" />
        </NFormItem>
        <NFormItem label="前端排序">
          <div class="w-full"><NInputNumber v-model:value="form.sort" :min="0" :max="2147483647" :precision="0" /><div class="text-12px mt-4px">數字越小越靠前，同值按渠道創建順序排列</div></div>
        </NFormItem>
        <NFormItem label="渠道圖標">
          <div class="w-full">
            <MediaField v-model:value="form.icon" tip="收銀台渠道/方式圖標（建議 1:1 透明 PNG）；留空使用內置徽標" />
          </div>
        </NFormItem>
        <NFormItem label="啓用">
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>

        <NFormItem label="開放用途">
          <div class="w-full">
            <UsageSwitches :value="form" :wallet="current?.driver === 'wallet'" @change="(key, value) => form[key] = value" />
            <div class="text-12px mt-8px">關閉某項後，該渠道不會出現在對應收銀台，新的支付請求也會被拒絕。已發起的支付仍正常核對到賬。</div>
            <div v-if="isAggregateDriver" class="text-12px mt-4px">下方可進一步限制支付寶、微信等方式。渠道關閉的用途，對所有方式生效。</div>
          </div>
        </NFormItem>
        <!-- 支付方式（聚合網關）：易支付按支付寶/微信分開收、USDT 按鏈選擇 -->
        <template v-if="isAggregateDriver">
          <NDivider title-placement="left" style="margin: 4px 0 16px">
            支付方式（顧客在收銀台看到的選項）
          </NDivider>
          <div class="flex items-center justify-between mb-8px">
            <span class="text-12px opacity-60">
              {{ current?.driver === "epay" ? "同一易支付網關可分開接入支付寶/微信等，顧客按方式下單" : "每個方式指定收款鏈和代幣，顧客在商城收銀台選擇" }}
            </span>
            <div class="flex gap-8px">
              <NButton v-if="current?.driver === 'epusdt'" size="tiny" secondary @click="refreshAssets">刷新可用資產</NButton>
              <NButton size="tiny" secondary :loading="generatingMethods" @click="genDefaultMethods">按模板生成</NButton>
              <NButton size="tiny" type="primary" secondary @click="addMethod">添加方式</NButton>
            </div>
          </div>
          <NAlert v-if="current?.driver === 'epusdt'" type="info" class="mb-12px">
            先在 EP 後台啓用鏈、代幣並配置收款錢包。這裏的支付方式優先於下方渠道默認值；修改默認值不會改寫已有方式。TRX 請選擇 Tron + TRX；Polygon 是鏈名，收款代幣需按 EP 實際支持選擇。
          </NAlert>
          <div class="flex flex-col gap-8px mb-8px">
            <div
              v-for="(m, i) in form.methods"
              :key="i"
              class="rounded-8px border border-#e5e7eb dark:border-#333 p-10px flex flex-wrap items-center gap-8px"
              :class="{ 'opacity-50': !m.enabled }"
            >
              <NSwitch v-model:value="m.enabled" size="small" />
              <NInput v-model:value="m.name" size="small" placeholder="方式名稱（如 支付寶）" style="width: 150px" />
              <NInput v-model:value="m.code" size="small" placeholder="標識（如 alipay）" style="width: 120px" />
              <NSelect
                v-if="current?.driver === 'epay'"
                v-model:value="m.params.type"
                size="small"
                :options="EPAY_TYPES"
                placeholder="網關支付類型"
                style="width: 130px"
              />
              <template v-else>
                <NSelect
                  v-model:value="m.params.network"
                  size="small"
                  :options="cryptoNetworks"
                  :loading="!!dynamicLoading.network"
                  filterable
                  @update:value="() => { delete m.params.token; }"
                  placeholder="收款鏈"
                  style="width: 170px"
                />
                <NSelect v-model:value="m.params.token" size="small" placeholder="收款代幣" style="width: 130px"
                  :options="methodTokens[m.params.network]?.options || []" :loading="!!m.params.network && !methodTokens[m.params.network]"
                  :disabled="!m.params.network" filterable :tag="!!methodTokens[m.params.network]?.fallback" />
                <span v-if="methodTokens[m.params.network]?.fallback" class="text-12px text-orange-600">未確認 EP 可用資產，保存前請核對網關；已有配置保留。</span>
              </template>
              <div style="min-width: 110px">
                <MediaField v-model:value="m.iconArr" />
              </div>
              <NButton size="tiny" :disabled="i === 0" @click="moveMethod(i, -1)">上移</NButton>
              <NButton size="tiny" :disabled="i === form.methods.length - 1" @click="moveMethod(i, 1)">下移</NButton>
              <NButton size="tiny" type="error" quaternary @click="form.methods.splice(i, 1)">刪除</NButton>
              <div class="w-full flex items-center gap-8px flex-wrap">
                <span class="text-12px">推薦此方式</span><NSwitch v-model:value="m.recommended" size="small" />
                <NInput v-if="m.recommended" v-model:value="m.recommend_label" placeholder="推薦標籤，默認推薦" :maxlength="6" style="width: 170px" size="small" />
                <NInput v-if="m.recommended" v-model:value="m.recommend_description" placeholder="推薦説明（選填）" :maxlength="60" style="flex: 1; min-width: 160px" size="small" />
              </div>
              <UsageSwitches :value="m" :parent="form" compact @change="(key, value) => m[key] = value" />
            </div>
            <div v-if="form.methods.length === 0" class="text-12px opacity-50 py-4px">
              尚未配置支付方式——顧客將看到渠道本身（或點「按模板生成」快速創建）
            </div>
          </div>
        </template>

        <template v-if="!isAggregateDriver || !form.methods.length">
          <NDivider title-placement="left">收銀台推薦</NDivider>
          <NFormItem label="推薦此渠道"><NSwitch v-model:value="form.recommended" /></NFormItem>
          <NFormItem v-if="form.recommended" label="推薦標籤"><NInput v-model:value="form.recommend_label" placeholder="默認：推薦" :maxlength="6" show-count /></NFormItem>
          <NFormItem v-if="form.recommended" label="推薦説明"><NInput v-model:value="form.recommend_description" placeholder="選填，如支持 USDT 支付" :maxlength="60" show-count /></NFormItem>
        </template>
        <!-- 手續費 -->
        <NDivider title-placement="left" style="margin: 4px 0 16px">手續費</NDivider>
        <NFormItem label="承擔方">
          <NRadioGroup v-model:value="form.fee_bearer" :disabled="current?.driver === 'wallet'"><NRadio value="merchant">商家承擔</NRadio><NRadio value="user">用户承擔</NRadio></NRadioGroup>
        </NFormItem>
        <p class="text-12px mb-12px opacity-70">{{ current?.driver === 'wallet' ? '餘額支付固定免手續費。' : form.fee_bearer === 'user' ? '在優惠後的訂單金額上加收，按分四捨五入；充值手續費不計入餘額。已創建支付沿用原費用。' : '不增加顧客實付金額，收銀台顯示免手續費。' }}</p>
        <NFormItem label="計費方式">
          <NRadioGroup v-model:value="form.fee_type" :disabled="current?.driver === 'wallet'">
            <NRadio value="fixed">固定金額</NRadio>
            <NRadio value="percent">按比例</NRadio>
          </NRadioGroup>
        </NFormItem>
        <NFormItem :label="feeLabel">
          <NInputNumber
            v-model:value="form.fee"
            :disabled="current?.driver === 'wallet'"
            :min="0"
            :max="form.fee_type === 'percent' ? 100 : 1000000"
            :step="form.fee_type === 'percent' ? 0.1 : 0.01"
            :precision="2"
            style="width: 200px"
          />
          <span class="ml-8px text-12px opacity-50">{{ form.fee_type === "percent" ? "%" : currencyUnit() }}</span>
        </NFormItem>

        <!-- 渠道參數（schema 驅動） -->
        <template v-if="currentFields.length > 0">
          <NDivider title-placement="left" style="margin: 4px 0 16px">渠道參數</NDivider>
          <NFormItem
            v-for="f in visibleFields"
            :key="f.key"
            :label="f.label"
            :required="f.required && !configuredKeys.has(f.key)"
          >
            <template v-if="f.type === 'select'">
              <div class="w-full">
                <NSelect
                  v-model:value="form.values[f.key]"
                  :options="fieldOptionsOf(f)"
                  :multiple="f.multiple"
                  :collapse-tags="f.multiple"
                  :max-tag-count="f.multiple ? 3 : undefined"
                  :loading="!!dynamicLoading[f.key]"
                  :placeholder="f.multiple ? '選擇支持的選項（可多選）' : f.placeholder || '請選擇'"
                  style="width: 100%"
                  @update:value="
                    (v) => {
                      // 級聯：network 變化 → 刷新 token 選項（已選鏈的代幣並集）
                      if (f.dynamic && f.key === 'network') {
                        const tokenField = currentFields.find((x) => x.key === 'token');
                        if (tokenField) loadFieldOptions(tokenField);
                      }
                    }
                  "
                />
                <div v-if="f.dynamic && dynamicOpts[f.key]?.fallback" class="text-12px opacity-50 mt-4px">
                  無法連接網關，當前為內置選項（配置網關地址後自動刷新）
                </div>
                <div v-if="['bepusdt', 'upay'].includes(current?.driver || '') && f.help" class="text-12px opacity-70 mt-4px">{{ f.help }}</div>
              </div>
            </template>
            <template v-else-if="f.type === 'textarea'">
              <div class="w-full">
                <NInput v-model:value="form.values[f.key]" type="textarea" :rows="4" :placeholder="fieldHint(f)"
                  :input-props="f.sensitive ? { autocomplete: 'off', spellcheck: false } : undefined" />
                <div v-if="f.help" class="text-12px opacity-70 mt-4px">{{ f.help }}</div>
              </div>
            </template>
            <template v-else-if="f.type === 'number'">
              <div class="w-full">
                <NInputNumber v-model:value="form.values[f.key] as any" style="width: 100%" :placeholder="fieldHint(f)"
                  :min="f.key === 'timeout' ? (current?.driver === 'upay' ? 60 : current?.driver === 'bepusdt' ? 180 : undefined) : undefined"
                  :max="['bepusdt', 'upay'].includes(current?.driver || '') && f.key === 'timeout' ? 3600 : undefined" />
                <div v-if="['bepusdt', 'upay'].includes(current?.driver || '') && f.help" class="text-12px opacity-70 mt-4px">{{ f.help }}</div>
              </div>
            </template>
            <template v-else>
              <div class="w-full">
                <NInput
                  v-model:value="form.values[f.key]"
                  :type="f.sensitive ? 'password' : 'text'"
                  show-password-on="click"
                  :placeholder="fieldHint(f)"
                  @blur="f.key === 'api_url' && f.dynamic !== true && current?.driver === 'epusdt' && refreshAssets()"
                />
                <div v-if="f.help && (current?.driver === 'upay' || (['epusdt', 'bepusdt'].includes(current?.driver || '') && f.key === 'api_url'))" class="text-12px opacity-70 mt-4px">
                  {{ f.help }}
                </div>
              </div>
            </template>
          </NFormItem>
        </template>

        <!-- 回調地址（配置到支付平台 webhook/notify） -->
        <template v-if="current?.callback_url">
          <NDivider title-placement="left" style="margin: 4px 0 16px">回調地址</NDivider>
          <NAlert type="info" :show-icon="true" title="異步通知回調地址" style="margin-bottom: 16px">
            <div class="text-12px opacity-70 mb-8px">將該地址配置到支付平台的 webhook / 異步通知，用於接收支付結果</div>
            <div class="flex items-center gap-8px">
              <code class="flex-1 text-12px break-all select-all bg-#f1f5f9 dark:bg-#333 rounded-6px px-8px py-6px">
                {{ current.callback_url }}
              </code>
              <NButton size="small" secondary type="primary" @click="copyText(current.callback_url)">複製</NButton>
            </div>
          </NAlert>
        </template>
      </NForm>

      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton @click="configVisible = false">取消</NButton>
          <NButton v-auth="'payment:write'" type="primary" :loading="saving" @click="handleConfigSave">保存</NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.payment-heading { display: grid; grid-template-columns: minmax(0, 1fr) minmax(340px, 1.2fr) auto; align-items: center; gap: 16px; }
@media (max-width: 1199px) {
  .payment-heading { grid-template-columns: minmax(0, 1fr) auto; }
  .payment-sponsors { grid-column: 1 / -1; grid-row: 2; }
  .payment-add { grid-column: 2; grid-row: 1; }
}
</style>
