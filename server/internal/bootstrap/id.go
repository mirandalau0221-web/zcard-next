package bootstrap

// 雪花 ID 生成器（訂單號/工單號/退款單號）。

import (
	"github.com/NovaWorks/zcard-next/server/internal/platform/id"

	"github.com/google/wire"
)

var idProviderSet = wire.NewSet(NewIDGenerator)

// NewIDGenerator 構造（workerID 暫用 0——單實例部署；多實例由實例 ID 派生）。
func NewIDGenerator() (*id.Generator, error) {
	return id.NewGenerator(0)
}
