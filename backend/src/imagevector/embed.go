package imagevector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // 注册解码器：上传允许 jpeg/png/webp/gif/bmp
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// ErrInvalidImage 表示数据不是可解码的图片，或尺寸超出限制。
var ErrInvalidImage = errors.New("imagevector: invalid image")

// ErrNoSubject 表示图片里没有可识别的主体（纯色、几乎空白），检索按“没有相似商品”处理。
var ErrNoSubject = errors.New("imagevector: no subject in image")

const (
	// MaxImageBytes 是参与图搜的单张图片上限（与上传上限无关，取较小者）。
	MaxImageBytes = 10 << 20
	maxSide       = 8000
	maxPixels     = 40_000_000
	minSide       = 8
)

// Embedder 把一张图片转成向量。同一个 Embedder 的维度固定。
type Embedder interface {
	Embed(ctx context.Context, data []byte) ([]float32, error)
	Dim() int
	// Name 是提供方 + 模型 + 维度，写进索引统计和评测报告。
	Name() string
	// Thresholds 是余弦相似度阈值：≥ match 视为同款 / 同类（可以推荐），≥ weak 视为相近（只作参考），更低的丢弃。
	Thresholds() (match, weak float64)
}

// Decode 先读图片头校验格式和尺寸（挡住解压炸弹），再完整解码。
func Decode(data []byte) (image.Image, string, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return nil, "", fmt.Errorf("%w: size %d", ErrInvalidImage, len(data))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidImage, err)
	}
	if cfg.Width < minSide || cfg.Height < minSide || cfg.Width > maxSide || cfg.Height > maxSide || cfg.Width*cfg.Height > maxPixels {
		return nil, "", fmt.Errorf("%w: %dx%d", ErrInvalidImage, cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidImage, err)
	}
	return img, format, nil
}
