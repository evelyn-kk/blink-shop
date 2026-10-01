package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// passwordHasher 负责密码哈希：新密码一律 bcrypt；兼容上游遗留的 SHA-256 hex，登录成功后升级。
type passwordHasher struct {
	cost      int
	dummyOnce sync.Once
	dummy     []byte
}

func newPasswordHasher(cost int) *passwordHasher {
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	return &passwordHasher{cost: cost}
}

func (p *passwordHasher) hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), p.cost)
	return string(b), err
}

func isBcrypt(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")
}

// verify 校验密码。遗留 SHA-256 hex 用常量时间比较。
func (p *passwordHasher) verify(hash, password string) bool {
	hash = strings.TrimSpace(hash)
	if isBcrypt(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	if len(hash) != sha256.Size*2 {
		return false
	}
	sum := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(hash)), []byte(hex.EncodeToString(sum[:]))) == 1
}

// needsRehash：遗留哈希或 bcrypt 成本低于当前设置时，登录成功后重新哈希。
func (p *passwordHasher) needsRehash(hash string) bool {
	if !isBcrypt(hash) {
		return true
	}
	cost, err := bcrypt.Cost([]byte(hash))
	return err != nil || cost < p.cost
}

// burn 在用户不存在时做一次同等成本的 bcrypt 比较，使响应时间与“密码错误”一致，避免枚举用户名。
func (p *passwordHasher) burn(password string) {
	p.dummyOnce.Do(func() {
		p.dummy, _ = bcrypt.GenerateFromPassword([]byte("blink-shop-dummy-password"), p.cost)
	})
	_ = bcrypt.CompareHashAndPassword(p.dummy, []byte(password))
}
