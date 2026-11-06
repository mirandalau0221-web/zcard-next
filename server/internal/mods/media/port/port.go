// Package port 為 media 模塊對外契約（零依賴包）。
package port

import "context"

// UploadInput 上傳入參（內存字節；ticket 附件/未來模塊複用）。
type UploadInput struct {
	Name        string // 原始文件名（擴展名參與白名單）
	ContentType string // 聲明 MIME（參與三重校驗）
	Data        []byte
	CategoryID  uint64 // 0 = 未分類
	UploaderID  uint64
}

// UploadResult 上傳結果。
type UploadResult struct {
	ID     uint64
	Path   string // 相對存儲根（/uploads/<path> 可訪問）
	Width  int32
	Height int32
}

// Uploader 上傳端口（業務模塊消費，通道 A——經安全三件套）。
type Uploader interface {
	Upload(ctx context.Context, in UploadInput) (*UploadResult, error)
}

// Referencer 引用計數端口（catalog/content/ticket 保存時調用）。
type Referencer interface {
	// AddRefs 引用 +1（ids 去重後統一加）。
	AddRefs(ctx context.Context, ids []uint64) error
	// ReleaseRefs 引用 -1（下限 0）。
	ReleaseRefs(ctx context.Context, ids []uint64) error
}
