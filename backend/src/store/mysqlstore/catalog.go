package mysqlstore

import (
	"context"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// ListCategories 返回扁平列表，按 (parent_id, sort_order, category_id) 排序；树形结构由上层组装。
func (s *Store) ListCategories(ctx context.Context) ([]domain.Category, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT category_id, parent_id, name, sort_order, created_at, updated_at
		FROM categories ORDER BY parent_id, sort_order, category_id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Category{}
	for rows.Next() {
		var c domain.Category
		if err := rows.Scan(&c.CategoryID, &c.ParentID, &c.Name, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

const productColumns = `product_id, merchant_id, category_id, name, brand, image_url, image_urls_json,
	price, market_price, stock_quantity, stock_status, tags_json, selling_points_json, recommend_reason,
	risk_notes_json, attributes_json, suitable_for_json, not_suitable_for_json, description, status,
	sort_order, created_at, updated_at`

// GetProduct 返回商品及其 SKU（按默认 SKU 优先、sku_id 排序）；不区分商品状态，可售判断由上层负责。
func (s *Store) GetProduct(ctx context.Context, productID string) (domain.Product, error) {
	var (
		p                                                        domain.Product
		images, tags, points, risks, attrs, suitable, unsuitable []byte
	)
	err := s.q(ctx).QueryRowContext(ctx, `SELECT `+productColumns+` FROM products WHERE product_id = ?`, productID).Scan(
		&p.ProductID, &p.MerchantID, &p.CategoryID, &p.Name, &p.Brand, &p.ImageURL, &images,
		&p.Price, &p.MarketPrice, &p.StockQuantity, &p.StockStatus, &tags, &points, &p.RecommendReason,
		&risks, &attrs, &suitable, &unsuitable, &p.Description, &p.Status,
		&p.SortOrder, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.Product{}, mapErr(err)
	}
	for _, col := range []struct {
		name string
		raw  []byte
		dst  any
	}{
		{"image_urls_json", images, &p.ImageURLs},
		{"tags_json", tags, &p.Tags},
		{"selling_points_json", points, &p.SellingPoints},
		{"risk_notes_json", risks, &p.RiskNotes},
		{"attributes_json", attrs, &p.Attributes},
		{"suitable_for_json", suitable, &p.SuitableFor},
		{"not_suitable_for_json", unsuitable, &p.NotSuitableFor},
	} {
		if err := fromJSON(col.name, col.raw, col.dst); err != nil {
			return domain.Product{}, err
		}
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()

	skus, err := s.listSKUs(ctx, productID)
	if err != nil {
		return domain.Product{}, err
	}
	p.SKUs = skus
	return p, nil
}

func (s *Store) listSKUs(ctx context.Context, productID string) ([]domain.ProductSKU, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT sku_id, product_id, sku_name, price, stock_quantity, stock_status,
		specs_json, is_default, created_at, updated_at
		FROM product_skus WHERE product_id = ? ORDER BY is_default DESC, sku_id`, productID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.ProductSKU{}
	for rows.Next() {
		var (
			sku   domain.ProductSKU
			specs []byte
		)
		if err := rows.Scan(&sku.SkuID, &sku.ProductID, &sku.SkuName, &sku.Price, &sku.StockQuantity, &sku.StockStatus,
			&specs, &sku.IsDefault, &sku.CreatedAt, &sku.UpdatedAt); err != nil {
			return nil, err
		}
		if err := fromJSON("specs_json", specs, &sku.Specs); err != nil {
			return nil, err
		}
		sku.CreatedAt, sku.UpdatedAt = sku.CreatedAt.UTC(), sku.UpdatedAt.UTC()
		out = append(out, sku)
	}
	return out, rows.Err()
}
