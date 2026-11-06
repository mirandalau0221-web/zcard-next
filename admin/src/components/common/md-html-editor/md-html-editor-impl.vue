<script setup lang="ts">
/**
 * MD/HTML 三模式文檔編輯器：
 *   - 「所見即所得」：wangEditor 富文本（HTML，插圖走素材庫，服務端白名單 sanitize）
 *   - 「Markdown」：md-editor-v3（開源 MIT，編輯 + 分屏預覽 + 工具欄 + 素材庫插圖）
 *   - 「HTML源碼」：等寬 textarea 直接編輯 HTML
 *   - 模式切換雙向轉換（turndown html→md / marked md→html）——
 *     v-model 恆為 HTML（前台 v-html 渲染的事實標準），MD/HTML 只是編輯態
 */
import { ref, watch, h, Fragment } from "vue";
import { NRadioButton, NRadioGroup, NInput } from "naive-ui";
import TurndownService from "turndown";
import { marked } from "marked";
import { MdEditor } from "md-editor-v3";
import type { ToolbarNames } from "md-editor-v3";
import "md-editor-v3/lib/style.css";
import RichEditor from "../rich-editor/index.vue";
import MediaToolbar from "./media-toolbar.vue";

defineOptions({ name: "MdHtmlEditor" });

const props = withDefaults(
  defineProps<{
    modelValue?: string;
    height?: string;
  }>(),
  { modelValue: "", height: "320px" },
);

const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

const mode = ref<"wysiwyg" | "markdown" | "html">("wysiwyg");
const html = ref(props.modelValue || "");
const mdDraft = ref("");
const htmlDraft = ref("");

const turndown = new TurndownService({
  headingStyle: "atx",
  codeBlockStyle: "fenced",
  bulletListMarker: "-",
});
turndown.keep(["video", "source"]);
marked.setOptions({ breaks: true, gfm: true });

// 外部值變化同步：
// - 與 html 一致 = 編輯器自身 emit 迴流，忽略（避免打斷 MD/HTML 編輯態）
// - 不一致 = 彈窗重開加載新內容（display-directive=show 下編輯器不銷燬重建，
//   須在此重置模式與草稿，否則停留在上次的 MD/HTML 模式看到舊草稿——
//   「編輯內容丟失」的根因之一）
watch(
  () => props.modelValue,
  (v) => {
    if (v === html.value) return;
    mode.value = "wysiwyg";
    html.value = v || "";
    mdDraft.value = "";
    htmlDraft.value = "";
  },
);

function switchMode(m: "wysiwyg" | "markdown" | "html") {
  if (m === mode.value) return;
  const prev = mode.value;
  if (m === "markdown") {
    mdDraft.value = turndown.turndown(html.value || "");
  } else if (m === "html") {
    htmlDraft.value = html.value || "";
  } else {
    // 切回所見即所得：按來源模式回填（md 經 marked 轉 HTML；html 直用源碼）
    const back = prev === "markdown" ? (marked.parse(mdDraft.value || "") as string) : htmlDraft.value;
    html.value = back;
    emit("update:modelValue", back);
  }
  mode.value = m;
}

function onMdChange() {
  // md-editor-v3 已通過 v-model 更新 mdDraft；同步 html 事實源並實時轉 HTML 對外輸出
  const out = marked.parse(mdDraft.value || "") as string;
  html.value = out;
  emit("update:modelValue", out);
}

function onWysiwygInput(v: string) {
  html.value = v;
  emit("update:modelValue", v);
}

function onHtmlInput(v: string) {
  htmlDraft.value = v;
  html.value = v;
  emit("update:modelValue", v);
}

// ── md-editor-v3 配置：精簡工具欄（禁圖片上傳——圖片一律素材庫；自定義素材按鈕由庫注入 insert）──
const mdToolbars: ToolbarNames[] = [
  "bold",
  "italic",
  "strikeThrough",
  "title",
  "-",
  "quote",
  "unorderedList",
  "orderedList",
  "task",
  "codeRow",
  "code",
  "link",
  0, // 素材庫圖片（defToolbars 中第 0 個按鈕）
  1, // 視頻
  "table",
  "-",
  "revoke",
  "next",
  "prettier",
  "preview",
  "htmlPreview",
  "fullscreen",
];
// 自定義工具欄按鈕：defToolbars 傳 Fragment（children 為按鈕數組），toolbars 數字下標引用
const defToolbars = h(Fragment, [h(MediaToolbar, { title: "素材庫圖片" }), h(MediaToolbar, {title:"插入視頻", video:true})]);
</script>

<template>
  <div class="md-html-editor">
    <div class="mb-8px flex items-center justify-between">
      <NRadioGroup size="small" :value="mode" @update:value="(v: any) => switchMode(v)">
        <NRadioButton value="wysiwyg">所見即所得</NRadioButton>
        <NRadioButton value="markdown">Markdown</NRadioButton>
        <NRadioButton value="html">HTML源碼</NRadioButton>
      </NRadioGroup>
      <span class="text-11px text-gray-400">
        {{
          mode === "wysiwyg"
            ? "富文本（支持圖片和視頻）"
            : mode === "markdown"
              ? "Markdown 代碼模式（分屏預覽，保存時自動轉 HTML）"
              : "HTML 源碼（保存時經服務端白名單清洗）"
        }}
      </span>
    </div>
    <!-- wangEditor 常駐（v-show）：銷燬重建會崩（Slate 實例衝突），MD/HTML 走 v-if 重建 -->
    <RichEditor v-show="mode === 'wysiwyg'" :model-value="html" :height="height" @update:model-value="onWysiwygInput" />
    <MdEditor
      v-if="mode === 'markdown'"
      v-model="mdDraft"
      :toolbars="mdToolbars"
      :def-toolbars="defToolbars"
      preview-theme="github"
      code-theme="github"
      language="zh-TW"
      :preview="true"
      :no-upload-img="true"
      :no-prettier="false"
      :style="{ height }"
      placeholder="支持 GFM：標題/列表/代碼塊/表格/圖片…"
      @on-change="onMdChange"
    />
    <NInput
      v-else-if="mode === 'html'"
      :value="htmlDraft"
      type="textarea"
      :style="{ height }"
      class="font-mono"
      placeholder="直接編輯 HTML：&lt;h2&gt;標題&lt;/h2&gt;&lt;p&gt;正文&lt;/p&gt;&lt;img src=&quot;素材庫URL&quot;&gt;…"
      @update:value="onHtmlInput"
    />
  </div>
</template>
