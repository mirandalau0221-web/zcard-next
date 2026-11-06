/** 圖片路徑與 emoji 共用分類 icon 字段；相對圖片路徑按站點根目錄解析。 */
export function categoryIconImagePath(icon?: string): string {
  const value = icon?.trim() || "";
  if (!value || (!value.includes("/") && !/\.(png|jpe?g|gif|webp|svg|ico|bmp|avif)(?:[?#].*)?$/i.test(value))) return "";
  if (value.startsWith("/") || /^(https?:|data:image\/)/i.test(value)) return value;
  return `/${value.replace(/^\.\//, "")}`;
}
