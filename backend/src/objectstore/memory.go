package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Memory 是线程安全的内存 Store，用于测试；SetFailPut / SetFailGet 用来做失败注入。
type Memory struct {
	mu      sync.Mutex
	objects map[string]memObject
	failPut error
	failGet error
}

type memObject struct {
	data        []byte
	contentType string
}

func NewMemory() *Memory {
	return &Memory{objects: map[string]memObject{}}
}

func (m *Memory) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	m.mu.Lock()
	fail := m.failPut
	m.mu.Unlock()
	if fail != nil {
		return fail
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("objectstore: 声明大小 %d 与实际 %d 不一致", size, len(data))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{data: data, contentType: contentType}
	return nil
}

func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet != nil {
		return nil, m.failGet
	}
	obj, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// SetFailPut / SetFailGet 设置后，对应操作返回该错误；传 nil 恢复正常。
func (m *Memory) SetFailPut(err error) { m.mu.Lock(); m.failPut = err; m.mu.Unlock() }
func (m *Memory) SetFailGet(err error) { m.mu.Lock(); m.failGet = err; m.mu.Unlock() }

// Keys 返回当前全部对象 key（已排序）。
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Object 返回对象内容和 Content-Type。
func (m *Memory) Object(key string) ([]byte, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	return obj.data, obj.contentType, ok
}
