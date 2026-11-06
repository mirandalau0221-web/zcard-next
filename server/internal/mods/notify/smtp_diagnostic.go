package notify

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"syscall"

	notifyport "github.com/NovaWorks/zcard-next/server/internal/mods/notify/port"
	kerrors "github.com/go-kratos/kratos/v3/errors"
)

var smtpStageLabels = map[string]string{
	"configuration": "SMTP 配置檢查失敗", "connect": "SMTP 連接失敗", "greeting": "SMTP 握手失敗",
	"tls": "SMTP TLS 連接失敗", "starttls": "SMTP STARTTLS 失敗", "auth": "SMTP 認證失敗",
	"from": "SMTP 發件地址被拒絕", "recipient": "SMTP 收件地址被拒絕",
	"data": "SMTP DATA 失敗", "body": "SMTP 正文發送失敗", "accept": "SMTP 服務器拒絕郵件",
}
var smtpStageHints = map[string]string{
	"configuration": "核對服務器地址（不含協議和端口）、端口、發件郵箱、連接與認證方式，然後保存配置。",
	"connect":       "檢查服務器域名、端口、防火牆和服務器出站網絡；確認郵件服務正在監聽此端口。",
	"greeting":      "檢查該端口是否為 SMTP 服務；SSL/TLS 通常使用 465，STARTTLS 通常使用 587，普通 SMTP 通常使用 25。",
	"tls":           "核對 SSL/TLS 方式和端口，並檢查郵件服務器證書及其完整證書鏈。",
	"starttls":      "確認服務器及端口支持 STARTTLS；此方式要求升級成功，請勿使用只支持隱式 TLS 的端口。",
	"auth":          "核對 SMTP 用戶名、密碼或授權碼、認證方式，以及服務器是否已開啓 SMTP 登錄。",
	"from":          "核對發件郵箱是否屬於當前賬號、發件域名是否獲准，以及中繼發信權限。",
	"recipient":     "核對收件地址、服務器中繼策略和收件限制；部分服務商要求測試收件人已驗證。",
	"data":          "檢查服務器發信權限、限額和郵件大小限制，並查看郵件服務日誌。",
	"body":          "檢查網絡是否中斷或超時，再查看郵件服務器日誌。",
	"accept":        "按服務器返回的狀態碼檢查內容策略、發信限額、域名驗證和郵件服務日誌。",
}

// SMTPDiagnostic carries the failure stage and sanitized provider response.
// The test endpoint exposes these as Kratos error metadata for inline guidance.
type SMTPDiagnostic struct {
	Stage, Detail, Hint            string
	Code                           int
	Host, Security, Authentication string
	Port                           int
	TLSVerify                      bool
}

func (e *SMTPDiagnostic) Error() string { return smtpStageLabels[e.Stage] + ": " + e.Detail }

func smtpFailure(stage string, err error, cfg *notifyport.SMTPConfig) *SMTPDiagnostic {
	d := &SMTPDiagnostic{Stage: stage, Detail: err.Error(), Hint: smtpStageHints[stage]}
	var response *textproto.Error
	if errors.As(err, &response) {
		d.Code = response.Code
	}
	var dns *net.DNSError
	var network net.Error
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		d.Hint = "服務器域名無法解析；請檢查 SMTP 主機名和服務器 DNS 配置。"
	case errors.As(err, &network) && network.Timeout():
		d.Hint = "連接或服務器響應超時；檢查防火牆、出站端口，以及連接方式是否與端口匹配。"
	case errors.Is(err, syscall.ECONNREFUSED):
		d.Hint = "連接被拒絕；確認端口正確、郵件服務已啓動，並允許服務器訪問此端口。"
	case errors.As(err, &unknown):
		d.Hint = "證書不受信任（可能是自簽名或缺少證書鏈）；為郵件服務安裝可信證書及完整鏈。自建可信服務可自行選擇是否校驗證書。"
	case errors.As(err, &hostname):
		d.Hint = "證書中的域名與 SMTP 服務器地址不匹配；使用證書覆蓋的主機名或更新證書。"
	case errors.As(err, &certificate):
		d.Hint = "證書已過期、尚未生效或用途不正確；檢查服務器時間及郵件服務證書。"
	case errors.As(err, &record):
		d.Hint = "此端口返回的不是 TLS 握手；核對連接方式，隱式 SSL/TLS 與 STARTTLS 不能混用。"
	case d.Code == 530 || d.Code == 538:
		d.Hint = "服務器要求認證或加密；檢查用戶名和密碼，並選擇 STARTTLS 或 SSL/TLS。"
	case stage == "auth" && d.Code == 535:
		d.Hint = "服務器拒絕登錄；檢查賬號、密碼或 SMTP 專用授權碼，以及是否啓用了 SMTP 登錄。"
	case d.Code == 421 || d.Code == 450 || d.Code == 451 || d.Code == 452:
		d.Hint = "服務器暫時不可用、限流或資源不足；查看服務日誌與限額，稍後再試。"
	}
	if cfg != nil {
		d.Host, d.Port, d.Security, d.Authentication, d.TLSVerify = cfg.Host, cfg.Port, cfg.Security, cfg.Auth, cfg.TLSVerify
		if cfg.Password != "" {
			for _, secret := range []string{base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.Username + "\x00" + cfg.Password)), base64.StdEncoding.EncodeToString([]byte(cfg.Password)), cfg.Password} {
				d.Detail = strings.ReplaceAll(d.Detail, secret, "[已隱藏]")
			}
		}
	}
	return d
}

func smtpTestError(err error) *kerrors.Error {
	if errors.Is(err, ErrSkipped) {
		return kerrors.BadRequest("notify.EMAIL_NOT_READY", "郵件通道未配置").WithMetadata(map[string]string{
			"stage": "configuration", "stage_label": smtpStageLabels["configuration"], "hint": "請先保存 SMTP 服務器、端口、連接方式和發件賬號。",
		})
	}
	reply := kerrors.BadRequest("notify.EMAIL_TEST_FAILED", err.Error())
	var d *SMTPDiagnostic
	if errors.As(err, &d) {
		reply = reply.WithMetadata(map[string]string{
			"stage": d.Stage, "stage_label": smtpStageLabels[d.Stage], "hint": d.Hint, "smtp_code": fmt.Sprint(d.Code),
			"host": d.Host, "port": fmt.Sprint(d.Port), "security": d.Security, "authentication": d.Authentication, "tls_verify": fmt.Sprint(d.TLSVerify),
		})
	}
	return reply
}
