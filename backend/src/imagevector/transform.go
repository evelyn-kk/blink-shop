package imagevector

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

// 图片变换：评测和测试用它们从商品图生成“用户拍的照片”（裁剪、缩放、亮度、压缩、翻转、加边框、噪声），
// 以及与商品无关的负样本（纯色、噪声、棋盘格）。变换名写在评测集里，格式 name 或 name:参数。

// Transform 按 spec 依次变换图片，例如 "crop:0.8"、"scale:0.4"、"bright:0.7"、"jpeg:50"、"flip"、"pad:0.2"、"noise:0.05"、"rotate:90"。
func Transform(img image.Image, specs []string, seed uint64) (image.Image, error) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for _, spec := range specs {
		name, arg, _ := strings.Cut(spec, ":")
		f := 0.0
		if arg != "" {
			v, err := strconv.ParseFloat(arg, 64)
			if err != nil {
				return nil, fmt.Errorf("transform %q: %w", spec, err)
			}
			f = v
		}
		switch name {
		case "crop":
			img = cropCenter(img, f, rng)
		case "scale":
			img = scale(img, f)
		case "bright":
			img = mapRGB(img, func(c float64) float64 { return c * f })
		case "contrast":
			img = mapRGB(img, func(c float64) float64 { return (c-0.5)*f + 0.5 })
		case "jpeg":
			out, err := jpegRoundTrip(img, int(f))
			if err != nil {
				return nil, err
			}
			img = out
		case "flip":
			img = flip(img)
		case "rotate":
			img = rotate90(img, int(f)/90)
		case "pad":
			img = pad(img, f)
		case "noise":
			img = noise(img, f, rng)
		default:
			return nil, fmt.Errorf("unknown transform %q", name)
		}
	}
	return img, nil
}

// Synthetic 生成负样本图片："solid:#rrggbb"、"noise"、"checker"、"stripes"。
func Synthetic(spec string, size int, seed uint64) (image.Image, error) {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	name, arg, _ := strings.Cut(spec, ":")
	switch name {
	case "solid":
		c, err := parseHex(arg)
		if err != nil {
			return nil, err
		}
		draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
	case "noise":
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				img.Set(x, y, color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255})
			}
		}
	case "checker":
		cell := max(size/8, 1)
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if (x/cell+y/cell)%2 == 0 {
					img.Set(x, y, color.RGBA{230, 120, 30, 255})
				} else {
					img.Set(x, y, color.RGBA{20, 140, 60, 255})
				}
			}
		}
	case "stripes":
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if (x/max(size/16, 1))%2 == 0 {
					img.Set(x, y, color.RGBA{250, 220, 40, 255})
				} else {
					img.Set(x, y, color.RGBA{200, 30, 120, 255})
				}
			}
		}
	default:
		return nil, fmt.Errorf("unknown synthetic image %q", spec)
	}
	return img, nil
}

// EncodePNG / EncodeJPEG 把图片编码成上传用的字节。
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Bytes(), err
}

func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// cropCenter 保留 f 比例的区域，中心随机偏移最多剩余空间的一半。
func cropCenter(img image.Image, f float64, rng *rand.Rand) image.Image {
	src := toRGBA(img)
	b := src.Bounds()
	w, h := max(int(float64(b.Dx())*f), minSide), max(int(float64(b.Dy())*f), minSide)
	slackX, slackY := b.Dx()-w, b.Dy()-h
	x0 := slackX/2 + int((rng.Float64()-0.5)*float64(slackX)/2)
	y0 := slackY/2 + int((rng.Float64()-0.5)*float64(slackY)/2)
	return src.SubImage(image.Rect(x0, y0, x0+w, y0+h))
}

func scale(img image.Image, f float64) image.Image {
	b := img.Bounds()
	w, h := max(int(float64(b.Dx())*f), minSide), max(int(float64(b.Dy())*f), minSide)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func mapRGB(img image.Image, f func(float64) float64) image.Image {
	src := toRGBA(img)
	clamp := func(v float64) uint8 { return uint8(min(max(v, 0), 1)*255 + 0.5) }
	for i := 0; i+3 < len(src.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			src.Pix[i+c] = clamp(f(float64(src.Pix[i+c]) / 255))
		}
	}
	return src
}

func jpegRoundTrip(img image.Image, q int) (image.Image, error) {
	data, err := EncodeJPEG(img, q)
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(data))
}

func flip(img image.Image) image.Image {
	src := toRGBA(img)
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(b.Dx()-1-x, y, src.At(x, y))
		}
	}
	return out
}

func rotate90(img image.Image, times int) image.Image {
	out := toRGBA(img)
	for ; times%4 > 0; times-- {
		b := out.Bounds()
		next := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				next.Set(b.Dy()-1-y, x, out.At(x, y))
			}
		}
		out = next
	}
	return out
}

// pad 四周加 f 比例的白边（模拟把商品放在更大的画面里拍）。
func pad(img image.Image, f float64) image.Image {
	b := img.Bounds()
	px, py := int(float64(b.Dx())*f), int(float64(b.Dy())*f)
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*px, b.Dy()+2*py))
	draw.Draw(out, out.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(px, py, px+b.Dx(), py+b.Dy()), img, b.Min, draw.Src)
	return out
}

func noise(img image.Image, amp float64, rng *rand.Rand) image.Image {
	src := toRGBA(img)
	for i := 0; i+3 < len(src.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			v := float64(src.Pix[i+c])/255 + (rng.Float64()*2-1)*amp
			src.Pix[i+c] = uint8(min(max(v, 0), 1)*255 + 0.5)
		}
	}
	return src
}

func parseHex(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(s, "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return color.RGBA{}, fmt.Errorf("bad color %q", s)
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}
