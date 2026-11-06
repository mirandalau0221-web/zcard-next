<script setup lang="ts">
import { defineAsyncComponent, h } from "vue";
const props = defineProps<{ modelValue?: string; placeholder?: string; height?: string }>();
const emit = defineEmits<{ (e: "update:modelValue", value: string): void }>();
// List routes can render without downloading the editor and its plugins.
const Editor = defineAsyncComponent({
  loader: () => import("./rich-editor-impl.vue"),
  delay: 150,
  loadingComponent: { render: () => h("div", { role: "status", class: "p-16px text-gray-500" }, "編輯器加載中…") },
  errorComponent: { render: () => h("div", { role: "alert", class: "p-16px text-red-500" }, "編輯器加載失敗，請刷新頁面後重試") },
});
</script>
<template>
  <Editor v-bind="props" @update:model-value="emit('update:modelValue', $event)" />
</template>
