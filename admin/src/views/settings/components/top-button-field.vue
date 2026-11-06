<script setup lang="ts">
// 站點頂部自定義按鈕設置（site.top_button JSON）：
// {text, type: link|post|notice, url?, slug?}——外部鏈接 / 站內文章 / 站內公告三態跳轉；
// 文字留空 = 不顯示；保存為 JSON。
import { computed, ref, watch } from "vue";
import { NButton, NInput, NRadioButton, NRadioGroup, NSelect } from "naive-ui";
import { fetchPosts } from "@/service/api";

const props = defineProps<{ value: string | null }>();
const emit = defineEmits<{ (e: "update", v: { text: string; type: string; url?: string; slug?: string } | null): void }>();

const text = ref("");
const type = ref<"link" | "post" | "notice">("link");
const url = ref("");
const slug = ref<string | null>(null);

// 文章/公告下拉（複用營銷-輪播的既有模式：zhValue 解析 title_json）
const posts = ref<any[]>([]);
const postsLoading = ref(false);

function zhValue(json: string): string {
  if (!json) return "-";
  try {
    const v = JSON.parse(json);
    return v.zh_CN || v.zh || Object.values(v)[0] || "-";
  } catch {
    return json;
  }
}

const postOptions = computed(() =>
  posts.value
    .filter((p) => (type.value === "notice" ? p.type === "notice" : true))
    .map((p) => ({ label: `${p.type === "notice" ? "[公告] " : ""}${zhValue(p.title_json)}`, value: p.slug })),
);

async function loadPosts() {
  if (posts.value.length || postsLoading.value) return;
  postsLoading.value = true;
  try {
    const { data, error } = await fetchPosts();
    if (!error && data) posts.value = (data as any).posts || [];
  } finally {
    postsLoading.value = false;
  }
}

function parse(v: string | null) {
  try {
    const o = JSON.parse(v || "null");
    if (o && typeof o === "object") {
      text.value = typeof o.text === "string" ? o.text : "";
      // 兼容舊結構 {text,url}（無 type 字段按外部鏈接處理）
      const t = o.type === "post" || o.type === "notice" ? o.type : "link";
      type.value = t;
      url.value = typeof o.url === "string" ? o.url : "";
      slug.value = typeof o.slug === "string" ? o.slug : null;
      return;
    }
  } catch {
    /* 非法 JSON 按空處理 */
  }
  text.value = "";
  type.value = "link";
  url.value = "";
  slug.value = null;
}
parse(props.value);
watch(() => props.value, parse);

// 切換跳轉類型：預載文章列表；清掉不屬於新類型的舊值
watch(type, (t) => {
  if (t !== "link") {
    loadPosts();
    url.value = "";
  } else {
    slug.value = null;
  }
});

const enabled = computed(() => !!text.value.trim());

function push() {
  const t = text.value.trim();
  // 文字留空 = 不啓用（存 null）；父級 setVal 統一 JSON.stringify
  if (!t) {
    emit("update", null);
    return;
  }
  if (type.value === "link") {
    emit("update", { text: t, type: "link", url: url.value.trim() });
  } else {
    emit("update", { text: t, type: type.value, slug: slug.value || "" });
  }
}

// 跳轉目標描述（提示行用）
const targetDesc = computed(() => {
  if (!enabled.value) return "";
  if (type.value === "link") {
    return url.value.trim() ? `點擊打開 ${url.value.trim()}（新窗口）` : "未填鏈接——按鈕只顯示不跳轉";
  }
  const opt = postOptions.value.find((o) => o.value === slug.value);
  return opt ? `點擊跳轉${type.value === "notice" ? "公告" : "文章"}「${opt.label}」` : "尚未選擇目標文章";
});

function clear() {
  text.value = "";
  url.value = "";
  slug.value = null;
  push();
}
</script>

<template>
  <div class="flex w-full flex-col gap-8px">
    <div class="flex items-center gap-8px">
      <NInput v-model:value="text" size="small" class="w-140px shrink-0" placeholder="按鈕文字，如 客服中心" @update:value="push" />
      <NRadioGroup v-model:value="type" size="small" @update:value="push">
        <NRadioButton value="link">外部鏈接</NRadioButton>
        <NRadioButton value="post">文章</NRadioButton>
        <NRadioButton value="notice">公告</NRadioButton>
      </NRadioGroup>
      <NButton v-if="enabled" size="small" quaternary type="error" class="shrink-0" @click="clear">清除</NButton>
    </div>
    <div class="flex items-center gap-8px">
      <NInput
        v-if="type === 'link'"
        v-model:value="url"
        size="small"
        class="flex-1"
        placeholder="跳轉鏈接 https://…（留空則不跳轉）"
        @update:value="push"
      />
      <NSelect
        v-else
        v-model:value="slug"
        size="small"
        class="flex-1"
        filterable
        clearable
        :loading="postsLoading"
        :options="postOptions"
        :placeholder="type === 'notice' ? '選擇要跳轉的公告文章' : '選擇要跳轉的文章'"
        @update:value="push"
      />
    </div>
    <div class="flex items-center gap-8px">
      <span class="text-11px text-gray-400">
        {{ enabled ? `效果預覽（將顯示在 storefront 頂部導航）：${targetDesc}` : "文字留空 = 不顯示；填寫後顯示在 storefront 頂部導航" }}
      </span>
      <span
        v-if="enabled"
        class="rounded-4px border border-primary px-8px py-2px text-12px font-500 text-primary"
        :title="`按鈕「${text}」在 storefront 頂部導航的顯示效果`"
      >
        {{ text }}
      </span>
    </div>
  </div>
</template>
