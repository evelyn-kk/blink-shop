package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// 各实体 ID 前缀，便于从日志里一眼看出 ID 类型。
const (
	PrefixAccount   = "acct_"
	PrefixMerchant  = "m_"
	PrefixCategory  = "c_"
	PrefixProduct   = "p_"
	PrefixSKU       = "sku_"
	PrefixFile      = "file_"
	PrefixDocument  = "doc_"
	PrefixChunk     = "ck_"
	PrefixCartItem  = "ci_"
	PrefixCheckout  = "chk_"
	PrefixOrder     = "o_"
	PrefixOrderItem = "oi_"
	PrefixPayment   = "pay_"
	PrefixPromotion = "promo_"
	PrefixCoupon    = "coupon_"
	PrefixUserCoup  = "uc_"
	PrefixReview    = "rv_"
	PrefixSession   = "s_"
	PrefixMessage   = "msg_"
	PrefixRun       = "run_"
	PrefixTrace     = "tr_"
	PrefixTraceEvt  = "te_"
	PrefixPrompt    = "prompt_"
	PrefixPublish   = "pub_"
	PrefixAudit     = "aud_"
)

// NewID 生成 “前缀 + 24 位随机 hex” 的不可猜测 ID。
func NewID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:]) // Go 1.24 起 crypto/rand.Read 不会返回错误
	return prefix + hex.EncodeToString(b[:])
}
