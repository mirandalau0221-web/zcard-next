package media

// 本地存儲 + 靜態服務（）：
// 存儲根 data/uploads/YYYY/MM/<隨機名>.<ext>（1.x 年月目錄慣例）；
// 靜態路由 /uploads/：擴展名 Content-Type + ETag（sha256 前 16 位）+
// Cache-Control；目錄列表禁用（僅精確文件路徑）；路徑穿越拒絕。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// StorageRoot 本地存儲根（相對工作目錄；Docker 部署掛 /data 卷）。
var StorageRoot = "data/uploads"

// contentTypeByExt 靜態服務 Content-Type（僅白名單擴展——其餘一律 octet-stream 拒絕）。
var contentTypeByExt = map[string]string{
	".mp4": "video/mp4",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".png": "image/png", ".webp": "image/webp", ".gif": "image/gif",
	".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon", ".svg": "image/svg+xml",
	".heic": "image/heic", ".heif": "image/heif",
}

// SaveLocal 存儲：淨化後字節 → 年月目錄隨機名；返回相對路徑。
func SaveLocal(clean []byte, ext string) (relPath string, err error) {
	return SaveLocalIn("", clean, ext)
}

// SaveLocalIn 存儲到 uploads/<subDir>/（subDir 淨化：僅中文/字母/數字/._-，
// 其餘替換為 -；空 = 年月目錄）。返回相對路徑（正斜槓——DB 與 URL 統一口徑）。
// 子目錄用於按來源分組（如採集渠道名），便於定位與清理。
func SaveLocalIn(subDir string, clean []byte, ext string) (relPath string, err error) {
	sub := sanitizeSubDir(subDir)
	now := time.Now().UTC()
	dir := StorageRoot
	if sub != "" {
		dir = filepath.Join(dir, sub)
	} else {
		dir = filepath.Join(dir, fmt.Sprintf("%04d/%02d", now.Year(), int(now.Month())))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("media: 創建存儲目錄失敗: %w", err)
	}
	name := randToken() + ext
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, clean, 0o644); err != nil {
		return "", fmt.Errorf("media: 寫入文件失敗: %w", err)
	}
	rel := strings.TrimPrefix(strings.ReplaceAll(full, "\\", "/"), StorageRoot+"/")
	return rel, nil
}

// sanitizeSubDir 子目錄名淨化：保留中文/字母/數字/._-，其餘替換為 -；禁路徑穿越。
func sanitizeSubDir(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r >= 0x4e00 && r <= 0x9fff: // 中文
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return ""
	}
	if len(out) > 60 { // 目錄名上限（防超長）
		out = out[:60]
	}
	return out
}

// DeleteLocal 物理刪除（ref=0 時調用；文件缺失視為已清理）。
func DeleteLocal(relPath string) error {
	full, ok := safeJoin(relPath)
	if !ok {
		return nil // 非法路徑：忽略（不泄露存在性）
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// safeJoin 相對路徑拼接存儲根（拒絕穿越）。
func safeJoin(relPath string) (string, bool) {
	clean := path.Clean("/" + relPath) // 歸一化並錨定根
	full := filepath.Join(StorageRoot, clean)
	rootAbs, err1 := filepath.Abs(StorageRoot)
	fullAbs, err2 := filepath.Abs(full)
	if err1 != nil || err2 != nil || !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

// randToken 隨機文件名（16 字節 hex）。
func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失敗不可恢復（熵源故障）——fail closed
		panic("media: 隨機數生成失敗")
	}
	return hex.EncodeToString(b)
}

// HashFile sha256（秒傳去重/ETag 複用）。
func HashFile(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// RegisterStatic 靜態服務 /uploads/（kratos srv.HandleFunc 註冊）。
// ETag = sha256(path)+size（內容變更即失效）；僅白名單擴展；無目錄列表。
func RegisterStatic(mux *khttp.Server) {
	mux.HandlePrefix("/uploads/", http.HandlerFunc(serveStatic))
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/uploads/")
	if rel == "" || strings.HasSuffix(rel, "/") {
		http.NotFound(w, r) // 目錄列表禁用
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	ct, ok := contentTypeByExt[ext]
	if !ok {
		http.NotFound(w, r) // 非白名單擴展（含 .php 等）一律 404
		return
	}
	full, ok := safeJoin(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	etag := fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size())
	w.Header().Set("Content-Type", ct)
	if ct == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // 隨機名內容不變
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	http.ServeFile(w, r, full) // ServeFile 拒絕目錄；內容類型已顯式設置
}
