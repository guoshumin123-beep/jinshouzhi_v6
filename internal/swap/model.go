// Package swap 是合约领域模型与持久化层，供 gateway 及各通道执行器共用。
package swap

import (
	"errors"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

type Route string

const (
	RouteStandard  Route = "STANDARD"  // DMA：透传至用户专属火币子账户（默认）
	RouteSynthetic Route = "SYNTHETIC" // 本地虚拟流动性
)

func (r Route) Valid() bool { return r == RouteStandard || r == RouteSynthetic }

// MarginMode 保证金模式，由用户自选，与路由画像相互独立。
type MarginMode string

const (
	MarginIsolated MarginMode = "ISOLATED"
	MarginCross    MarginMode = "CROSS"
)

func (m MarginMode) Valid() bool { return m == MarginIsolated || m == MarginCross }

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

func (s Side) Valid() bool { return s == SideBuy || s == SideSell }

func (s Side) Opposite() Side {
	if s == SideBuy {
		return SideSell
	}
	return SideBuy
}

type Direction string

const (
	DirectionLong  Direction = "LONG"
	DirectionShort Direction = "SHORT"
)

// DirectionOf 单向持仓下 BUY 开多、SELL 开空。
func DirectionOf(s Side) Direction {
	if s == SideBuy {
		return DirectionLong
	}
	return DirectionShort
}

// ClosingSide 平掉该方向持仓所需的订单方向。
func (d Direction) ClosingSide() Side {
	if d == DirectionLong {
		return SideSell
	}
	return SideBuy
}

// Sign 多头 +1，空头 -1。
func (d Direction) Sign() decimal.Decimal {
	if d == DirectionLong {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromInt(-1)
}

type OrderType string

const (
	OrderMarket OrderType = "MARKET"
	OrderLimit  OrderType = "LIMIT"
)

type OrderSource string

const (
	SourceUser        OrderSource = "USER"
	SourceLiquidation OrderSource = "LIQUIDATION"
)

type OrderStatus string

const (
	OrderNew             OrderStatus = "NEW"              // 仅存在于受理事务内
	OrderPendingSubmit   OrderStatus = "PENDING_SUBMIT"   // Standard：已落库，待提交火币
	OrderSubmitted       OrderStatus = "SUBMITTED"        // Standard：火币已受理
	OrderOpen            OrderStatus = "OPEN"             // Synthetic：挂单中，无成交
	OrderPartiallyFilled OrderStatus = "PARTIALLY_FILLED" // 部分成交，剩余仍挂单
	OrderFilled          OrderStatus = "FILLED"
	OrderCanceled        OrderStatus = "CANCELED" // 可能 filled_volume > 0（部分成交后撤销）
	OrderRejected        OrderStatus = "REJECTED"
)

// ActiveOrderStatuses 仍在途、会影响槽位路由固化的订单状态。
var ActiveOrderStatuses = []OrderStatus{OrderPendingSubmit, OrderSubmitted, OrderOpen, OrderPartiallyFilled}

func (s OrderStatus) Active() bool {
	for _, a := range ActiveOrderStatuses {
		if s == a {
			return true
		}
	}
	return false
}

type PositionStatus string

const (
	PositionOpen   PositionStatus = "OPEN"
	PositionClosed PositionStatus = "CLOSED"
)

type CloseReason string

const (
	CloseNormal      CloseReason = "CLOSE"
	CloseFlip        CloseReason = "FLIP"
	CloseLiquidation CloseReason = "LIQUIDATION"
)

type TradeKind string

const (
	TradeOpen  TradeKind = "OPEN"
	TradeClose TradeKind = "CLOSE"
)

type Role string

const (
	RoleMaker       Role = "MAKER"
	RoleTaker       Role = "TAKER"
	RoleLiquidation Role = "LIQUIDATION"
)

type Profile struct {
	UserID     int64
	Route      Route
	MarginMode MarginMode
}

type Position struct {
	ID               uint64
	UserID           int64
	ContractCode     string
	MarginMode       MarginMode
	Route            Route
	Direction        Direction
	Volume           decimal.Decimal
	AvgPrice         decimal.Decimal
	Margin           decimal.Decimal
	Leverage         int
	LiquidationPrice decimal.Decimal
	RealizedPnL      decimal.Decimal
	FeePaid          decimal.Decimal
	FundingPaid      decimal.Decimal
	Status           PositionStatus
	CloseReason      CloseReason
	OpenedAt         time.Time
	ClosedAt         *time.Time
}

type Order struct {
	ID            uint64
	UserID        int64
	ClientOrderID string
	ContractCode  string
	MarginMode    MarginMode
	Route         Route
	Side          Side
	OrderType     OrderType
	ReduceOnly    bool
	Source        OrderSource
	Volume        decimal.Decimal
	Price         decimal.Decimal
	Leverage      int
	FilledVolume  decimal.Decimal
	AvgFillPrice  decimal.Decimal
	FeePaid       decimal.Decimal
	RealizedPnL   decimal.Decimal
	FrozenAmount  decimal.Decimal
	FillSeq       int
	Status        OrderStatus
	ErrMsg        string
	CreatedAt     time.Time
}

// Remaining 未成交张数。
func (o *Order) Remaining() decimal.Decimal { return o.Volume.Sub(o.FilledVolume) }

// BizID 生成该订单在 dealer 中的幂等键。
func (o *Order) BizID(parts ...string) string {
	s := "swap:o" + strconv.FormatUint(o.ID, 10)
	for _, p := range parts {
		s += ":" + p
	}
	return s
}

type Trade struct {
	ID            uint64
	OrderID       uint64
	UserID        int64
	PositionID    uint64
	ContractCode  string
	MarginMode    MarginMode
	Route         Route
	Side          Side
	Kind          TradeKind
	Role          Role
	Price         decimal.Decimal
	Volume        decimal.Decimal
	Fee           decimal.Decimal
	RealizedPnL   decimal.Decimal
	LossShortfall decimal.Decimal
}

var ErrNotFound = errors.New("swap: not found")
