package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"sync"
	"time"
	"unicode/utf8"
)

// MockRecognizer 不调外部服务：不管音频内容，按收到的音频时长逐步“识别”出固定的 Text——
// 每 0.5 秒音频多出一个字的 partial，End 后给出 final（没有收到音频时 final 为空）。用于本地演示和测试。
type MockRecognizer struct {
	Text string
}

const mockBytesPerRune = BytesPerSecond / 2

func (m MockRecognizer) Name() string { return "mock" }

func (m MockRecognizer) Start(ctx context.Context, _ string) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := m.Text
	if text == "" {
		text = "推荐一款降噪耳机"
	}
	return &mockStream{text: []rune(text), events: make(chan Event, 64), done: make(chan struct{})}, nil
}

type mockStream struct {
	mu       sync.Mutex
	text     []rune
	received int
	shown    int
	ended    bool
	closed   bool
	events   chan Event
	done     chan struct{}
}

func (s *mockStream) Send(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ended {
		return ErrClosed
	}
	s.received += len(pcm)
	if n := min(s.received/mockBytesPerRune, len(s.text)); n > s.shown {
		s.shown = n
		s.events <- Event{Type: "partial", Text: string(s.text[:n])}
	}
	return nil
}

func (s *mockStream) End() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ended {
		return ErrClosed
	}
	s.ended = true
	final := ""
	if s.received > 0 {
		final = string(s.text)
	}
	s.events <- Event{Type: "final", Text: final}
	close(s.events)
	return nil
}

func (s *mockStream) Recv() (Event, error) {
	select {
	case ev, ok := <-s.events:
		if !ok {
			return Event{}, ErrClosed
		}
		return ev, nil
	case <-s.done:
		return Event{}, ErrClosed
	}
}

func (s *mockStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

// MockSynthesizer 生成一段提示音 WAV（16kHz 单声道，每个字 60ms，最长 8 秒），不调外部服务。用于本地演示和测试。
type MockSynthesizer struct {
	// Delay 模拟供应商耗时（测试用）。
	Delay time.Duration
}

func (MockSynthesizer) Name() string  { return "mock" }
func (MockSynthesizer) Voice() string { return "mock" }

func (m MockSynthesizer) Synthesize(ctx context.Context, text, _ string) (Audio, error) {
	if m.Delay > 0 {
		select {
		case <-ctx.Done():
			return Audio{}, ctx.Err()
		case <-time.After(m.Delay):
		}
	}
	d := min(time.Duration(utf8.RuneCountInString(text))*60*time.Millisecond, 8*time.Second)
	return Audio{Data: toneWAV(d), ContentType: "audio/wav"}, nil
}

// toneWAV 生成 440Hz 正弦波的 WAV，首尾 20ms 淡入淡出。
func toneWAV(d time.Duration) []byte {
	n := int(d.Seconds() * SampleRate)
	pcm := make([]int16, n)
	fade := SampleRate / 50
	for i := range pcm {
		amp := 0.3
		if i < fade {
			amp *= float64(i) / float64(fade)
		} else if n-i < fade {
			amp *= float64(n-i) / float64(fade)
		}
		pcm[i] = int16(amp * math.MaxInt16 * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
	}
	var buf bytes.Buffer
	dataLen := uint32(n * 2)
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, 36+dataLen)
	buf.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(SampleRate), uint32(BytesPerSecond), uint16(2), uint16(16)} {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataLen)
	_ = binary.Write(&buf, binary.LittleEndian, pcm)
	return buf.Bytes()
}
