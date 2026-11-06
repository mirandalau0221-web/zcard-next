package captcha

// HTTP transport（生成端點；免鑑權——註冊/登錄前可用）。

import (
	"context"

	storefrontv1 "github.com/NovaWorks/zcard-next/server/api/storefront/v1"

	"google.golang.org/protobuf/types/known/emptypb"
)

// StoreCaptchaService HTTP 服務（實現 storefront proto）。
type StoreCaptchaService struct {
	storefrontv1.UnimplementedStoreCaptchaServiceServer
	svc *Service
}

// NewStoreCaptchaService 構造。
func NewStoreCaptchaService(svc *Service) *StoreCaptchaService {
	return &StoreCaptchaService{svc: svc}
}

// GetImage 生成驗證碼。
func (s *StoreCaptchaService) GetImage(ctx context.Context, _ *emptypb.Empty) (*storefrontv1.CaptchaImage, error) {
	img, err := s.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &storefrontv1.CaptchaImage{CaptchaId: img.ID, ImageBase64: img.ImageBase64, Length: uint32(img.Length), Charset: img.Charset}, nil
}
