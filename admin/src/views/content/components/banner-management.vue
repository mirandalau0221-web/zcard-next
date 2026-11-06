<script setup lang="ts">
// 首頁橫幅管理（storefront 首頁消費）：表格 + 新增彈窗（原內容管理橫幅區獨立成頁）。
import { computed, h, onMounted, ref, watch } from "vue";
import { NButton, NDataTable, NInput, NModal, NForm, NFormItem, NInputNumber, NPopconfirm, NSelect, NSwitch, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchBanners, createBanner, updateBanner, deleteBanner, fetchPosts } from "@/service/api";
import { checkAuth } from "@/directives";
import MediaField from "@/components/common/media-picker/media-field.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";

defineOptions({ name: "BannerManagement" });

const canWrite = () => checkAuth("content:write");

const bannerLoading = ref(false);
const banners = ref<any[]>([]);
const showBanner = ref(false);
const bannerSaving = ref(false);
const bannerForm = ref({ name: "", position: "top", title_json: "", image: [] as string[], link_type: "url", link_value: "", is_active: true, sort: 0 });

// 生效狀態快捷篩選（客戶端過濾，帶實時計數）
const bannerFilter = ref<"" | "on" | "off">("");
const bannerTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "生效中", value: "on", type: "success" as const },
  { label: "已停用", value: "off", type: "default" as const },
];
const bannerCounts = computed(() => ({
  "": banners.value.length,
  on: banners.value.filter((b) => b.is_active).length,
  off: banners.value.filter((b) => !b.is_active).length,
}));
const filteredBanners = computed(() =>
  bannerFilter.value === "" ? banners.value : banners.value.filter((b) => (bannerFilter.value === "on" ? b.is_active : !b.is_active)),
);

// 跳轉類型：post=文章詳情、notice=點擊打開公告彈窗（無需跳轉目標）
const linkTypeOptions = [
  { label: "文章（/posts/:slug）", value: "post" },
  { label: "公告彈窗（點擊彈出全局公告）", value: "notice" },
  { label: "外部鏈接", value: "url" },
  { label: "商品", value: "product" },
  { label: "分類", value: "category" },
];
const postSlugOptions = ref<{ label: string; value: string }[]>([]);
watch(
  () => bannerForm.value.link_type,
  (t) => {
    // 切換類型時清掉舊值，避免把商品 ID 當 slug 用
    if (t !== "url" && t !== "product" && t !== "category" && t !== "post") bannerForm.value.link_value = "";
  },
);

function zhValue(json: string): string {
  if (!json || json === "-") return "-";
  try {
    const v = JSON.parse(json);
    return typeof v === "object" && v ? v.zh_CN || v.en_US || Object.values(v)[0] || "-" : String(v);
  } catch {
    return json;
  }
}

const bannerColumns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 50 },
  { title: "名稱", key: "name", width: 120, ellipsis: { tooltip: true } },
  { title: "位置", key: "position", width: 120, render: (row) => ({ top: "頂部", middle: "商品列表中部", bottom: "商品列表下方" }[row.position as string] || row.position) },
  {
    title: "圖片",
    key: "image",
    width: 200,
    render: (row) =>
      row.image
        ? h(
            "span",
            {
              class: "img-preview-trigger text-primary",
              style: "border-bottom:1px dotted var(--primary-color);cursor:pointer;position:relative;",
            },
            [
              row.image.length > 28 ? row.image.slice(0, 28) + "…" : row.image,
              h("img", {
                src: row.image,
                class: "img-preview-pop",
                style: "display:none;position:absolute;left:0;top:24px;z-index:100;width:360px;max-height:240px;object-fit:contain;border-radius:6px;box-shadow:0 4px 16px rgba(0,0,0,.25);background:#fff;padding:4px;",
              }),
            ],
          )
        : "-",
  },
  { title: "跳轉", key: "link_value", width: 150, ellipsis: true, render: (row) => {
    const label = ({ url: "外鏈", product: "商品", category: "分類", post: "文章", notice: "公告彈窗", ad: "廣告" } as Record<string, string>)[row.link_type] || row.link_type;
    return h("span", { class: "flex items-center gap-4px" }, [
      h(NTag, { size: "tiny", bordered: false, type: row.link_type === "notice" ? "warning" : "info" }, { default: () => label }),
      h("span", { class: "truncate" }, row.link_value || (row.link_type === "notice" ? "—" : "-")),
    ]);
  } },
  { title: "排序", key: "sort", width: 56 },
  {
    title: "狀態",
    key: "is_active",
    width: 70,
    render: (row) => h(NTag, { size: "small", type: row.is_active ? "success" : "default" }, { default: () => (row.is_active ? "生效" : "停用") }),
  },
  {
    title: "操作",
    key: "actions",
    width: 110,
    render: (row) =>
      canWrite()
        ? h("div", { class: "flex gap-4px" }, [
            h(NButton, { size: "tiny", quaternary: true, onClick: () => openEditBanner(row) }, { default: () => "編輯" }),
            h(NPopconfirm, { onPositiveClick: () => handleDeleteBanner(row.id) }, { trigger: () => h(NButton, { size: "tiny", type: "error", quaternary: true }, { default: () => "刪除" }), default: () => "確定刪除該橫幅？" }),
          ])
        : null,
  },
];

async function loadBanners() {
  bannerLoading.value = true;
  try {
    const { data, error } = await fetchBanners();
    if (!error && data) banners.value = (data as any).banners || [];
  } finally {
    bannerLoading.value = false;
  }
}

