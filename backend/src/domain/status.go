package domain

import "fmt"

// 状态机定义见 docs/02-data-and-domain.md。所有状态迁移只能由服务端通过这里校验后执行。

type transitions[S ~string] map[S][]S

func (t transitions[S]) allowed(from, to S) bool {
	for _, next := range t[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ErrInvalidTransition 表示不允许的状态迁移。
type ErrInvalidTransition struct {
	Entity   string
	From, To string
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("%s 不能从 %s 变为 %s", e.Entity, e.From, e.To)
}

func check[S ~string](t transitions[S], entity string, from, to S) error {
	if t.allowed(from, to) {
		return nil
	}
	return &ErrInvalidTransition{Entity: entity, From: string(from), To: string(to)}
}

// OrderStatus 订单：pending_payment -> paid -> shipped -> completed；仅待支付可取消。
type OrderStatus string

const (
	OrderPendingPayment OrderStatus = "pending_payment"
	OrderPaid           OrderStatus = "paid"
	OrderShipped        OrderStatus = "shipped"
	OrderCompleted      OrderStatus = "completed"
	OrderCancelled      OrderStatus = "cancelled"
)

var orderTransitions = transitions[OrderStatus]{
	OrderPendingPayment: {OrderPaid, OrderCancelled},
	OrderPaid:           {OrderShipped},
	OrderShipped:        {OrderCompleted},
}

func (s OrderStatus) CanTransitionTo(to OrderStatus) error {
	return check(orderTransitions, "订单", s, to)
}

// PaymentStatus 支付：pending -> paid | closed。
type PaymentStatus string

const (
	PaymentPending PaymentStatus = "pending"
	PaymentPaid    PaymentStatus = "paid"
	PaymentClosed  PaymentStatus = "closed"
)

var paymentTransitions = transitions[PaymentStatus]{
	PaymentPending: {PaymentPaid, PaymentClosed},
}

func (s PaymentStatus) CanTransitionTo(to PaymentStatus) error {
	return check(paymentTransitions, "支付单", s, to)
}

// EntityStatus 用于账户、商家：active <-> inactive；任意 -> risk；risk 可由管理员恢复为 active/inactive。
type EntityStatus string

const (
	StatusActive   EntityStatus = "active"
	StatusInactive EntityStatus = "inactive"
	StatusRisk     EntityStatus = "risk"
)

var entityTransitions = transitions[EntityStatus]{
	StatusActive:   {StatusInactive, StatusRisk},
	StatusInactive: {StatusActive, StatusRisk},
	StatusRisk:     {StatusActive, StatusInactive},
}

func (s EntityStatus) CanTransitionTo(to EntityStatus) error {
	return check(entityTransitions, "状态", s, to)
}

// ProductStatus 商品：在 EntityStatus 基础上可 -> deleted，deleted 为终态。
type ProductStatus string

const (
	ProductActive   ProductStatus = "active"
	ProductInactive ProductStatus = "inactive"
	ProductRisk     ProductStatus = "risk"
	ProductDeleted  ProductStatus = "deleted"
)

var productTransitions = transitions[ProductStatus]{
	ProductActive:   {ProductInactive, ProductRisk, ProductDeleted},
	ProductInactive: {ProductActive, ProductRisk, ProductDeleted},
	ProductRisk:     {ProductActive, ProductInactive, ProductDeleted},
}

func (s ProductStatus) CanTransitionTo(to ProductStatus) error {
	return check(productTransitions, "商品", s, to)
}

// StockStatus 由库存数量推导，不单独迁移。
type StockStatus string

const (
	StockInStock    StockStatus = "in_stock"
	StockLow        StockStatus = "low_stock"
	StockOutOfStock StockStatus = "out_of_stock"
)

// LowStockThreshold 以下（含）视为库存紧张。
const LowStockThreshold = 10

func StockStatusOf(quantity int) StockStatus {
	switch {
	case quantity <= 0:
		return StockOutOfStock
	case quantity <= LowStockThreshold:
		return StockLow
	default:
		return StockInStock
	}
}

// RunStatus Agent run：queued -> running -> completed | failed | cancelled；排队中也可直接取消或失败。
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

var runTransitions = transitions[RunStatus]{
	RunQueued:  {RunRunning, RunCancelled, RunFailed},
	RunRunning: {RunCompleted, RunFailed, RunCancelled},
}

func (s RunStatus) CanTransitionTo(to RunStatus) error {
	return check(runTransitions, "运行", s, to)
}

// Terminal 是否已结束（completed / failed / cancelled）。
func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled
}

// DocumentStatus 知识文档：uploaded -> parsing -> indexing -> indexed | failed；失败或已索引的文档可重新解析。
type DocumentStatus string

const (
	DocUploaded DocumentStatus = "uploaded"
	DocParsing  DocumentStatus = "parsing"
	DocIndexing DocumentStatus = "indexing"
	DocIndexed  DocumentStatus = "indexed"
	DocFailed   DocumentStatus = "failed"
)

var documentTransitions = transitions[DocumentStatus]{
	DocUploaded: {DocParsing, DocFailed},
	DocParsing:  {DocIndexing, DocFailed},
	DocIndexing: {DocIndexed, DocFailed},
	DocIndexed:  {DocParsing},
	DocFailed:   {DocParsing},
}

// Valid 判断是否为已知的文档状态。
func (s DocumentStatus) Valid() bool {
	_, ok := documentTransitions[s]
	return ok
}

func (s DocumentStatus) CanTransitionTo(to DocumentStatus) error {
	return check(documentTransitions, "文档", s, to)
}
