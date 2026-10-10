package imagevector

import (
	"context"
	"image"
	"math"
	"strconv"
)

// Local 是不依赖外部服务的图像特征：把图片缩到 32×32 后取三部分并按权重拼接（各自 L2 归一化）：
//   - 颜色：色相 12 档 × 饱和度 2 档 + 灰度 4 档的直方图（28 维），接近背景色的像素降权；
//   - 边缘布局：Sobel 梯度幅值池化到 10×10（100 维），反色、换背景色不变；
//   - 边缘方向：梯度方向（模 180°，反色不变）8 个区间 × 2×2 象限（32 维），对平移和轻微裁剪更稳。
//
// 取特征前先找出主体（与四周背景色差别明显的区域，扩成正方形），对裁剪、缩放、加边框更稳。
//
// 它衡量的是外形和配色的相似，不理解语义；适合本地演示、测试和评测，真实照片找同款应换多模态模型。
type Local struct{}

const (
	localGrid  = 32
	edgeCells  = 10
	orientBins = 8
	hueBins    = 12
	grayBins   = 4
	colorDim   = hueBins*2 + grayBins
	localDim   = colorDim + edgeCells*edgeCells + orientBins*4
	// 每个格子最多取 6×6 个采样点，大图的耗时有上界。
	cellSamples = 6
)

var localWeights = [3]float64{0.4, 0.4, 0.2}

func (Local) Dim() int     { return localDim }
func (Local) Name() string { return "local:color-edge-v1:" + strconv.Itoa(localDim) }

// Thresholds 由 quality/data/eval/image_search.jsonl 标定：同款经裁剪、缩放、亮度、压缩、翻转、加边框、噪声后多在 0.6 以上，
// 噪声、棋盘格等无关图片不超过 0.5（纯色图没有主体，直接不返回）。但同款和外形相近的不同商品（几款手机）分数重叠，
// 所以“同款”门槛定得偏高（0.80），其余只当相近商品展示。换成多模态模型后应重新评测再调。
func (Local) Thresholds() (float64, float64) { return 0.80, 0.58 }

func (Local) Embed(ctx context.Context, data []byte) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img, _, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if !hasSubject(img) {
		return nil, ErrNoSubject
	}
	return Features(img), nil
}

// hasSubject：画面里有没有和背景明显不同的内容（纯色、几乎空白的图片没有可比的主体）。
func hasSubject(img image.Image) bool {
	b := img.Bounds()
	var first [3]float64
	diff := 0
	for i := 0; i < focusGrid; i++ {
		for j := 0; j < focusGrid; j++ {
			x, y := b.Min.X+(2*j+1)*b.Dx()/(2*focusGrid), b.Min.Y+(2*i+1)*b.Dy()/(2*focusGrid)
			r, g, bl := rgbAt(img, x, y)
			c := [3]float64{r, g, bl}
			if i == 0 && j == 0 {
				first = c
			}
			if colorDist(c, first) > focusDiff {
				diff++
			}
		}
	}
	return diff >= focusGrid*focusGrid/100
}

