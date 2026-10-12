package mysqlstore

import (
	"context"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// BulkInsertProducts 批量写入商品和规格（每 batch 件一条多行 INSERT，每批一个事务）。只给大目录生成器用：
// 不做“已存在则跳过”，主键冲突直接报错。
func (s *Store) BulkInsertProducts(ctx context.Context, ps []domain.Product, batch int) error {
	if batch <= 0 {
		batch = 500
	}
	for start := 0; start < len(ps); start += batch {
		chunk := ps[start:min(start+batch, len(ps))]
		err := s.WithTx(ctx, func(ctx context.Context) error {
			var prows, srows []string
			var pargs, sargs []any
			for i := range chunk {
				p := chunk[i]
				if err := store.PrepareProduct(&p); err != nil {
					return err
				}
				images, _ := jsonArray(p.ImageURLs)
				tags, _ := jsonArray(p.Tags)
				points, _ := jsonArray(p.SellingPoints)
				risks, _ := jsonArray(p.RiskNotes)
				attrs, _ := jsonArray(p.Attributes)
				suitable, _ := jsonArray(p.SuitableFor)
				unsuitable, _ := jsonArray(p.NotSuitableFor)
				prows = append(prows, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
				pargs = append(pargs, p.ProductID, p.MerchantID, p.CategoryID, p.Name, p.Brand, p.ImageURL, images,
					p.Price, p.MarketPrice, p.StockQuantity, p.StockStatus, tags, points, p.RecommendReason,
					risks, attrs, suitable, unsuitable, p.Description, p.Status, p.SortOrder, s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt))
				for _, sku := range p.SKUs {
					specs, err := jsonObject(sku.Specs)
					if err != nil {
						return err
					}
					srows = append(srows, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
					sargs = append(sargs, sku.SkuID, sku.ProductID, sku.SkuName, sku.Price, sku.StockQuantity, sku.StockStatus, specs, sku.IsDefault,
						s.orNow(sku.CreatedAt), s.orNow(sku.UpdatedAt))
				}
			}
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO products (`+productColumns+`) VALUES `+strings.Join(prows, ","), pargs...); err != nil {
				return mapErr(err)
			}
			if len(srows) > 0 {
				if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO product_skus
					(sku_id, product_id, sku_name, price, stock_quantity, stock_status, specs_json, is_default, created_at, updated_at) VALUES `+
					strings.Join(srows, ","), sargs...); err != nil {
					return mapErr(err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
