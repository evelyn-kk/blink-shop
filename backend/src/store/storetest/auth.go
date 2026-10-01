package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// clockSetter 两种实现都提供，用来让 token 过期而不必真的等待。
type clockSetter interface {
	SetClock(now func() time.Time)
}

// manualClock 是并发安全的可调时钟。
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func useClock(t *testing.T, s store.Store) *manualClock {
	t.Helper()
	cs, ok := s.(clockSetter)
	if !ok {
		t.Fatalf("%T 不支持 SetClock", s)
	}
	c := &manualClock{now: time.Now().UTC().Truncate(time.Millisecond)}
	cs.SetClock(c.Now)
	t.Cleanup(func() { cs.SetClock(func() time.Time { return time.Now().UTC() }) })
	return c
}

func authCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"AuthTokenRoundTrip", testAuthTokenRoundTrip},
		{"AuthTokenExpires", testAuthTokenExpires},
		{"AuthTokenRevoked", testAuthTokenRevoked},
		{"AuthTokenShowsAccountStatus", testAuthTokenStatus},
		{"UsernameCaseInsensitive", testUsernameCaseInsensitive},
		{"UpdatePasswordHash", testUpdatePasswordHash},
		{"UpdateProfileAndContact", testUpdateProfileAndContact},
		{"ConcurrentPartialContactUpdates", testConcurrentContactUpdates},
		{"SoftDeleteAccountRevokesTokens", testSoftDeleteAccount},
	}
}

func testAuthTokenRoundTrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	clock := useClock(t, s)
	acc := mustCreate(t, s, ctx, newUser("tok_user"))

	token, expires, err := s.CreateAuthToken(ctx, acc.AccountID, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 43 || !expires.Equal(clock.Now().Add(24*time.Hour)) {
		t.Fatalf("token = %q (len %d), expires = %v", token, len(token), expires)
	}
	other, _, _ := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	if other == token {
		t.Fatal("两次生成了相同的 token")
	}
	got, err := s.GetAccountByToken(ctx, token)
	if err != nil || got.AccountID != acc.AccountID {
		t.Fatalf("GetAccountByToken = %+v, %v", got, err)
	}
	// 伪造 token、空 token、把摘要当 token 用都查不到。
	for _, bad := range []string{"", "forged-token", token + "x", store.HashAuthToken(token)} {
		_, err := s.GetAccountByToken(ctx, bad)
		expectNotFound(t, err, "token "+bad)
	}
	if _, _, err := s.CreateAuthToken(ctx, acc.AccountID, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("ttl=0: err = %v, want ErrInvalid", err)
	}
}

func testAuthTokenExpires(t *testing.T, s store.Store) {
	ctx := context.Background()
	clock := useClock(t, s)
	acc := mustCreate(t, s, ctx, newUser("exp_user"))
	token, _, err := s.CreateAuthToken(ctx, acc.AccountID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute - time.Millisecond)
	if _, err := s.GetAccountByToken(ctx, token); err != nil {
		t.Fatalf("过期前应有效: %v", err)
	}
	clock.Advance(time.Millisecond)
	_, err = s.GetAccountByToken(ctx, token)
	expectNotFound(t, err, "到期的 token")
}

func testAuthTokenRevoked(t *testing.T, s store.Store) {
	ctx := context.Background()
	acc := mustCreate(t, s, ctx, newUser("rev_user"))
	keep, _, _ := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	drop, _, _ := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	if err := s.DeleteAuthToken(ctx, drop); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetAccountByToken(ctx, drop)
	expectNotFound(t, err, "已撤销的 token")
	if _, err := s.GetAccountByToken(ctx, keep); err != nil {
		t.Fatalf("撤销一个 token 不应影响同账户的其他 token: %v", err)
	}
	// 重复撤销、撤销不存在的 token 都不报错。
	if err := s.DeleteAuthToken(ctx, drop); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAuthToken(ctx, "never-issued"); err != nil {
		t.Fatal(err)
	}
}

