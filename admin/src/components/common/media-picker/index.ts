import { reactive } from "vue";

/**
 * 素材選擇器全局單例（模塊級狀態，App.vue 掛載 <MediaPickerHost /> 一次）。
 *
 * 用法：const urls = await pickMedia({ multiple: true }); // 取消返回 null
 * 所有「上傳/選擇圖片」入口統一呼出素材管理彈窗——複用已有素材 + 就地上傳。
 */

interface MediaPickerState {
  show: boolean;
  multiple: boolean;
  initialURLs: string[];
 kind: "image" | "video";
  tip: string;
  resolve: ((urls: string[] | null) => void) | null;
}

export const mediaPickerState = reactive<MediaPickerState>({
  show: false,
  multiple: false,
  initialURLs: [],
 kind: "image",
  tip: "",
  resolve: null,
});

export interface PickMediaOptions {
  /** 多選（圖集）；默認單選 */
  multiple?: boolean;
  initialURLs?: string[];
 kind?: "image" | "video";
  /** 當前圖片用途的上傳提示 */
  tip?: string;
}

export function pickMedia(options: PickMediaOptions = {}): Promise<string[] | null> {
  // 二次打開時廢棄上一個等待（防懸掛 Promise）
  mediaPickerState.resolve?.(null);
  mediaPickerState.multiple = options.multiple ?? false;
  mediaPickerState.initialURLs = [...new Set(options.initialURLs || [])];
 mediaPickerState.kind = options.kind ?? "image";
  mediaPickerState.tip = options.tip ?? "";
  return new Promise((resolve) => {
    mediaPickerState.resolve = resolve;
    mediaPickerState.show = true;
  });
}

/** 彈窗內部專用：確認/取消回填 */
export function settleMediaPicker(urls: string[] | null) {
  mediaPickerState.show = false;
  mediaPickerState.resolve?.(urls);
  mediaPickerState.resolve = null;
}
