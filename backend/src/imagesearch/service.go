package imagesearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var (
	// ErrFileNotFound：文件不存在或不属于当前账户（两者不区分，避免探测别人的文件）。
	ErrFileNotFound = errors.New("imagesearch: file not found")
	// ErrNotImage：文件不是图片。
	ErrNotImage = errors.New("imagesearch: file is not an image")
	// ErrStorage：对象存储不可用或对象丢失。
	ErrStorage = errors.New("imagesearch: object storage unavailable")
	// ErrUnavailable：没有配置图片搜索。
	ErrUnavailable = errors.New("imagesearch: not configured")
	// ErrIndex：向量索引不可用。
	ErrIndex = errors.New("imagesearch: index unavailable")
	// ErrInvalidImage：图片无法解码或尺寸超限。
	ErrInvalidImage = imagevector.ErrInvalidImage
)

const (
	// MaxImagesPerProduct：每个商品最多索引的图片数（主图 + 前几张详情图）。
	MaxImagesPerProduct = 4
	MaxTopK             = 20
	DefaultTopK         = 6
)

// Match 等级：同款 / 相似（只作参考）。
const (
	LevelMatch   = "match"
	LevelSimilar = "similar"
)

// 检索结果状态。
const (
	StatusMatched = "matched" // 最相似的商品达到同款阈值
	StatusSimilar = "similar" // 只有相近的商品
	StatusNoMatch = "no_match"
)

// Service 组合 Embedder、索引、商品图读取和 Store。任一必需部分为 nil 时 New 返回 nil，上层按“图搜不可用”处理。
type Service struct {
	embedder imagevector.Embedder
	index    Index
	loader   Loader
	store    store.Store
	objects  objectstore.Store
	logger   *slog.Logger
}

