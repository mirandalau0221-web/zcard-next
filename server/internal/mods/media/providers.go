package media

// wire providers（）。
//
// 跨模塊綁定（通道 A）：
// - MediaRepo 實現 mediaport.Uploader（ticket 附件/管理上傳共用安全路徑）
// - MediaRepo 實現 mediaport.Referencer（catalog/content/ticket 引用計數）
// 引用注入方向：消費方在自己的 providers 裏以依賴參數收 port.Referencer
// （media 不 import 消費方——無環）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/mods/media/port"

	"github.com/google/wire"
)

// ProviderSet media providers。
var ProviderSet = wire.NewSet(
	NewMediaRepo,
	NewAdminMediaService,
	NewStoreMediaService,
	wire.Bind(new(port.Uploader), new(*MediaRepo)),
	wire.Bind(new(port.Referencer), new(*MediaRepo)),
)
