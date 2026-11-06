// 推廣歸因捕獲：URL ?ref= → localStorage（30 天 TTL）。
// 任何頁面進站即捕獲（不依賴註冊頁）；註冊預填/下單帶碼統一讀取。

const REF_KEY = 'zc_ref';
const REF_TTL_MS = 30 * 24 * 60 * 60 * 1000; // 30 天

interface RefRecord {
  code: string;
  expires: number;
}

/** 捕獲 URL 中的 ?ref=（App 掛載時調用一次） */
export function captureRefCode() {
  try {
    const params = new URLSearchParams(location.search);
    const ref = params.get('ref');
    if (ref && ref.trim()) {
      const rec: RefRecord = { code: ref.trim(), expires: Date.now() + REF_TTL_MS };
      localStorage.setItem(REF_KEY, JSON.stringify(rec));
    }
  } catch { /* 存儲不可用忽略 */ }
}

/** 讀取有效歸因碼（過期清除；空返回 ''） */
export function getRefCode(): string {
  try {
    const raw = localStorage.getItem(REF_KEY);
    if (!raw) return '';
    const rec = JSON.parse(raw) as RefRecord;
    if (!rec?.code || !rec?.expires || rec.expires < Date.now()) {
      localStorage.removeItem(REF_KEY);
      return '';
    }
    return rec.code;
  } catch {
    return '';
  }
}

/** 清除歸因（註冊成功消費後可清；保留亦可——下單仍可歸因） */
export function clearRefCode() {
  try {
    localStorage.removeItem(REF_KEY);
  } catch { /* 忽略 */ }
}
