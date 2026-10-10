package eval

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/assets"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
)

// ImageCase 是一条图片搜索用例：从 source（`catalog/` 下的商品图，或 `synthetic:<样式>` 生成的无关图片）按 transforms 变换后检索。
//   - expect_top：期望最相似的商品（recall@1）；通过条件是它出现在前 3（recall@3）；
//   - expect_none：不能返回任何商品（无关图片）；absent：不能出现的商品（下架、风控、删除的商品）。
//
// Tags 里的 held_out 表示该变换没有参与阈值标定。
type ImageCase struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	Transforms []string `json:"transforms"`
	ExpectTop  string   `json:"expect_top"`
	ExpectNone bool     `json:"expect_none"`
	Absent     []string `json:"absent"`
	Tags       []string `json:"tags"`
}

// ImageOptions 是图片搜索评测的输入；Index 为 nil 时用内存索引，Embedder 为 nil 时用本地特征。
type ImageOptions struct {
	Cases    string
	Index    imagesearch.Index
	Embedder imagevector.Embedder
	Version  string
	Config   map[string]string
}

const imageEvalK = 3

// RunImages 跑图片搜索用例。指标：recall@1、recall@3、MRR、held-out recall@3、无关图片的不返回率、同款（match 级）的准确率、
// 不可售商品泄漏次数。
func RunImages(ctx context.Context, o ImageOptions) (Report, error) {
	start := time.Now()
	emb := o.Embedder
	if emb == nil {
		emb = imagevector.Local{}
	}
	idx := o.Index
	rep := Report{Suite: "images", Version: o.Version, StartedAt: start.UTC(), Config: map[string]string{"embedder": emb.Name(), "index": "memory"}, Metrics: map[string]float64{}}
	if idx == nil {
		idx = imagesearch.NewMemoryIndex()
	} else {
		rep.Config["index"] = "milvus"
	}
	match, weak := emb.Thresholds()
	rep.Config["threshold_match"], rep.Config["threshold_weak"] = fmt.Sprint(match), fmt.Sprint(weak)
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	cases, err := readJSONL[ImageCase](o.Cases)
	if err != nil {
		return rep, err
	}
	st, err := newSeededStore(ctx)
	if err != nil {
		return rep, err
	}
	svc := imagesearch.New(emb, idx, imagesearch.NewSource(nil), st, nil, nil)
	stats, err := svc.Bootstrap(ctx)
	if err != nil {
		return rep, err
	}
	rep.Config["indexed_images"] = fmt.Sprint(stats.Images)

	var positives, hit1, hit3, heldOut, heldOutHit, negatives, negOK, matchTop, matchTopOK, leaks int
	var rr float64
	for i, c := range cases {
		data, err := caseImage(c, uint64(i+1))
		if err != nil {
			return rep, fmt.Errorf("case %s: %w", c.ID, err)
		}
		res, err := svc.Search(ctx, data, imagesearch.DefaultTopK)
		if err != nil {
			return rep, fmt.Errorf("case %s: %w", c.ID, err)
		}
		got := make([]string, 0, len(res.Items))
		for _, m := range res.Items {
			got = append(got, fmt.Sprintf("%s:%.3f:%s", m.Product.ProductID, m.Score, m.Level))
		}
		var problems []string
		for _, a := range c.Absent {
			for _, m := range res.Items {
				if m.Product.ProductID == a {
					problems = append(problems, "不应返回 "+a)
					leaks++
				}
			}
		}
		if c.ExpectNone {
			negatives++
			if len(res.Items) == 0 {
				negOK++
			} else {
				problems = append(problems, "无关图片不应返回商品")
			}
		}
		if c.ExpectTop != "" {
			positives++
			held := slices.Contains(c.Tags, "held_out")
			if held {
				heldOut++
			}
			rank := 0
			for j, m := range res.Items {
				if m.Product.ProductID == c.ExpectTop {
					rank = j + 1
					break
				}
			}
			if rank == 1 {
				hit1++
			}
			if rank > 0 {
				rr += 1 / float64(rank)
			}
			if rank > 0 && rank <= imageEvalK {
				hit3++
				if held {
					heldOutHit++
				}
			} else {
				problems = append(problems, fmt.Sprintf("%s 不在前 %d", c.ExpectTop, imageEvalK))
			}
			if len(res.Items) > 0 && res.Items[0].Level == imagesearch.LevelMatch {
				matchTop++
				if res.Items[0].Product.ProductID == c.ExpectTop {
					matchTopOK++
				}
			}
		}
		rep.Total++
		if len(problems) == 0 {
			rep.Passed++
		} else {
			rep.Failures = append(rep.Failures, Failure{ID: c.ID, Query: c.Source + " " + strings.Join(c.Transforms, "+"), Problems: problems, Got: got})
		}
	}
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 1
		}
		return round(float64(a) / float64(b))
	}
	rep.Metrics["recall_at_1"] = ratio(hit1, positives)
	rep.Metrics["recall_at_3"] = ratio(hit3, positives)
	rep.Metrics["held_out_recall_at_3"] = ratio(heldOutHit, heldOut)
	if positives > 0 {
		rep.Metrics["mrr"] = round(rr / float64(positives))
	}
	rep.Metrics["no_match_accuracy"] = ratio(negOK, negatives)
	rep.Metrics["match_precision"] = ratio(matchTopOK, matchTop)
	rep.Metrics["match_rate"] = ratio(matchTop, positives)
	rep.Metrics["inactive_leaks"] = float64(leaks)
	rep.finish(start)
	return rep, nil
}

// caseImage 生成用例图片（PNG 字节）。
func caseImage(c ImageCase, seed uint64) ([]byte, error) {
	if spec, ok := strings.CutPrefix(c.Source, "synthetic:"); ok {
		img, err := imagevector.Synthetic(spec, 480, seed)
		if err != nil {
			return nil, err
		}
		return imagevector.EncodePNG(img)
	}
	raw, err := fs.ReadFile(assets.FS, c.Source)
	if err != nil {
		return nil, err
	}
	img, _, err := imagevector.Decode(raw)
	if err != nil {
		return nil, err
	}
	out, err := imagevector.Transform(img, c.Transforms, seed)
	if err != nil {
		return nil, err
	}
	return imagevector.EncodePNG(out)
}
