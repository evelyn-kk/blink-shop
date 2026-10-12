package mysqlstore

import (
	"context"
	"database/sql"
	"strings"
	"unicode"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
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
	p, err := scanProduct(s.q(ctx).QueryRowContext(ctx, `SELECT `+productColumns+` FROM products WHERE product_id = ?`, productID))
	if err != nil {
		return domain.Product{}, err
	}
	if p.SKUs, err = s.listSKUs(ctx, productID); err != nil {
		return domain.Product{}, err
	}
	return p, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanProduct 按 productColumns 的顺序读取一行（不含 SKU），后面可以跟额外列 extra。
func scanProduct(row rowScanner, extra ...any) (domain.Product, error) {
	var (
		p                                                        domain.Product
		images, tags, points, risks, attrs, suitable, unsuitable []byte
	)
	dest := []any{
		&p.ProductID, &p.MerchantID, &p.CategoryID, &p.Name, &p.Brand, &p.ImageURL, &images,
		&p.Price, &p.MarketPrice, &p.StockQuantity, &p.StockStatus, &tags, &points, &p.RecommendReason,
		&risks, &attrs, &suitable, &unsuitable, &p.Description, &p.Status,
		&p.SortOrder, &p.CreatedAt, &p.UpdatedAt,
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
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
	return p, nil
}

func (s *Store) listSKUs(ctx context.Context, productID string) ([]domain.ProductSKU, error) {
	m, err := s.skusByProduct(ctx, []string{productID})
	if err != nil {
		return nil, err
	}
	return m[productID], nil
}

// skusByProduct 一次查出多个商品的 SKU（列表页不再每个商品查一次）。每个商品都有一项，没有 SKU 时是空切片；
// 顺序与单个商品时相同：默认 SKU 在前，再按 sku_id。
func (s *Store) skusByProduct(ctx context.Context, productIDs []string) (map[string][]domain.ProductSKU, error) {
	out := make(map[string][]domain.ProductSKU, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(productIDs))
	for i, id := range productIDs {
		args[i] = id
		out[id] = []domain.ProductSKU{}
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT sku_id, product_id, sku_name, price, stock_quantity, stock_status,
		specs_json, is_default, created_at, updated_at
		FROM product_skus WHERE product_id IN (?`+strings.Repeat(`, ?`, len(productIDs)-1)+`) ORDER BY product_id, is_default DESC, sku_id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
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
		out[sku.ProductID] = append(out[sku.ProductID], sku)
	}
	return out, rows.Err()
}

// ---------- 公开目录 ----------

func (s *Store) ListActiveMerchants(ctx context.Context, page store.Page) ([]domain.Merchant, int, error) {
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM merchants WHERE status = 'active'`).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT merchant_id, name, logo_url, description, service_phone, status, created_at, updated_at
		FROM merchants WHERE status = 'active' ORDER BY merchant_id LIMIT ? OFFSET ?`, page.PageSize, page.Offset())
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Merchant{}
	for rows.Next() {
		var m domain.Merchant
		if err := rows.Scan(&m.MerchantID, &m.Name, &m.LogoURL, &m.Description, &m.ServicePhone, &m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, 0, err
		}
		m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func (s *Store) GetMerchant(ctx context.Context, merchantID string) (domain.Merchant, error) {
	var m domain.Merchant
	err := s.q(ctx).QueryRowContext(ctx, `SELECT merchant_id, name, logo_url, description, service_phone, status, created_at, updated_at
		FROM merchants WHERE merchant_id = ?`, merchantID).Scan(&m.MerchantID, &m.Name, &m.LogoURL, &m.Description, &m.ServicePhone, &m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return domain.Merchant{}, mapErr(err)
	}
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return m, nil
}

// visibleProductFrom 是公开商品查询的 FROM/WHERE 公共部分。
const visibleProductFrom = ` FROM products p
	JOIN merchants m ON m.merchant_id = p.merchant_id
	LEFT JOIN categories c ON c.category_id = p.category_id
	WHERE p.status = 'active' AND m.status = 'active'`

// productHits 返回“关键词命中”的商品 ID 子查询，结果与逐词 LIKE（名称、品牌、分类名、标签、卖点，任一词命中即可）一致或更多：
//   - 每个检索词拆成汉字段和字母数字段，两字及以上的段作为短语交给全文索引（迁移 0009，ngram 二字切分，短语要求连续出现，
//     所以段是字段的子串时一定命中）；
//   - 一个两字段都没有的词（如“灯a”）全文索引查不了，按原来的 LIKE 兜底；
//   - 分类名不在全文索引里：按分类名 LIKE 找分类，再取分类下的商品。
//
// 不可见商品在外层按状态过滤。
func productHits(terms []string) (string, []any) {
	var phrases, cats []string
	var args, catArgs, shortArgs []any
	var short []string
	for _, term := range terms {
		like := "%" + likeEscape.Replace(term) + "%"
		cats = append(cats, `c2.name LIKE ?`)
		catArgs = append(catArgs, like)
		runs := searchRuns(term)
		if len(runs) == 0 {
			short = append(short, `p3.name LIKE ?`, `p3.brand LIKE ?`,
				`JSON_SEARCH(LOWER(p3.tags_json), 'one', ?) IS NOT NULL`, `JSON_SEARCH(LOWER(p3.selling_points_json), 'one', ?) IS NOT NULL`)
			shortArgs = append(shortArgs, like, like, like, like)
		}
		for _, r := range runs {
			phrases = append(phrases, `"`+r+`"`)
		}
	}
	var parts []string
	if len(phrases) > 0 {
		parts = append(parts, `SELECT product_id FROM products WHERE MATCH(search_text) AGAINST (? IN BOOLEAN MODE)`)
		args = append(args, strings.Join(phrases, " "))
	}
	parts = append(parts, `SELECT p2.product_id FROM categories c2 JOIN products p2 ON p2.category_id = c2.category_id WHERE `+strings.Join(cats, ` OR `))
	args = append(args, catArgs...)
	if len(short) > 0 {
		parts = append(parts, `SELECT p3.product_id FROM products p3 WHERE `+strings.Join(short, ` OR `))
		args = append(args, shortArgs...)
	}
	return strings.Join(parts, ` UNION `), args
}

// searchRuns 把检索词拆成汉字段和字母数字段（与 store.SearchTerms 的切分一致），只保留两字及以上的段（全文索引的 ngram 是两字）。
func searchRuns(term string) []string {
	var out []string
	var run []rune
	han := false
	flush := func() {
		if len(run) >= 2 {
			out = append(out, string(run))
		}
		run = nil
	}
	for _, r := range term {
		isHan := unicode.Is(unicode.Han, r)
		if !isHan && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(run) > 0 && isHan != han {
			flush()
		}
		han = isHan
		run = append(run, r)
	}
	flush()
	return out
}

// visibleSearch 拼出公开商品列表 / 搜索的 FROM 和 WHERE（SELECT 之后的提示、FROM、WHERE、参数）；ok 为 false 表示关键词拆不出检索词，直接无结果。
func visibleSearch(q store.ProductSearch) (hint, from, where string, args []any, ok bool) {
	from = visibleProductFrom
	if terms := store.SearchTerms(q.Keyword); len(terms) > 0 {
		// 先求命中的商品 ID，再和可见性条件连接：全文、分类名、短词兜底三路各自走索引，用 UNION 合并（写成 OR 会全表扫描）。
		// STRAIGHT_JOIN 固定从命中集合开始连接：分类名的 LIKE 一多，优化器会把命中集合估成好几万行，改成扫全部在售商品再逐行
		// 查命中集合（10 万商品时“量子纠缠”这种查不到的词也要 0.3s）；从命中集合开始，命中少时几乎不花时间，命中多时也不更慢。
		hint = "STRAIGHT_JOIN "
		hits, hitArgs := productHits(terms)
		from = ` FROM (` + hits + `) hit
	JOIN products p ON p.product_id = hit.product_id` + strings.TrimPrefix(visibleProductFrom, ` FROM products p`)
		args = append(args, hitArgs...)
	} else if strings.TrimSpace(q.Keyword) != "" {
		return "", "", "", nil, false
	}
	if q.CategoryID != "" {
		where += ` AND (p.category_id = ? OR c.parent_id = ?)`
		args = append(args, q.CategoryID, q.CategoryID)
	}
	return hint, from, where, args, true
}

// likeEscape 转义 LIKE / JSON_SEARCH 通配符，关键词里的 % 和 _ 按字面匹配。
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *Store) SearchVisibleProducts(ctx context.Context, q store.ProductSearch) ([]store.CatalogProduct, int, error) {
	hint, from, where, args, ok := visibleSearch(q)
	if !ok {
		// 关键词有内容但拆不出检索词（例如单个字符），与上游一致按无结果处理。
		return []store.CatalogProduct{}, 0, nil
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT `+hint+`COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+hint+prefixed("p.", productColumns)+`, m.name`+from+where+
		` ORDER BY p.sort_order, p.product_id LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.CatalogProduct{}
	for rows.Next() {
		var cp store.CatalogProduct
		if cp.Product, err = scanProduct(rows, &cp.MerchantName); err != nil {
			return nil, 0, err
		}
		out = append(out, cp)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ProductID
	}
	skus, err := s.skusByProduct(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].SKUs = skus[out[i].ProductID]
	}
	return out, total, nil
}

func (s *Store) GetVisibleProduct(ctx context.Context, productID string) (store.CatalogProduct, error) {
	var cp store.CatalogProduct
	p, err := scanProduct(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+prefixed("p.", productColumns)+`, m.name`+visibleProductFrom+` AND p.product_id = ?`, productID), &cp.MerchantName)
	if err != nil {
		return store.CatalogProduct{}, err
	}
	cp.Product = p
	if cp.SKUs, err = s.listSKUs(ctx, productID); err != nil {
		return store.CatalogProduct{}, err
	}
	return cp, nil
}

func (s *Store) ListVisibleReviews(ctx context.Context, productID string, page store.Page) ([]store.PublicReview, int, error) {
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM product_reviews WHERE product_id = ? AND status = 'visible'`,
		productID).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT r.review_id, r.order_id, r.order_item_id, r.product_id, r.sku_id, r.account_id,
		r.rating, r.content, r.tags_json, r.status, COALESCE(r.merchant_reply, ''), r.merchant_replied_at, r.created_at, r.updated_at,
		COALESCE(IF(a.deleted_at IS NULL, a.display_name, ''), '')
		FROM product_reviews r LEFT JOIN accounts a ON a.account_id = r.account_id
		WHERE r.product_id = ? AND r.status = 'visible'
		ORDER BY r.created_at DESC, r.review_id DESC LIMIT ? OFFSET ?`, productID, page.PageSize, page.Offset())
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.PublicReview{}
	for rows.Next() {
		var (
			r       store.PublicReview
			tags    []byte
			replied sql.NullTime
		)
		if err := rows.Scan(&r.ReviewID, &r.OrderID, &r.OrderItemID, &r.ProductID, &r.SkuID, &r.AccountID,
			&r.Rating, &r.Content, &tags, &r.Status, &r.MerchantReply, &replied, &r.CreatedAt, &r.UpdatedAt, &r.ReviewerName); err != nil {
			return nil, 0, err
		}
		if err := fromJSON("tags_json", tags, &r.Tags); err != nil {
			return nil, 0, err
		}
		r.MerchantRepliedAt = timePtr(replied)
		r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *Store) ListActivePromotions(ctx context.Context, q store.PromotionQuery) ([]domain.PromotionRule, int, error) {
	at := q.At.UTC()
	where := ` FROM promotion_rules pr LEFT JOIN merchants m ON m.merchant_id = pr.merchant_id
		WHERE pr.status = 'active' AND pr.start_at <= ? AND pr.end_at > ? AND (pr.merchant_id = '' OR m.status = 'active')`
	args := []any{at, at}
	if q.ProductID != "" {
		scopes := []string{`pr.scope = 'platform'`, `(pr.scope = 'merchant' AND pr.merchant_id = ?)`, `(pr.scope = 'product' AND pr.product_id = ?)`}
		args = append(args, q.MerchantID, q.ProductID)
		if len(q.CategoryIDs) > 0 {
			scopes = append(scopes, `(pr.scope = 'category' AND pr.category_id IN (?`+strings.Repeat(`, ?`, len(q.CategoryIDs)-1)+`))`)
			for _, id := range q.CategoryIDs {
				args = append(args, id)
			}
		}
		where += ` AND (` + strings.Join(scopes, ` OR `) + `)`
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+prefixed("pr.", promotionColumns)+where+
		` ORDER BY pr.created_at DESC, pr.promotion_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.PromotionRule{}
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}
