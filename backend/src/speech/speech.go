// Package speech 是语音识别（实时转写）和语音合成的供应商抽象。凭据只在服务端配置里，客户端经后端代理使用，
// 拿不到任何密钥。供应商：mock（本地演示和测试，不调外部服务）、讯飞实时转写 / 在线合成、豆包合成。
package speech

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// 实时转写的音频格式：16kHz、单声道、16 位小端 PCM。
const (
	SampleRate     = 16000
	BytesPerSecond = SampleRate * 2
)

var (
	// ErrProvider 表示供应商连接或返回出错（不含凭据的说明在错误文本里）。
	ErrProvider = errors.New("speech: provider error")
	// ErrClosed 表示识别流已结束。
	ErrClosed = errors.New("speech: stream closed")
)

// Event 是识别结果：partial 为到目前为止的整句（会变化），final 为最终结果。
type Event struct {
	Type string // "partial" / "final"
	Text string
}

// Recognizer 创建实时识别流。
type Recognizer interface {
	Name() string
	Start(ctx context.Context, requestID string) (Stream, error)
}

// Stream 是一次实时识别：Send 发送 PCM，End 表示音频结束，Recv 依次读结果（全部结束后返回 ErrClosed）。
// Close 随时可调用，释放连接；可重复调用。
type Stream interface {
	Send(pcm []byte) error
	End() error
	Recv() (Event, error)
	Close() error
}

// Audio 是合成结果。
type Audio struct {
	Data        []byte
	ContentType string
}

// Synthesizer 把文本合成音频。
type Synthesizer interface {
	Name() string
	// Voice 是默认音色。
	Voice() string
	Synthesize(ctx context.Context, text, voice string) (Audio, error)
}

// ContentTypeOf 把编码名转成 Content-Type。
func ContentTypeOf(encoding string) string {
	switch strings.ToLower(encoding) {
	case "mp3", "lame":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "ogg", "opus", "ogg_opus":
		return "audio/ogg"
	case "pcm", "raw":
		return "audio/L16;rate=16000"
	}
	return "application/octet-stream"
}

// Redact 去掉错误文本里 URL 的查询串（签名、密钥都在查询串里），用于日志。
func Redact(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, "?"); i >= 0 {
		msg = msg[:i] + "?<redacted>"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = strings.ReplaceAll(msg, ue.URL, "<url>")
	}
	return msg
}