// New 创建服务。objects 可以为 nil（只能建索引、不能按上传文件检索）。
func New(e imagevector.Embedder, idx Index, loader Loader, st store.Store, objects objectstore.Store, logger *slog.Logger) *Service {
	if e == nil || idx == nil || loader == nil || st == nil {
		return nil
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{embedder: e, index: idx, loader: loader, store: st, objects: objects, logger: logger}
}

// EmbedderName 是当前 Embedder 的名称（报告和响应里用）。
func (s *Service) EmbedderName() string { return s.embedder.Name() }

// ProductImages 是参与索引的商品图：主图在前，去重，最多 MaxImagesPerProduct 张。
func ProductImages(p domain.Product) []string {
	var out []string
	for _, u := range append([]string{p.ImageURL}, p.ImageURLs...) {
		u = strings.TrimSpace(u)
		if u == "" || contains(out, u) {
			continue
		}
		out = append(out, u)
		if len(out) == MaxImagesPerProduct {
			break
		}
	}
	return out
}

// IndexStats 是一次索引的结果：写入的图片数、因地址不允许跳过的、读取或解码失败的。
type IndexStats struct {
	Products int `json:"products"`
	Images   int `json:"images"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
	Removed  int `json:"removed"`
}

func (a *IndexStats) add(b IndexStats) {
	a.Products += b.Products
	a.Images += b.Images
	a.Skipped += b.Skipped
	a.Failed += b.Failed
	a.Removed += b.Removed
}

// IndexProduct 重建一个商品的图片向量。单张图读取或解码失败只计数，其余照常写入。
func (s *Service) IndexProduct(ctx context.Context, p domain.Product) (IndexStats, error) {
	var st IndexStats
	var rows []IndexedImage
	for _, u := range ProductImages(p) {
		data, err := s.loader.Load(ctx, u)
		if errors.Is(err, ErrSourceNotAllowed) {
			st.Skipped++
			continue
		}
		var vec []float32
		if err == nil {
			vec, err = s.embedder.Embed(ctx, data)
		}
		if err != nil {
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			st.Failed++
			s.logger.WarnContext(ctx, "image index: image failed", "product_id", p.ProductID, "image_url", u, "error", err)
			continue
		}
		rows = append(rows, IndexedImage{ProductID: p.ProductID, MerchantID: p.MerchantID, ImageURL: u, Vector: vec})
	}
	if err := s.index.ReplaceProduct(ctx, p.ProductID, rows); err != nil {
		return st, fmt.Errorf("%w: %v", ErrIndex, err)
	}
	st.Products, st.Images = 1, len(rows)
	return st, nil
}

// Remove 从索引删除商品的全部图片。
func (s *Service) Remove(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.index.DeleteProducts(ctx, ids); err != nil {
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	return nil
}

// maxSyncRounds：写完复查时发现商品又变了，最多重做的轮数。
const maxSyncRounds = 3

// Sync 按商品当前状态更新索引：公开可见的重建，不可见（下架、风控、删除、不存在）的删除。
// 每个商品写完后再读一次：图片列表或可见性在此期间变了（同时有别的进程在改、在同步）就按新状态重做，
// 不会把读取时的旧快照留在索引里。
func (s *Service) Sync(ctx context.Context, ids ...string) (IndexStats, error) {
	var total IndexStats
	for _, id := range ids {
		key, p, err := s.snapshot(ctx, id)
		if err != nil {
			return total, err
		}
		for round := 1; ; round++ {
			if p == nil {
				if err := s.Remove(ctx, id); err != nil {
					return total, err
				}
				total.Removed++
			} else {
				st, err := s.IndexProduct(ctx, *p)
				total.add(st)
				if err != nil {
					return total, err
				}
			}
			again, next, err := s.snapshot(ctx, id)
			if err != nil {
				return total, err
			}
			if again == key || round >= maxSyncRounds {
				break
			}
			key, p = again, next
		}
	}
	return total, nil
}

// snapshot 返回商品当前会写进索引的内容（可见性 + 图片列表）和商品本身；不可见时商品为 nil。
func (s *Service) snapshot(ctx context.Context, id string) (string, *domain.Product, error) {
	p, err := s.store.GetVisibleProduct(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return "hidden", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return p.MerchantID + "\x00" + strings.Join(ProductImages(p.Product), "\x00"), &p.Product, nil
}

const bootstrapPage = 100

// Bootstrap 为全部在售商品建图片索引（可重复执行：按商品整体覆盖，写完复查，见 Sync）。单个商品失败计入 Failed 并继续。
func (s *Service) Bootstrap(ctx context.Context) (IndexStats, error) {
	var total IndexStats
	for page := 1; ; page++ {
		items, n, err := s.store.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: page, PageSize: bootstrapPage}})
		if err != nil {
			return total, err
		}
		for _, it := range items {
			st, err := s.Sync(ctx, it.ProductID)
			total.add(st)
			if err != nil {
				if ctx.Err() != nil {
					return total, ctx.Err()
				}
				total.Failed++
				s.logger.WarnContext(ctx, "image index: product failed", "product_id", it.ProductID, "error", err)
			}
		}
		if page*bootstrapPage >= n {
			break
		}
	}
	return total, nil
}

// Match 是一个命中的商品（已按可见性重新取回）。
type Match struct {
	Product  store.CatalogProduct
	Score    float64
	ImageURL string // 最相似的那张商品图
	Level    string
}

// Result 是一次图片检索的结果。
type Result struct {
	Status   string
	Items    []Match
	Embedder string
	// Candidates 是参与合并的图片命中数；Dropped 是命中了但商品已不可售的数量（索引过期）。
	Candidates int
	Dropped    int
}

// ReadFile 读取 accountID 本人上传的图片文件。
func (s *Service) ReadFile(ctx context.Context, accountID, fileID string) ([]byte, error) {
	f, err := s.store.GetStoredFile(ctx, fileID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && f.AccountID != accountID) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(f.MimeType, "image/") {
		return nil, ErrNotImage
	}
	if f.SizeBytes > imagevector.MaxImageBytes {
		return nil, fmt.Errorf("%w: file larger than %d bytes", ErrInvalidImage, imagevector.MaxImageBytes)
	}
	if s.objects == nil {
		return nil, ErrStorage
	}
	obj, err := s.objects.Get(ctx, f.ObjectKey)
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStorage, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(io.LimitReader(obj, imagevector.MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStorage, err)
	}
	return data, nil
}

// SearchFile 用本人上传的图片检索。
func (s *Service) SearchFile(ctx context.Context, accountID, fileID string, k int) (Result, error) {
	data, err := s.ReadFile(ctx, accountID, fileID)
	if err != nil {
		return Result{}, err
	}
	return s.Search(ctx, data, k)
}

// Search 检索与图片相似的可售商品：按商品取最相似的一张图，低于相近阈值的丢弃，命中回 Store 按可见性取回。
func (s *Service) Search(ctx context.Context, data []byte, k int) (Result, error) {
	if k <= 0 {
		k = DefaultTopK
	}
	k = min(k, MaxTopK)
	vec, err := s.embedder.Embed(ctx, data)
	if errors.Is(err, imagevector.ErrNoSubject) {
		return Result{Status: StatusNoMatch, Embedder: s.embedder.Name(), Items: []Match{}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	hits, err := s.index.Search(ctx, vec, k*MaxImagesPerProduct+8)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrIndex, err)
	}
	match, weak := s.embedder.Thresholds()
	res := Result{Embedder: s.embedder.Name(), Candidates: len(hits), Items: []Match{}}
	best := map[string]Hit{}
	for _, h := range hits {
		if b, ok := best[h.ProductID]; !ok || h.Score > b.Score {
			best[h.ProductID] = h
		}
	}
	ranked := make([]Hit, 0, len(best))
	for _, h := range best {
		ranked = append(ranked, h)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ProductID < ranked[j].ProductID
	})
	for _, h := range ranked {
		if h.Score < weak || len(res.Items) == k {
			break
		}
		p, err := s.store.GetVisibleProduct(ctx, h.ProductID)
		if errors.Is(err, store.ErrNotFound) {
			res.Dropped++
			continue
		}
		if err != nil {
			return Result{}, err
		}
		level := LevelSimilar
		if h.Score >= match {
			level = LevelMatch
		}
		res.Items = append(res.Items, Match{Product: p, Score: h.Score, ImageURL: h.ImageURL, Level: level})
	}
	switch {
	case len(res.Items) == 0:
		res.Status = StatusNoMatch
	case res.Items[0].Level == LevelMatch:
		res.Status = StatusMatched
	default:
		res.Status = StatusSimilar
	}
	return res, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
