import { t as $t } from '@/i18n';
import { computed } from 'vue';
// 商品無圖默認佔位（SVG data URI：灰底 + 🖼 + 「暫無主圖」）。
// 用於無 cover 與 cover 加載失敗（@error）兩種場景，避免裂圖/空白。

export const NO_IMAGE = computed(() =>
  'data:image/svg+xml;charset=utf-8,' +
  encodeURIComponent(
    "<svg xmlns='http://www.w3.org/2000/svg' width='400' height='400'>" +
      "<rect width='100%' height='100%' fill='#f1f5f9'/>" +
      "<rect x='1' y='1' width='398' height='398' fill='none' stroke='#e2e8f0' stroke-width='2'/>" +
      "<text x='50%' y='44%' font-size='40' fill='#cbd5e1' text-anchor='middle' dominant-baseline='middle'>🖼️</text>" +
      "<text x='50%' y='60%' font-family='-apple-system,PingFang SC,sans-serif' font-size='24' fill='#94a3b8' text-anchor='middle' dominant-baseline='middle'>" + $t('暫無主圖') + '</text>' +
      '</svg>',
  ));

// onImgError 圖片加載失敗 → 換佔位圖（data URI 不會再失敗，無重入風險）。
export function onImgError(e: Event) {
  const img = e.target as HTMLImageElement;
  if (img.src !== NO_IMAGE.value) img.src = NO_IMAGE.value;
}