// 跳轉類型=文章時需要 slug 下拉（橫幅與文章松耦合：僅打開彈窗時拉一次）
async function loadPostSlugOptions() {
  const { data, error } = await fetchPosts();
  if (!error && data) {
    postSlugOptions.value = ((data as any).posts || []).map((p: any) => ({
      label: `[${p.type === "notice" ? "公告" : "博客"}] ${zhValue(p.title_json)}`,
      value: p.slug,
    }));
  }
}

const editingBannerId = ref<number | null>(null);

function openCreateBanner() {
  editingBannerId.value = null;
  bannerForm.value = { name: "", position: "top", title_json: "", image: [], link_type: "url", link_value: "", is_active: true, sort: 0 };
  if (bannerForm.value.link_type === "post") loadPostSlugOptions();
  showBanner.value = true;
}

/** 編輯：回填現有值（圖片 URL 回填進 MediaField 單值數組；多語言 title 原文回填） */
function openEditBanner(row: any) {
  editingBannerId.value = row.id;
  bannerForm.value = {
    name: row.name || "",
    position: row.position || "top",
    title_json: typeof row.title_json === "string" && row.title_json.startsWith("{") ? row.title_json : "",
    image: row.image ? [row.image] : [],
    link_type: row.link_type || "url",
    link_value: row.link_value || "",
    is_active: !!row.is_active,
    sort: row.sort ?? 0,
  };
  if (bannerForm.value.link_type === "post") loadPostSlugOptions();
  showBanner.value = true;
}

async function handleBanner() {
  if (!bannerForm.value.name || !bannerForm.value.image.length) return;
  bannerSaving.value = true;
  try {
    const title = bannerForm.value.title_json || JSON.stringify({ zh_CN: bannerForm.value.name });
    const payload = { ...bannerForm.value, image: bannerForm.value.image[0], title_json: title };
    const { error } = editingBannerId.value
      ? await updateBanner(editingBannerId.value, payload)
      : await createBanner(payload);
    if (!error) {
      window.$message?.success(editingBannerId.value ? "橫幅已更新，刷新首頁即可查看" : "橫幅已創建，刷新首頁即可查看");
      showBanner.value = false;
      loadBanners();
    }
  } finally {
    bannerSaving.value = false;
  }
}

async function handleDeleteBanner(id: number) {
  const { error } = await deleteBanner(id);
  if (!error) {
    window.$message?.success("已刪除");
    loadBanners();
  }
}

onMounted(loadBanners);
</script>

<template>
  <div class="flex flex-col gap-12px">
    <div class="flex items-center justify-between gap-12px">
      <FilterTabs v-model:value="bannerFilter" :options="bannerTabs" :counts="bannerCounts" size="small" />
      <NButton v-if="canWrite()" size="small" type="primary" @click="openCreateBanner">新增橫幅</NButton>
    </div>
    <NDataTable :columns="bannerColumns" :data="filteredBanners" :loading="bannerLoading" size="small" :max-height="560" />

    <NModal v-model:show="showBanner" preset="dialog" :title="editingBannerId ? '編輯橫幅' : '新增橫幅'" style="width: 520px">
      <NForm :model="bannerForm" label-placement="left" label-width="72">
        <NFormItem label="名稱" required>
          <NInput v-model:value="bannerForm.name" />
        </NFormItem>
        <NFormItem label="位置">
          <NSelect v-model:value="bannerForm.position" :options="[{ label: '頂部', value: 'top' }, { label: '中部（商品列表內）', value: 'middle' }, { label: '底部（商品列表下方）', value: 'bottom' }]" />
        </NFormItem>
        <p v-if="bannerForm.position === 'middle'" style="margin: -4px 0 16px 72px; line-height: 1.6; color: var(--text-color-2)">
          顯示在當前頁商品中段，網格視圖會對齊完整商品行。多個橫幅在電腦端並排、手機端橫滑；僅一行商品時顯示在該行之後，無商品時隱藏。建議使用寬幅圖片。
        </p>
        <NFormItem label="圖片" required>
          <MediaField v-model:value="bannerForm.image" :tip="bannerForm.position === 'top' ? '推薦寬 1200 px × 高 400 px（3:1）；默認主題會隨屏幕裁切，文字和主體請居中，多張圖片建議尺寸一致。' : '從素材庫選擇或上傳橫幅圖片'" />
        </NFormItem>
        <NFormItem label="跳轉類型">
          <NSelect v-model:value="bannerForm.link_type" :options="linkTypeOptions" />
        </NFormItem>
        <NFormItem v-if="bannerForm.link_type === 'post'" label="跳轉文章">
          <NSelect
            v-model:value="bannerForm.link_value"
            :options="postSlugOptions"
            filterable
            placeholder="選擇點擊橫幅後打開的文章"
            @focus="!postSlugOptions.length && loadPostSlugOptions()"
          />
        </NFormItem>
        <NFormItem v-else-if="bannerForm.link_type !== 'notice'" label="跳轉目標">
          <NInput v-model:value="bannerForm.link_value" placeholder="URL 或商品/分類 ID" />
        </NFormItem>
        <NFormItem label="排序">
          <NInputNumber v-model:value="bannerForm.sort" class="w-full" />
        </NFormItem>
        <NFormItem label="生效">
          <NSwitch v-model:value="bannerForm.is_active" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showBanner = false">取消</NButton>
        <NButton type="primary" :loading="bannerSaving" @click="handleBanner">{{ editingBannerId ? "保存" : "創建" }}</NButton>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
/* 橫幅圖片懸停預覽（原生 :hover 命中測試，無 JS 依賴） */
:deep(.img-preview-trigger:hover .img-preview-pop) {
  display: block !important;
}
</style>
