import { t as $t } from '@/i18n';
import { mergeThemeConfig } from '../../packages/theme-sdk/src/index';
// Seed all modules before Vue mounts.
import { publicConfig } from './config';
if (!publicConfig.value) mergeThemeConfig([]);
import { ViteSSG } from 'vite-ssg';
import App from './App.vue';
import { routes, installRouterGuards, scrollBehavior } from './router';
import { setActiveHead } from './seo';
import './style.css';

export const createApp = ViteSSG(
  App,
  // Theme assets use a versioned <base>; application routes stay at the site root.
  { routes, base: '/', scrollBehavior },
  ({ app, head, router, isClient }) => {
    setActiveHead(head);
    if (isClient) {
      // 客户端用完整 router（含登錄守衞/尾斜槓規範化）；SSR 用 vite-ssg 內置 router 渲染。
      // 注意：默認 SEO 不在客户端全局 push（會與水合中的頁面級 SEO 競爭覆蓋）——
      // 首頁由 Home.vue 負責默認 SEO；頁面級 SEO 由各頁面組件負責。
      // 守衞直接註冊到 vite-ssg 的 router（不創建第二個 router——雙 history 會互相干擾）
      installRouterGuards(router);
    } else {
      app.use(router);
      // A distributable build has no customer identity or catalog. The Go bot
      // renderer supplies request-time SEO; never bake build-host API errors.
      return;
    }
  },
);

// Release builds contain a neutral shell. Explicit fixture/site builds can also
// emit dynamic route files; request-time data and SEO are supplied by the server.
export async function includedRoutes(paths: string[], _routes: unknown[]) {
  const api = import.meta.env.VITE_SSG_API;
  const out = paths.filter((p) => !p.includes(':'));
  if (!api) return out;
  try {
    const [prodResp, postResp] = await Promise.all([
      fetch(`${api}/api/v1/storefront/products?page=1&page_size=200`),
      fetch(`${api}/api/v1/storefront/posts?page=1&page_size=200`),
    ]);
    const prods = await prodResp.json();
    (prods?.items || []).forEach((p: { id: number }) => out.push(`/product/${p.id}`));
    const posts = await postResp.json();
    (posts?.posts || []).forEach((p: { slug: string }) => out.push(`/posts/${p.slug}`));
  } catch (e) {
    console.warn($t("[ssg] 靜態化數據拉取失敗（跳過動態頁）"), e);
  }
  return out;
}
