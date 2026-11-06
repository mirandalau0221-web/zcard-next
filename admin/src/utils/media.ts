import { getServiceBaseURL } from "@/utils/service";

// 素材/靜態資源 URL 解析：
// 後端返回相對路徑（如 /uploads/2026/08/xxx.png），瀏覽器直接加載會落在前端
// 站點根下 404（admin 走 /proxy-default 網關，圖片需同網關前綴轉發到後端靜態服務）。
// 絕對 URL（http(s)/data:）原樣返回。
const isHttpProxy = import.meta.env.DEV && import.meta.env.VITE_HTTP_PROXY === "Y";
const { baseURL } = getServiceBaseURL(import.meta.env, isHttpProxy);

export function resolveMediaUrl(url?: string | null): string {
  if (!url) return "";
  if (/^https?:\/\//i.test(url) || url.startsWith("data:")) return url;
  if (url.startsWith("/uploads/") || url.startsWith("/static/")) {
    return `${baseURL}${url}`;
  }
  return url;
}
