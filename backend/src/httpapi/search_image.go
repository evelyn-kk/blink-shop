package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
)

var (
	ErrImageSearchDown = &APIError{Status: http.StatusServiceUnavailable, Code: "image_search_unavailable", Message: "图片搜索暂不可用，请稍后再试或用文字描述商品"}
	ErrInvalidImage    = &APIError{Status: http.StatusBadRequest, Code: "invalid_image", Message: "图片无法识别，请换一张清晰的 JPG / PNG 图片", Field: "file_id"}
	ErrNotImageFile    = &APIError{Status: http.StatusBadRequest, Code: "unsupported_file_type", Message: "只能用图片搜索", Field: "file_id"}
)

type searchImageRequest struct {
	FileID string `json:"file_id"`
	TopK   *int   `json:"top_k"`
}

type imageMatchView struct {
	Product         productCard `json:"product"`
	Score           float64     `json:"score"`
	Level           string      `json:"level"`
	MatchedImageURL string      `json:"matched_image_url"`
}

type searchImageResponse struct {
	Status   string           `json:"status"`
	Embedder string           `json:"embedder"`
	Items    []imageMatchView `json:"items"`
}

// handleSearchImage 用本人上传的图片找相似的可售商品。别人的文件和不存在的文件同样返回 404。
func (s *Server) handleSearchImage(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in searchImageRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !fileIDPattern.MatchString(in.FileID) {
		writeError(w, fieldError("file_id", "请先上传图片，再用返回的 file_id 搜索"))
		return
	}
	k := imagesearch.DefaultTopK
	if in.TopK != nil {
		if *in.TopK < 1 || *in.TopK > imagesearch.MaxTopK {
			writeError(w, fieldError("top_k", "top_k 取 1–20"))
			return
		}
		k = *in.TopK
	}
	if s.imageSearch == nil {
		writeError(w, ErrImageSearchDown)
		return
	}
	res, err := s.imageSearch.SearchFile(r.Context(), acc.AccountID, in.FileID, k)
	if err != nil {
		writeError(w, s.imageSearchError(r, err))
		return
	}
	out := searchImageResponse{Status: res.Status, Embedder: res.Embedder, Items: make([]imageMatchView, 0, len(res.Items))}
	for _, m := range res.Items {
		out.Items = append(out.Items, imageMatchView{Product: toProductCard(m.Product), Score: round4(m.Score), Level: m.Level, MatchedImageURL: m.ImageURL})
	}
	s.logger.InfoContext(r.Context(), "image search", "request_id", requestIDFromContext(r.Context()), "account_id", acc.AccountID,
		"file_id", in.FileID, "status", res.Status, "items", len(res.Items), "candidates", res.Candidates, "dropped", res.Dropped, "embedder", res.Embedder)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) imageSearchError(r *http.Request, err error) error {
	switch {
	case errors.Is(err, imagesearch.ErrFileNotFound):
		return ErrFileNotFound
	case errors.Is(err, imagesearch.ErrNotImage):
		return ErrNotImageFile
	case errors.Is(err, imagesearch.ErrInvalidImage):
		return ErrInvalidImage
	case errors.Is(err, imagesearch.ErrStorage):
		s.logger.WarnContext(r.Context(), "image search: object storage", "request_id", requestIDFromContext(r.Context()), "error", err)
		return ErrObjectStorageDown
	case errors.Is(err, imagesearch.ErrIndex):
		s.logger.WarnContext(r.Context(), "image search: index", "request_id", requestIDFromContext(r.Context()), "error", err)
		return ErrImageSearchDown
	}
	s.logger.ErrorContext(r.Context(), "image search failed", "request_id", requestIDFromContext(r.Context()), "error", err)
	return ErrInternal
}

func round4(f float64) float64 { return float64(int64(f*10000+0.5)) / 10000 }

// serverImageSearch 让导购每次调用时读取 Server 当前的图片搜索（未配置时返回 ErrUnavailable，导购如实说明）。
type serverImageSearch struct{ s *Server }

func (a serverImageSearch) SearchFile(ctx context.Context, accountID, fileID string, k int) (imagesearch.Result, error) {
	if a.s.imageSearch == nil {
		return imagesearch.Result{}, imagesearch.ErrUnavailable
	}
	return a.s.imageSearch.SearchFile(ctx, accountID, fileID, k)
}

var _ agent.ImageSearcher = serverImageSearch{}
