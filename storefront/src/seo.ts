import { t as $t, localeTag } from '@/i18n';
import { loadPublicConfig } from '@/config';
/**
 * SEO 工具：站點級配置消費（/api/v1/storefront/config 公開下發）+ 頁面級 head 聲明。
 * 基於 @unhead/vue（vite-ssg 內置）：SSG 構建時輸出到靜態 HTML，客户端水合後動態更新。
 * 通過 vite-ssg 注入的 head 實例操作（head.push 不依賴組件 setup 上下文——
 * async setup 的 await 之後 useHead/inject 不可用）。
 * SSR 安全：不依賴 window/document（canonical 基準用 site.url）。
 */

type HeadEntry = Record<string, any>;

let activeHead: { push: (entry: HeadEntry) => unknown } | null = null;

/** 由 vite-ssg createApp 回調注入 head 實例（main.ts） */
export function setActiveHead(head: { push: (entry: HeadEntry) => unknown } | undefined) {
  activeHead = head ?? null;
}

function headPush(entry: HeadEntry) {
  if (!activeHead) {
    console.warn($t("[seo] head 未注入（setActiveHead 未調用）"));
    return;
  }
  activeHead.push(entry);
}

export interface SiteSeoConfig {
  name: string;
  url: string;
  logo: string;
  seoTitle: string;
  seoKeywords: string;
  seoDesc: string;
  verificationGoogle: string;
  verificationBing: string;
}

let cached: SiteSeoConfig | null = null;

function parseStr(raw: string | undefined, dflt = ""): string {
  if (raw === undefined) return dflt;
  try {
    const v = JSON.parse(raw);
    return typeof v === "string" ? v : dflt;
  } catch {
    return dflt;
  }
}

/** 站點 SEO 配置（失敗回退默認值；SSR 構建期經 VITE_SSG_API 訪問，客户端同源） */
export async function fetchSiteSeo(): Promise<SiteSeoConfig> {

  const def: SiteSeoConfig = { get name() { return $t("商店"); }, url: "", logo: "", seoTitle: "", seoKeywords: "", seoDesc: "", verificationGoogle: "", verificationBing: "" };
  try {
    const json = await loadPublicConfig();
    const find = (k: string) => json?.entries?.find((e: any) => e.key === k)?.value_json;
    cached = {
      name: parseStr(find("site.name"), def.name),
      url: parseStr(find("site.url")),
      logo: parseStr(find("site.logo")),
      seoTitle: parseStr(find("site.seo_title")),
      seoKeywords: parseStr(find("site.seo_keywords")),
      seoDesc: parseStr(find("site.seo_desc")),
      verificationGoogle: parseStr(find("site.verification_google")),
      verificationBing: parseStr(find("site.verification_bing")),
    };
    return cached;
  } catch {
    return cached || def;
  }
}

/** 頁面級 SEO 元數據 */
export interface SeoMeta {
  title?: string;
  description?: string;
  keywords?: string;
  /** 缺省 = 站點地址（site.url） */
  canonical?: string;
  ogImage?: string;
  ogType?: string; // website | article | product
  jsonLd?: object | object[];
}

/** 絕對 URL 歸一（相對路徑拼站點基準） */
function absUrl(u: string, base: string): string {
  if (!u) return "";
  return /^https?:\/\//i.test(u) ? u : base + u;
}

/** 站點 URL 基準（SSR 無 window：site.url 優先，空則客户端 origin） */
function siteBase(site: SiteSeoConfig): string {
  if (site.url) return site.url;
  return typeof window !== "undefined" ? window.location.origin : "";
}

/**
 * 聲明頁面級 SEO（冪等）：title/canonical/meta/og/JSON-LD。
 * 所有鍵每次全量輸出（缺省空串）+ key 去重——避免 SPA 導航殘留舊頁 meta。
 */
export function applySeo(meta: SeoMeta, site: SiteSeoConfig) {
  const base = siteBase(site);
  const canonicalUrl = meta.canonical || base;
  headPush({
    title: meta.title || "",
    htmlAttrs: { lang: localeTag.value },
    meta: [
      { name: "description", content: meta.description ?? "", key: "description" },
      { name: "keywords", content: meta.keywords ?? "", key: "keywords" },
      { property: "og:title", content: meta.title ?? "", key: "og:title" },
      { property: "og:description", content: meta.description ?? "", key: "og:description" },
      { property: "og:type", content: meta.ogType || "website", key: "og:type" },
      { property: "og:url", content: canonicalUrl, key: "og:url" },
      { property: "og:image", content: meta.ogImage || absUrl(site.logo, base), key: "og:image" },
    ],
    link: [{ rel: "canonical", href: canonicalUrl, key: "canonical" }],
    script: [{ key: "jsonld", type: "application/ld+json", innerHTML: meta.jsonLd ? JSON.stringify(meta.jsonLd) : "" }],
  });
}

/** 站點兜底 SEO（首頁/非 SEO 頁面） */
export function applyDefaultSeo(site: SiteSeoConfig) {
  const base = siteBase(site);
  applySeo(
    {
      title: site.seoTitle || $t("{0} - 自動髮卡商城", [site.name]),
      description: site.seoDesc,
      keywords: site.seoKeywords,
      ogType: "website",
      jsonLd: {
        "@context": "https://schema.org",
        "@type": "Organization",
        name: site.name,
        url: base,
        ...(site.logo ? { logo: absUrl(site.logo, base) } : {}),
      },
    },
    site,
  );
}

/** 站長驗證碼 meta（GSC/Bing 驗證；SSG 輸出到靜態頁 head） */
export function applyVerification(site: SiteSeoConfig) {
  headPush({
    meta: [
      ...(site.verificationGoogle
        ? [{ name: "google-site-verification", content: site.verificationGoogle, key: "google-verification" }]
        : []),
      ...(site.verificationBing
        ? [{ name: "msvalidate.01", content: site.verificationBing, key: "bing-verification" }]
        : []),
    ],
  });
}

/** HTML 轉純文本（meta description 用；無 DOM 依賴，SSR 安全） */
export function stripHtml(html: string): string {
  return html
    .replace(/<[^>]*>/g, " ")
    .replace(/&nbsp;/gi, " ")
    .replace(/&amp;/gi, "&")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/\s+/g, " ")
    .trim();
}

/** 截斷到 maxLen（中文按字符計） */
export function truncate(text: string, maxLen = 150): string {
  return text.length > maxLen ? text.slice(0, maxLen - 1) + "…" : text;
}
