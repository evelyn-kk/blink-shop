package memstore_test

import (
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return memstore.New() })
}