// testAuthTokenStatus：token 仍能查到 inactive/risk 账户，并带上当前状态，由认证层决定放行范围。
func testAuthTokenStatus(t *testing.T, s store.Store) {
	ctx := context.Background()
	for _, status := range []domain.EntityStatus{domain.StatusInactive, domain.StatusRisk} {
		data := store.SeedData{Accounts: []store.SeedAccount{{PasswordHash: "h", Account: domain.Account{
			AccountID: "acct_" + string(status), Username: "status_" + string(status), DisplayName: "状态账户",
			Role: domain.RoleUser, Status: status,
		}}}}
		if _, err := s.ApplySeed(ctx, data); err != nil {
			t.Fatal(err)
		}
		token, _, err := s.CreateAuthToken(ctx, "acct_"+string(status), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAccountByToken(ctx, token)
		if err != nil || got.Status != status {
			t.Fatalf("status %s: got %+v, %v", status, got, err)
		}
	}
}

// testUsernameCaseInsensitive：与 MySQL 排序规则一致，用户名唯一性和查找都不区分大小写。
func testUsernameCaseInsensitive(t *testing.T, s store.Store) {
	ctx := context.Background()
	acc := mustCreate(t, s, ctx, newUser("Case_User"))
	got, _, err := s.GetAccountByUsername(ctx, "case_user")
	if err != nil || got.AccountID != acc.AccountID || got.Username != "Case_User" {
		t.Fatalf("按小写查找 = %+v, %v", got, err)
	}
	if _, err := s.CreateAccount(ctx, newUser("CASE_USER")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("大小写不同的重复用户名: err = %v, want ErrConflict", err)
	}
}

func testUpdatePasswordHash(t *testing.T, s store.Store) {
	ctx := context.Background()
	acc := mustCreate(t, s, ctx, newUser("pw_user"))
	if err := s.UpdatePasswordHash(ctx, acc.AccountID, "$2a$10$newhash"); err != nil {
		t.Fatal(err)
	}
	if _, hash, _ := s.GetAccountByUsername(ctx, "pw_user"); hash != "$2a$10$newhash" {
		t.Fatalf("hash = %q", hash)
	}
	if err := s.UpdatePasswordHash(ctx, acc.AccountID, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("空哈希: err = %v, want ErrInvalid", err)
	}
	expectNotFound(t, s.UpdatePasswordHash(ctx, "acct_missing", "h"), "不存在的账户")
}

func testUpdateProfileAndContact(t *testing.T, s store.Store) {
	ctx := context.Background()
	clock := useClock(t, s)
	acc := mustCreate(t, s, ctx, newUser("profile_user"))
	clock.Advance(time.Second)

	name := "新昵称"
	got, err := s.UpdateAccountProfile(ctx, acc.AccountID, store.ProfileUpdate{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != name || got.AvatarURL != "" || !got.UpdatedAt.After(acc.UpdatedAt) {
		t.Fatalf("只改昵称: %+v", got)
	}
	avatar := "/api/v1/uploads/avatar/a.png"
	got, err = s.UpdateAccountProfile(ctx, acc.AccountID, store.ProfileUpdate{AvatarURL: &avatar})
	if err != nil || got.DisplayName != name || got.AvatarURL != avatar {
		t.Fatalf("只改头像: %+v, %v", got, err)
	}
	// 与当前值相同的更新也要成功（MySQL 此时 RowsAffected 为 0）。
	if _, err := s.UpdateAccountProfile(ctx, acc.AccountID, store.ProfileUpdate{AvatarURL: &avatar}); err != nil {
		t.Fatalf("相同值更新: %v", err)
	}

	phone, email, empty := "13800000000", "p@example.com", ""
	got, err = s.UpdateAccountContact(ctx, acc.AccountID, store.ContactUpdate{Phone: &phone, Email: &email})
	if err != nil || got.Phone != phone || got.Email != email {
		t.Fatalf("改联系方式: %+v, %v", got, err)
	}
	got, err = s.UpdateAccountContact(ctx, acc.AccountID, store.ContactUpdate{Email: &empty})
	if err != nil || got.Phone != phone || got.Email != "" {
		t.Fatalf("只清空邮箱: %+v, %v", got, err)
	}
	got, err = s.UpdateAccountContact(ctx, acc.AccountID, store.ContactUpdate{Phone: &empty})
	if err != nil || got.Phone != "" {
		t.Fatalf("清空手机号: %+v, %v", got, err)
	}
	reread, _ := s.GetAccount(ctx, acc.AccountID)
	if reread.DisplayName != name || reread.AvatarURL != avatar {
		t.Fatalf("重新读取: %+v", reread)
	}

	_, err = s.UpdateAccountProfile(ctx, "acct_missing", store.ProfileUpdate{DisplayName: &name})
	expectNotFound(t, err, "不存在的账户改资料")
	_, err = s.UpdateAccountContact(ctx, "acct_missing", store.ContactUpdate{Phone: &empty})
	expectNotFound(t, err, "不存在的账户改联系方式")
}

// testConcurrentContactUpdates：并发地分别改手机号和邮箱，两者都应保留。
func testConcurrentContactUpdates(t *testing.T, s store.Store) {
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		acc := mustCreate(t, s, ctx, newUser(fmt.Sprintf("race_contact_%d", i)))
		phone, email := "13900000000", "race@example.com"
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, in := range []store.ContactUpdate{{Phone: &phone}, {Email: &email}} {
			wg.Add(1)
			go func(in store.ContactUpdate) {
				defer wg.Done()
				_, err := s.UpdateAccountContact(ctx, acc.AccountID, in)
				errs <- err
			}(in)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, _ := s.GetAccount(ctx, acc.AccountID)
		if got.Phone != phone || got.Email != email {
			t.Fatalf("第 %d 次：并发部分更新丢失 phone=%q email=%q", i, got.Phone, got.Email)
		}
	}
}

func testSoftDeleteAccount(t *testing.T, s store.Store) {
	ctx := context.Background()
	acc := mustCreate(t, s, ctx, newUser("del_user"))
	other := mustCreate(t, s, ctx, newUser("del_other"))
	t1, _, _ := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	t2, _, _ := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	t3, _, _ := s.CreateAuthToken(ctx, other.AccountID, time.Hour)

	if err := s.SoftDeleteAccount(ctx, acc.AccountID); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{t1, t2} {
		_, err := s.GetAccountByToken(ctx, tok)
		expectNotFound(t, err, "注销后的 token")
	}
	if _, err := s.GetAccountByToken(ctx, t3); err != nil {
		t.Fatalf("其他账户的 token 不应受影响: %v", err)
	}
	_, err := s.GetAccount(ctx, acc.AccountID)
	expectNotFound(t, err, "注销后的账户")
	_, _, err = s.GetAccountByUsername(ctx, "del_user")
	expectNotFound(t, err, "注销后按用户名查")
	expectNotFound(t, s.SoftDeleteAccount(ctx, acc.AccountID), "重复注销")
	name := "x"
	_, err = s.UpdateAccountProfile(ctx, acc.AccountID, store.ProfileUpdate{DisplayName: &name})
	expectNotFound(t, err, "注销后改资料")
	if _, err := s.CreateAccount(ctx, newUser("del_user")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("注销后复用用户名: err = %v, want ErrConflict", err)
	}

	// 注销与发 token 在同一事务里回滚时，账户保持原样。
	keep := mustCreate(t, s, ctx, newUser("del_rollback"))
	boom := errors.New("boom")
	err = s.WithTx(ctx, func(ctx context.Context) error {
		if err := s.SoftDeleteAccount(ctx, keep.AccountID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.GetAccount(ctx, keep.AccountID); err != nil {
		t.Fatalf("回滚后账户应仍在: %v", err)
	}
}
