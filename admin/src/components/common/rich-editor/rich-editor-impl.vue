<script setup lang="ts">
/**
 * 富文本編輯器（wangEditor 5，開源 MIT——中文後台事實標準）：
 *   - 所見即所得 HTML 輸出（服務端 bluemonday 白名單 sanitize 兜底，防 XSS）
 *   - 插圖統一走素材庫（自定義菜單「素材庫圖片」→ pickMedia 彈窗）——
 *     禁用 base64/本地上傳（data: URL 會被服務端白名單剝離，且圖片必須沉澱素材庫可複用）
 */
import { onBeforeUnmount, shallowRef, computed } from "vue";
import "@wangeditor/editor/dist/css/style.css";
import { Editor, Toolbar } from "@wangeditor/editor-for-vue";
import type { IEditorConfig, IToolbarConfig, IDomEditor } from "@wangeditor/editor";
import { DomEditor } from "@wangeditor/editor";
import { registerMediaMenu } from "./media-menu";

defineOptions({ name: "RichEditor" });

const props = withDefaults(
  defineProps<{
    modelValue?: string;
    placeholder?: string;
    height?: string;
  }>(),
  { modelValue: "", placeholder: "請輸入內容…", height: "320px" },
);

const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

// ── 自定義菜單：素材庫插圖（模塊級註冊一次；編輯器銷燬後菜單類仍可複用）──

registerMediaMenu();

// ── 編輯器裝配 ──

const editorRef = shallowRef<IDomEditor>();
const value = computed({
  get: () => props.modelValue,
  set: (v: string) => emit("update:modelValue", v),
});

// 工具欄：精選鍵集（禁 uploadImage/insertVideo——圖片一律素材庫；data: URL 會被服務端剝離）
const toolbarConfig: Partial<IToolbarConfig> = {
  toolbarKeys: [
    "headerSelect",
    "|",
    "bold",
    "italic",
    "underline",
    "through",
    "color",
    "bgColor",
    "|",
    "bulletedList",
    "numberedList",
    "insertLink",
    "|",
    "zcMediaLibrary",
    "zcVideo",
    "insertTable",
    "blockquote",
    "codeBlock",
    "|",
    "clearStyle",
    "undo",
    "redo",
  ],
};

const editorConfig: Partial<IEditorConfig> = {
  placeholder: props.placeholder,
  MENU_CONF: {},
};

function handleCreated(editor: IDomEditor) {
  editorRef.value = editor;
}

// 銷燬（wangEditor 強紀律：不銷燬會內存泄漏；防禦性包裹——Slate 實例
// 異常時不讓卸載鈎子拋錯中斷組件樹）
onBeforeUnmount(() => {
  try {
    const editor = editorRef.value;
    if (!editor || editor.isDestroyed) return;
    editor.blur();
    // wangEditor removes the listener on destroy, but leaves its 100 ms debounce pending.
    // Cancel that callback before the editor-to-DOM mapping is removed.
    const textarea = DomEditor.getTextarea(editor) as unknown as { onDOMSelectionChange?: { cancel(): void } };
    textarea.onDOMSelectionChange?.cancel();
    editor.destroy();
    editorRef.value = undefined;
  } catch {
    // 忽略銷燬異常（重複銷燬/實例已失效）
  }
});
</script>

<template>
  <div class="z-0 overflow-hidden rounded-4px border border-gray-300 dark:border-gray-600">
    <Toolbar
      :editor="editorRef"
      :default-config="toolbarConfig"
      mode="default"
      class="border-b border-gray-200 dark:border-gray-600"
    />
    <Editor
      v-model="value"
      :default-config="editorConfig"
      mode="default"
      :style="{ height, overflowY: 'hidden' }"
      @on-created="handleCreated"
    />
  </div>
</template>

<style>
/* wangEditor 亮色渲染——暗色模式下容器底色兜底（編輯區保持白底保證可讀性） */
.w-e-text-container {
  background-color: #fff;
}
.w-e-toolbar {
  background-color: #fafafa;
}
</style>
