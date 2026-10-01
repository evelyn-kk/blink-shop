// Command gen-sample-images 生成演示商品图和商家 logo（PNG，640×640），输出到 assets/catalog。
// 图片只用几何图形绘制，结果确定、无外部依赖；同一商品的主图和副图配色不同，便于 8.3 图搜区分。
//
//	go run ./cmd/gen-sample-images
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const size = 640

type canvas struct{ *image.RGBA }

func newCanvas(bg color.RGBA) canvas {
	c := canvas{image.NewRGBA(image.Rect(0, 0, size, size))}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// 竖向轻微渐变，避免纯色背景。
			k := float64(y) / size * 0.12
			c.SetRGBA(x, y, shade(bg, 1-k))
		}
	}
	return c
}

func shade(c color.RGBA, k float64) color.RGBA {
	f := func(v uint8) uint8 { return uint8(math.Max(0, math.Min(255, float64(v)*k))) }
	return color.RGBA{f(c.R), f(c.G), f(c.B), 255}
}

// fill 把 inside(x,y) 为真的像素涂成 col；bounds 限定扫描范围。
func (c canvas) fill(x0, y0, x1, y1 int, col color.RGBA, inside func(x, y float64) bool) {
	for y := max(y0, 0); y < min(y1, size); y++ {
		for x := max(x0, 0); x < min(x1, size); x++ {
			if inside(float64(x)+0.5, float64(y)+0.5) {
				c.SetRGBA(x, y, col)
			}
		}
	}
}

func (c canvas) roundRect(x0, y0, x1, y1, r int, col color.RGBA) {
	fr := float64(r)
	c.fill(x0, y0, x1, y1, col, func(x, y float64) bool {
		cx := math.Max(float64(x0)+fr, math.Min(x, float64(x1)-fr))
		cy := math.Max(float64(y0)+fr, math.Min(y, float64(y1)-fr))
		return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= fr*fr
	})
}

func (c canvas) ellipse(cx, cy, rx, ry int, col color.RGBA) {
	c.fill(cx-rx, cy-ry, cx+rx+1, cy+ry+1, col, func(x, y float64) bool {
		dx, dy := (x-float64(cx))/float64(rx), (y-float64(cy))/float64(ry)
		return dx*dx+dy*dy <= 1
	})
}

func (c canvas) circle(cx, cy, r int, col color.RGBA) { c.ellipse(cx, cy, r, r, col) }

// polygon 用奇偶规则填充多边形。
func (c canvas) polygon(col color.RGBA, pts ...[2]float64) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range pts {
		minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
		minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
	}
	c.fill(int(minX), int(minY), int(maxX)+1, int(maxY)+1, col, func(x, y float64) bool {
		in := false
		for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
			a, b := pts[i], pts[j]
			if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
				in = !in
			}
		}
		return in
	})
}

// palette 是一张图的配色：背景、主体、细节。
type palette struct{ bg, body, detail color.RGBA }

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 255}
}

func phone(c canvas, p palette, cameras int) {
	c.roundRect(200, 90, 440, 560, 40, shade(p.body, 0.75)) // 阴影边框
	c.roundRect(210, 100, 430, 550, 34, p.body)
	c.roundRect(226, 130, 414, 520, 18, p.detail)
	for i := 0; i < cameras; i++ {
		c.circle(260+i*34, 112, 9, shade(p.body, 0.5))
	}
}

func earbuds(c canvas, p palette) {
	c.roundRect(140, 410, 500, 520, 50, shade(p.body, 0.85)) // 充电盒
	for _, x := range []int{240, 400} {
		c.roundRect(x-18, 250, x+18, 400, 18, p.body)
		c.circle(x, 230, 62, p.body)
		c.circle(x, 230, 30, p.detail)
	}
}

func mouse(c canvas, p palette) {
	c.ellipse(320, 330, 130, 200, p.body)
	c.roundRect(316, 140, 324, 300, 4, p.detail)
	c.roundRect(304, 190, 336, 250, 14, p.detail)
}

func keyboard(c canvas, p palette) {
	c.roundRect(70, 200, 570, 450, 26, p.body)
	for row := 0; row < 4; row++ {
		for col := 0; col < 11; col++ {
			x, y := 98+col*41, 228+row*50
			c.roundRect(x, y, x+33, y+40, 6, p.detail)
		}
	}
	c.roundRect(200, 428-12, 440, 428+8, 6, p.detail)
}

