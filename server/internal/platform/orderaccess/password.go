// Package orderaccess shares the password gate across order details and delivery.
package orderaccess

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/NovaWorks/zcard-next/server/internal/platform/crypto"
	"github.com/go-kratos/kratos/v3/errors"
)

const MaxFailures = 5
const LockTTL = 30 * time.Minute

type State struct {
	Failures  int
	ExpiresAt time.Time
}

func (s State) Locked() bool { return s.Failures >= MaxFailures && s.ExpiresAt.After(time.Now()) }

type Gate interface {
	FetchPasswordState(context.Context, string) (State, error)
	RecordFetchFailure(context.Context, string) (State, error)
	ResetFetchFailures(context.Context, string) error
}

func passwordError(s State) error {
	if s.Locked() {
		seconds := max(1, int(math.Ceil(time.Until(s.ExpiresAt).Seconds())))
		minutes := (seconds + 59) / 60
		return errors.New(429, "order.FETCH_LOCKED", fmt.Sprintf("查詢失敗已達 5 次，此訂單在當前網絡下已暫時鎖定，請約 %d 分鐘後重試", minutes)).WithMetadata(map[string]string{
			"retry_after_seconds": strconv.Itoa(seconds), "remaining_attempts": "0",
		})
	}
	remaining := max(0, MaxFailures-s.Failures)
	return errors.NotFound("order.NOT_FOUND", fmt.Sprintf("訂單不存在或查詢密碼錯誤，還可嘗試 %d 次；連續失敗 5 次將鎖定 30 分鐘", remaining)).WithMetadata(map[string]string{"remaining_attempts": strconv.Itoa(remaining)})
}

// PublicError preserves actionable password feedback without leaking storage errors.
func PublicError(err error) error {
	e := errors.FromError(err)
	if e.Reason == "order.NOT_FOUND" || e.Reason == "order.FETCH_LOCKED" || e.Reason == "order.QUERY_PASSWORD_REQUIRED" || e.Reason == "order.ACCESS_REQUIRED" {
		return e
	}
	return errors.InternalServer("order.QUERY_FAILED", "訂單查詢暫時失敗，請稍後重試")
}

func Verify(ctx context.Context, gate Gate, no, hash, password, ip, contactEmail string) error {
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed != nil {
		if v4 := parsed.To4(); v4 != nil {
			ip = v4.String()
		} else {
			ip = parsed.Mask(net.CIDRMask(64, 128)).String()
		}
	}
	key := "fetch:" + ip + ":" + no
	if gate != nil {
		state, err := gate.FetchPasswordState(ctx, key)
		if err != nil {
			return err
		}
		if state.Locked() {
			return passwordError(state)
		}
	}
	// Opening a payment return URL without a saved password is not a failed guess.
	if password == "" {
		return errors.BadRequest("order.QUERY_PASSWORD_REQUIRED", "請輸入下單時設置的查詢密碼")
	}
	// Guest orders: the contact email entered at checkout is accepted as the
	// access credential, so buyers can view delivery with just their email —
	// no query password and no emailed verification code needed. The order
	// number itself is unguessable, and a wrong email still counts as a
	// failed attempt below. contactEmail is only supplied for guest orders.
	if contactEmail != "" {
		if email, err := EmailAddress(password); err == nil && email == contactEmail {
			return verifySuccess(ctx, gate, key)
		}
	}
	if hash == "" || !crypto.VerifyPassword(hash, password) {
		state := State{Failures: 1}
		if gate != nil {
			var err error
			state, err = gate.RecordFetchFailure(ctx, key)
			if err != nil {
				return err
			}
		}
		return passwordError(state)
	}
	return verifySuccess(ctx, gate, key)
}

// verifySuccess clears the failure counter after a valid credential, mirroring
// the original password path (a concurrent fifth failure still locks).
func verifySuccess(ctx context.Context, gate Gate, key string) error {
	if gate != nil {
		if err := gate.ResetFetchFailures(ctx, key); err != nil {
			return err
		}
		// A concurrent fifth failure must not be cleared by this successful attempt.
		state, err := gate.FetchPasswordState(ctx, key)
		if err != nil {
			return err
		}
		if state.Locked() {
			return passwordError(state)
		}
	}
	return nil
}