// Features 计算本地特征（已 L2 归一化）。
func Features(img image.Image) []float32 {
	full := img.Bounds()
	bounds, bg := focus(img)
	var r, g, b, gray [localGrid][localGrid]float64
	w, h := bounds.Dx(), bounds.Dy()
	for cy := 0; cy < localGrid; cy++ {
		y0, y1 := bounds.Min.Y+cy*h/localGrid, bounds.Min.Y+(cy+1)*h/localGrid
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for cx := 0; cx < localGrid; cx++ {
			x0, x1 := bounds.Min.X+cx*w/localGrid, bounds.Min.X+(cx+1)*w/localGrid
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb, n float64
			for _, y := range samplePoints(y0, y1) {
				for _, x := range samplePoints(x0, x1) {
					n++
					if !(image.Point{x, y}).In(full) {
						// 正方形取景超出原图的部分按背景色补
						sr, sg, sb = sr+bg[0], sg+bg[1], sb+bg[2]
						continue
					}
					pr, pg, pb := rgbAt(img, x, y)
					sr, sg, sb = sr+pr, sg+pg, sb+pb
				}
			}
			r[cy][cx], g[cy][cx], b[cy][cx] = sr/n, sg/n, sb/n
			gray[cy][cx] = 0.299*r[cy][cx] + 0.587*g[cy][cx] + 0.114*b[cy][cx]
		}
	}

	// 背景色取取景框四周格子的平均；接近背景的像素在颜色直方图里只占很小的权重，让主体的颜色起主导作用。
	var br, bgc, bb, bn float64
	for i := 0; i < localGrid; i++ {
		for _, p := range [][2]int{{0, i}, {localGrid - 1, i}, {i, 0}, {i, localGrid - 1}} {
			br, bgc, bb = br+r[p[0]][p[1]], bgc+g[p[0]][p[1]], bb+b[p[0]][p[1]]
			bn++
		}
	}
	mean := [3]float64{br / bn, bgc / bn, bb / bn}
	color := make([]float64, colorDim)
	for y := 0; y < localGrid; y++ {
		for x := 0; x < localGrid; x++ {
			w := 1.0
			if colorDist([3]float64{r[y][x], g[y][x], b[y][x]}, mean) < 0.15 {
				w = 0.1
			}
			color[colorBin(r[y][x], g[y][x], b[y][x])] += w
		}
	}

	edges := make([]float64, edgeCells*edgeCells)
	orient := make([]float64, orientBins*4)
	inner := localGrid - 2
	for y := 1; y < localGrid-1; y++ {
		for x := 1; x < localGrid-1; x++ {
			gx := gray[y-1][x+1] + 2*gray[y][x+1] + gray[y+1][x+1] - gray[y-1][x-1] - 2*gray[y][x-1] - gray[y+1][x-1]
			gy := gray[y+1][x-1] + 2*gray[y+1][x] + gray[y+1][x+1] - gray[y-1][x-1] - 2*gray[y-1][x] - gray[y-1][x+1]
			mag := math.Hypot(gx, gy)
			if mag < 1e-3 {
				continue
			}
			ex, ey := (x-1)*edgeCells/inner, (y-1)*edgeCells/inner
			edges[ey*edgeCells+ex] += mag
			theta := math.Atan2(gy, gx)
			if theta < 0 {
				theta += math.Pi
			}
			ob := int(theta / math.Pi * orientBins)
			if ob >= orientBins {
				ob = orientBins - 1
			}
			q := 0
			if x >= localGrid/2 {
				q++
			}
			if y >= localGrid/2 {
				q += 2
			}
			orient[q*orientBins+ob] += mag
		}
	}

	out := make([]float32, 0, localDim)
	for i, part := range [][]float64{color, edges, orient} {
		scale := math.Sqrt(localWeights[i]) / norm(part)
		if math.IsInf(scale, 0) || math.IsNaN(scale) {
			scale = 0
		}
		for _, v := range part {
			out = append(out, float32(v*scale))
		}
	}
	return normalize(out)
}

// rgbAt 取像素颜色（0–1），透明像素按白底合成。
func rgbAt(img image.Image, x, y int) (float64, float64, float64) {
	pr, pg, pb, pa := img.At(x, y).RGBA()
	a := float64(pa) / 0xffff
	return float64(pr)/0xffff + (1 - a), float64(pg)/0xffff + (1 - a), float64(pb)/0xffff + (1 - a)
}

const (
	focusGrid = 64
	// focusDiff 是与背景色的差（三通道绝对差之和）超过多少算主体。
	focusDiff = 0.12
)