func lamp(c canvas, p palette) {
	c.ellipse(320, 540, 140, 30, p.body)
	c.polygon(p.body, [2]float64{300, 540}, [2]float64{340, 540}, [2]float64{430, 230}, [2]float64{400, 220})
	c.polygon(p.body, [2]float64{330, 140}, [2]float64{480, 120}, [2]float64{540, 290}, [2]float64{250, 330})
	c.ellipse(395, 310, 135, 22, p.detail)
}

func speaker(c canvas, p palette) {
	c.roundRect(170, 120, 470, 540, 60, p.body)
	c.circle(320, 380, 110, p.detail)
	c.circle(320, 380, 46, p.body)
	c.circle(320, 200, 34, p.detail)
}

func powerbank(c canvas, p palette) {
	c.roundRect(170, 110, 470, 540, 36, p.body)
	c.polygon(p.detail, [2]float64{340, 190}, [2]float64{250, 350}, [2]float64{315, 350}, [2]float64{290, 470},
		[2]float64{390, 300}, [2]float64{325, 300})
	for i := 0; i < 4; i++ {
		c.circle(260+i*40, 505, 9, p.detail)
	}
}

func logoDigital(c canvas, p palette) {
	c.circle(320, 320, 220, p.body)
	c.circle(320, 320, 150, p.bg)
	c.circle(320, 320, 80, p.detail)
}

func logoHome(c canvas, p palette) {
	c.polygon(p.body, [2]float64{320, 110}, [2]float64{560, 320}, [2]float64{80, 320})
	c.roundRect(150, 300, 490, 540, 10, p.body)
	c.roundRect(280, 390, 360, 540, 8, p.detail)
}

type item struct {
	name string
	draw func(canvas, palette)
	main palette
}

var (
	digitalBG = rgb(0xE8F0FE)
	homeBG    = rgb(0xEAF6EC)
)

func items() []item {
	ph := func(n int) func(canvas, palette) { return func(c canvas, p palette) { phone(c, p, n) } }
	return []item{
		{"products/p_seed_nova", ph(2), palette{digitalBG, rgb(0x1F2937), rgb(0x3B82F6)}},
		{"products/p_seed_vista", ph(3), palette{rgb(0xEDE9FE), rgb(0x4C1D95), rgb(0xA78BFA)}},
		{"products/p_seed_legacy", ph(1), palette{rgb(0xF3F4F6), rgb(0x6B7280), rgb(0xD1D5DB)}},
		{"products/p_seed_earbuds", earbuds, palette{rgb(0xE0F2FE), rgb(0xF9FAFB), rgb(0x0EA5E9)}},
		{"products/p_seed_mouse", mouse, palette{rgb(0xF1F5F9), rgb(0x475569), rgb(0xCBD5E1)}},
		{"products/p_seed_keyboard", keyboard, palette{rgb(0xFEF3C7), rgb(0x78350F), rgb(0xFDE68A)}},
		{"products/p_seed_lamp", lamp, palette{homeBG, rgb(0x166534), rgb(0xFEF9C3)}},
		{"products/p_seed_speaker", speaker, palette{rgb(0xFFE4E6), rgb(0x881337), rgb(0xFDA4AF)}},
		{"products/p_seed_powerbank", powerbank, palette{rgb(0xECFCCB), rgb(0x365314), rgb(0xBEF264)}},
		{"merchants/logo-digital", logoDigital, palette{digitalBG, rgb(0x2563EB), rgb(0x1E3A8A)}},
		{"merchants/logo-home", logoHome, palette{homeBG, rgb(0x15803D), rgb(0xFEF9C3)}},
	}
}

func write(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func main() {
	out := filepath.Join("assets", "catalog")
	n := 0
	for _, it := range items() {
		c := newCanvas(it.main.bg)
		it.draw(c, it.main)
		if err := write(filepath.Join(out, it.name+".png"), c); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n++
		if filepath.Dir(it.name) != "products" {
			continue
		}
		// 副图：深色背景、主体与细节互换，模拟另一角度/配色的实拍。
		alt := palette{bg: shade(it.main.body, 0.9), body: it.main.detail, detail: it.main.bg}
		c = newCanvas(alt.bg)
		it.draw(c, alt)
		if err := write(filepath.Join(out, it.name+"-2.png"), c); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n++
	}
	fmt.Printf("已生成 %d 张图片到 %s\n", n, out)
}
