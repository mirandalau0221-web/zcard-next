// The app shell can outlive the server binary that supplied its lazy chunks.
// Keep recovery independent of Vue/Naive UI: initial route loading may fail before mount.
let notice: HTMLElement | undefined;
let reloadPage = () => window.location.reload();

export function isAssetLoadError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  return /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Loading (?:CSS )?chunk .+ failed|Unable to preload CSS/i.test(message);
}

export function clearAssetLoadError() {
  notice?.remove();
  notice = undefined;
  reloadPage = () => window.location.reload();
}

export function showAssetLoadError(reload?: () => void) {
  window.NProgress?.done?.();
  if (reload) reloadPage = reload;
  if (notice?.isConnected) return;
  notice = document.createElement("section");
  notice.id = "asset-load-error";
  notice.setAttribute("role", "alert");
  notice.setAttribute("aria-label", "頁面加載失敗");
  notice.style.cssText = "position:fixed;z-index:10000;left:16px;right:16px;bottom:16px;max-width:560px;margin:auto;padding:20px;box-sizing:border-box;background:#fff;color:#172033;border:1px solid #cbd5e1;border-radius:12px;box-shadow:0 8px 32px #0003;font:14px/1.6 system-ui";
  const title = document.createElement("strong");
  title.textContent = "頁面加載失敗";
  const message = document.createElement("p");
  message.textContent = "系統可能已更新，或網絡暫時中斷。請先保存當前未提交的內容，再重新加載頁面。";
  const retry = document.createElement("button");
  retry.type = "button";
  retry.textContent = "重新加載頁面";
  retry.style.cssText = "min-height:44px;padding:8px 16px;margin-right:12px;background:#2563eb;color:#fff;border:0;border-radius:6px;cursor:pointer;font:inherit";
  retry.onclick = () => reloadPage();
  const dismiss = document.createElement("button");
  dismiss.type = "button";
  dismiss.textContent = "暫不刷新";
  dismiss.style.cssText = "min-height:44px;padding:8px 16px;background:#fff;color:#172033;border:1px solid #94a3b8;border-radius:6px;cursor:pointer;font:inherit";
  dismiss.onclick = clearAssetLoadError;
  notice.append(title, message, retry, dismiss);
  document.body.append(notice);
}

export function setupAssetRecovery() {
  // Leave rejection propagation intact so Vue Router can abort the failed navigation.
  window.addEventListener("vite:preloadError", () => showAssetLoadError());
}
