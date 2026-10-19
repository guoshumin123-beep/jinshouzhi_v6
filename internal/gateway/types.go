// Package gateway 是合约订单的动态路由网关（Standard / Synthetic 双轨）。
//
// 路由固化规则（槽位 = user + contract + margin_mode）：
//  1. 槽位有 OPEN 持仓 → 使用 position.route；
//  2. 否则槽位有在途订单 → 使用该订单的 route（保证同一槽位不会出现两条通道的持仓）；
//  3. 否则读取 user_trade_profiles.route（默认 STANDARD）。
//
// 加仓、减仓、平仓、平反手（剩余反向开仓部分）全部沿用持仓路由；持仓 CLOSED 且无在途订单后，
// 下一次开仓才重新读取画像。保证金模式由用户自选，与路由画像相互独立。
package gateway

import (
	"context"
	"database/sql"
	"errors"

	"github.com/shopspring/decimal"

	"backend_v2/internal/swap"
)

// PlaceOrderRequest 来自 User 端 Handler（UserID 取自 JWT，绝不取自请求体）。
type PlaceOrderRequest struct {
	UserID        int64
	ClientOrderID string
	ContractCode  string
	MarginMode    swap.MarginMode // 为空时取用户画像中的默认保证金模式
	Side          swap.Side
	OrderType     swap.OrderType
	Volume        decimal.Decimal // 张数，正整数
	Price         decimal.Decimal // 限价单必填
	Leverage      int
	ReduceOnly    bool
}

// Executor 某一路由的执行器。所有方法都在已持有 swap.LockUser 的事务内调用。
type Executor interface {
	Route() swap.Route

	// Accept 受理新订单：o 已落库（有 ID），pos 为槽位当前 OPEN 持仓（可能为 nil）。
	// 执行器负责资金冻结/成交/持仓变更，并设置 o 的状态；返回 error 则整个事务回滚。
	// 不得在此调用任何外部网络接口。
	Accept(ctx context.Context, tx *sql.Tx, o *swap.Order, pos *swap.Position) error

	// Cancel 受理撤单（o 处于在途状态）。
	Cancel(ctx context.Context, tx *sql.Tx, o *swap.Order) error

	// Submit 事务提交后执行外部动作（Standard：用子账户 Key 调用火币）。Synthetic 为空操作。
	Submit(ctx context.Context, o *swap.Order) error
}

var (
	ErrInvalidRequest     = errors.New("gateway: invalid request")
	ErrNoExecutor         = errors.New("gateway: no executor for route")
	ErrLeverageMismatch   = errors.New("gateway: leverage differs from open position or active orders in this slot")
	ErrOrderNotCancelable = errors.New("gateway: order is not cancelable")
)
