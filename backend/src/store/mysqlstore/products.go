package mysqlstore

import (
	"context"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 商家商品管理 ----------

func (s *Store) ListMerchantProducts(ctx context.Context, q store.MerchantProductQuery) ([]domain.Product, int, error) {
	where := ` FROM products WHERE merchant_id = ? AND status <> 'deleted'`
	args := []any{q.MerchantID}
	if q.Status != "" {
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		where += ` AND name LIKE ?`
		args = append(args, "%"+likeEscape.Replace(kw)+"%")
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+productColumns+where+
		` ORDER BY updated_at DESC, product_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	for i := range out {
		if out[i].SKUs, err = s.listSKUs(ctx, out[i].ProductID); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

func (s *Store) CreateProduct(ctx context.Context, p domain.Product) (domain.Product, error) {
	if err := store.PrepareProduct(&p); err != nil {
		return domain.Product{}, err
	}
	now := s.timestamp()
	p.CreatedAt, p.UpdatedAt = now, now
	err := s.WithTx(ctx, func(ctx context.Context) error {
		if err := s.insertProduct(ctx, p); err != nil {
			return err
		}
		for i := range p.SKUs {
			p.SKUs[i].CreatedAt, p.SKUs[i].UpdatedAt = now, now
			if err := s.insertSKU(ctx, p.SKUs[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Product{}, err
	}
	return s.GetProduct(ctx, p.ProductID)
}

func (s *Store) UpdateProduct(ctx context.Context, productID string, fn func(p *domain.Product) error) (domain.Product, error) {
	err := s.WithTx(ctx, func(ctx context.Context) error {
		p, err := scanProduct(s.q(ctx).QueryRowContext(ctx,
			`SELECT `+productColumns+` FROM products WHERE product_id = ? FOR UPDATE`, productID))
		if err != nil {
			return err
		}
		if p.SKUs, err = s.listSKUs(ctx, productID); err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, sku := range p.SKUs {
			existing[sku.SkuID] = true
		}
		if err := fn(&p); err != nil {
			return err
		}
		p.ProductID = productID // fn 不能改主键
		if err := store.PrepareProduct(&p); err != nil {
			return err
		}
		now := s.timestamp()
		p.UpdatedAt = now
		if err := s.writeProduct(ctx, p); err != nil {
			return err
		}
		keep := make([]any, 0, len(p.SKUs)+1)
		keep = append(keep, productID)
		for _, sku := range p.SKUs {
			keep = append(keep, sku.SkuID)
			sku.UpdatedAt = now
			if existing[sku.SkuID] {
				err = s.writeSKU(ctx, sku)
			} else {
				sku.CreatedAt = now
				err = s.insertSKU(ctx, sku)
			}
			if err != nil {
				return err
			}
		}
		_, err = s.q(ctx).ExecContext(ctx, `DELETE FROM product_skus WHERE product_id = ? AND sku_id NOT IN (?`+
			strings.Repeat(`, ?`, len(keep)-2)+`)`, keep...)
		return mapErr(err)
	})
	if err != nil {
		return domain.Product{}, err
	}
	return s.GetProduct(ctx, productID)
}

// writeProduct 覆盖商品的全部可变列（不改 merchant_id、created_at）。
func (s *Store) writeProduct(ctx context.Context, p domain.Product) error {
	images, _ := jsonArray(p.ImageURLs)
	tags, _ := jsonArray(p.Tags)
	points, _ := jsonArray(p.SellingPoints)
	risks, _ := jsonArray(p.RiskNotes)
	attrs, _ := jsonArray(p.Attributes)
	suitable, _ := jsonArray(p.SuitableFor)
	unsuitable, _ := jsonArray(p.NotSuitableFor)
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE products SET category_id = ?, name = ?, brand = ?, image_url = ?,
		image_urls_json = ?, price = ?, market_price = ?, stock_quantity = ?, stock_status = ?, tags_json = ?,
		selling_points_json = ?, recommend_reason = ?, risk_notes_json = ?, attributes_json = ?, suitable_for_json = ?,
		not_suitable_for_json = ?, description = ?, status = ?, sort_order = ?, updated_at = ?
		WHERE product_id = ?`,
		p.CategoryID, p.Name, p.Brand, p.ImageURL, images, p.Price, p.MarketPrice, p.StockQuantity, p.StockStatus, tags,
		points, p.RecommendReason, risks, attrs, suitable, unsuitable, p.Description, p.Status, p.SortOrder, p.UpdatedAt,
		p.ProductID)
	return mapErr(err)
}

func (s *Store) writeSKU(ctx context.Context, sku domain.ProductSKU) error {
	specs, err := jsonObject(sku.Specs)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `UPDATE product_skus SET sku_name = ?, price = ?, stock_quantity = ?,
		stock_status = ?, specs_json = ?, is_default = ?, updated_at = ? WHERE sku_id = ? AND product_id = ?`,
		sku.SkuName, sku.Price, sku.StockQuantity, sku.StockStatus, specs, sku.IsDefault, sku.UpdatedAt, sku.SkuID, sku.ProductID)
	return mapErr(err)
}
