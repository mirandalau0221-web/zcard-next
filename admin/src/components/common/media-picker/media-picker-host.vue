<script setup lang="ts">
/**
 * 素材選擇器全局掛載點（App.vue 引一次；彈窗常駐、按 mediaPickerState.show 顯隱）。
 */
import MediaPicker from "./media-picker.vue";
import VideoPicker from "../video-picker/video-picker.vue";
</script>

<template>
  <VideoPicker />
  <MediaPicker />
</template>