// focus 找出画面里的主体：先用四周估计背景色，取与背景差别明显的区域的外接框，扩成正方形（保留主体的长宽比）。
// 做两轮——加了边框的图第一轮只去掉边框，第二轮才落到商品上。主体太小或几乎占满画面时用整张图。
func focus(img image.Image) (image.Rectangle, [3]float64) {
	rect := img.Bounds()
	var bg [3]float64
	for pass := 0; pass < 2; pass++ {
		var cells [focusGrid][focusGrid][3]float64
		w, h := rect.Dx(), rect.Dy()
		for cy := 0; cy < focusGrid; cy++ {
			y := rect.Min.Y + (2*cy+1)*h/(2*focusGrid)
			for cx := 0; cx < focusGrid; cx++ {
				x := rect.Min.X + (2*cx+1)*w/(2*focusGrid)
				if (image.Point{x, y}).In(img.Bounds()) {
					r, g, b := rgbAt(img, x, y)
					cells[cy][cx] = [3]float64{r, g, b}
				} else {
					cells[cy][cx] = bg
				}
			}
		}
		var sum [3]float64
		n := 0.0
		for i := 0; i < focusGrid; i++ {
			for _, c := range [][3]float64{cells[0][i], cells[focusGrid-1][i], cells[i][0], cells[i][focusGrid-1]} {
				sum[0], sum[1], sum[2] = sum[0]+c[0], sum[1]+c[1], sum[2]+c[2]
				n++
			}
		}
		bg = [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
		minX, minY, maxX, maxY, count := focusGrid, focusGrid, -1, -1, 0
		for cy := 0; cy < focusGrid; cy++ {
			for cx := 0; cx < focusGrid; cx++ {
				if colorDist(cells[cy][cx], bg) > focusDiff {
					minX, minY, maxX, maxY = min(minX, cx), min(minY, cy), max(maxX, cx), max(maxY, cy)
					count++
				}
			}
		}
		if count < focusGrid*focusGrid/100 {
			break
		}
		bw, bh := maxX-minX+1, maxY-minY+1
		if bw*bh > focusGrid*focusGrid*95/100 {
			break
		}
		side := float64(max(bw, bh)) * 1.1
		cx, cy := float64(minX+maxX+1)/2, float64(minY+maxY+1)/2
		sx, sy := float64(w)/focusGrid, float64(h)/focusGrid
		// 正方形按原图像素计（格子可能不是正方形）
		half := side / 2 * math.Max(sx, sy)
		ox, oy := float64(rect.Min.X)+cx*sx, float64(rect.Min.Y)+cy*sy
		next := image.Rect(int(ox-half), int(oy-half), int(ox+half+0.5), int(oy+half+0.5))
		if next.Dx() < minSide || next.Dy() < minSide {
			break
		}
		rect = next
	}
	return rect, bg
}

func colorDist(a, b [3]float64) float64 {
	return math.Abs(a[0]-b[0]) + math.Abs(a[1]-b[1]) + math.Abs(a[2]-b[2])
}

func samplePoints(lo, hi int) []int {
	n := hi - lo
	if n <= cellSamples {
		pts := make([]int, n)
		for i := range pts {
			pts[i] = lo + i
		}
		return pts
	}
	pts := make([]int, cellSamples)
	for i := range pts {
		pts[i] = lo + (2*i+1)*n/(2*cellSamples)
	}
	return pts
}

// colorBin：低饱和或很暗的颜色按亮度分 4 档；其余按色相 12 档 × 饱和度 2 档。
func colorBin(r, g, b float64) int {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	v, d := mx, mx-mn
	s := 0.0
	if mx > 0 {
		s = d / mx
	}
	if s < 0.2 || v < 0.15 {
		i := int(v * grayBins)
		return hueBins*2 + min(max(i, 0), grayBins-1)
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	if h < 0 {
		h += 6
	}
	hb := int(h / 6 * hueBins)
	if hb >= hueBins {
		hb = hueBins - 1
	}
	sb := 0
	if s >= 0.55 {
		sb = 1
	}
	return hb*2 + sb
}

func norm(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// Cosine 是两个向量的余弦相似度（维度不同返回 0）。
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
