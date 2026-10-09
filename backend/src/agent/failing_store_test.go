package agent

import (
	"context"
	"errors"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// failingStore 让购物车读取失败，用于验证内部错误不外泄。
type failingStore struct{ store.Store }

func (failingStore) ListCartLines(context.Context, string) ([]store.CartLine, error) {
	return nil, errors.New("secret dsn failure")
}
