package procurement

// 工具函數（base64 行編碼：多卡密密文 JSON 序列化）。

import "encoding/base64"

func encodeB64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func decodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
