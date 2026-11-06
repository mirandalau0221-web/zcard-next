package admincmd

// 配置裝載（與 main 的 loadBootstrap 同源邏輯，避免相互依賴）。

import (
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/conf"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

func scanConf(dir string, out *conf.Bootstrap) error {
	c := config.New(config.WithSource(file.NewSource(dir)))
	defer c.Close()
	if err := c.Load(); err != nil {
		return fmt.Errorf("讀取配置失敗（目錄 %s）: %w", dir, err)
	}
	return c.Scan(out)
}
