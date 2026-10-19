// Package app 组装全部模块，产出 User / Admin HTTP Handler 与后台任务。cmd/server 只负责进程生命周期。
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"

	"backend_v2/api/admin"
	"backend_v2/api/httpx"
	"backend_v2/api/user"
	"backend_v2/internal/config"
	"backend_v2/internal/dealer"
	"backend_v2/internal/gateway"
	"backend_v2/internal/gateway/standard"
	"backend_v2/internal/gateway/synthetic"
	"backend_v2/internal/htx"
	"backend_v2/internal/pkg/safego"
	"backend_v2/internal/subaccount"
)

// ErrTradingDisabled 未开启真实下单（HTX_TRADING_ENABLED=false）。
var ErrTradingDisabled = errors.New("app: HTX trading is disabled (HTX_TRADING_ENABLED=false)")

type App struct {
	UserHandler  http.Handler
	AdminHandler http.Handler

	db         *sql.DB
	cfg        *config.Config
	logger     *slog.Logger
	background []task
}

type task struct {
	name  string
	every time.Duration
	run   func(ctx context.Context)
}

// New 打开数据库并组装模块。不会主动访问火币（启动阶段零外部写操作）。
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	db, err := sql.Open("mysql", cfg.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("app: open mysql: %w", err)
	}
	db.SetMaxOpenConns(64)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("app: ping mysql: %w", err)
	}

	a := &App{db: db, cfg: cfg, logger: logger}
	if err := a.wire(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) wire() error {
	cfg, log := a.cfg, a.logger
	dl := dealer.NewMySQLDealer(a.db, log)

	// 火币客户端：合约与现货分开限频（火币各业务线独立计数）
	swapClient, err := htx.NewClient(cfg.HTXSwapBaseURL, htx.WithLogger(log), htx.WithChannelCode(cfg.ChannelCode))
	if err != nil {
		return err
	}
	spotClient, err := htx.NewClient(cfg.HTXSpotBaseURL, htx.WithLogger(log))
	if err != nil {
		return err
	}

	keys := subaccount.NewDBKeyProvider(a.db, cfg.Vault, time.Minute)

	var trader standard.Trader = disabledTrader{}
	if cfg.TradingEnabled {
		trader = swapClient
	}
	std := standard.NewExecutor(a.db, keys, trader, log)

	// Synthetic：真实行情在第三阶段接入（internal/marketdata）。在此之前行情源不可用，
	// Synthetic 订单返回 MARKET_UNAVAILABLE，绝不以假价格成交；撮合/强平/资金费后台任务暂不启动。
	syn := synthetic.NewEngine(a.db, dl, unavailableMarket{}, unavailableMarket{}, noSpecs{}, synthetic.Config{
		MakerFeeRate: decimal.RequireFromString("0.0002"), TakerFeeRate: decimal.RequireFromString("0.0005"),
		LiquidationFeeRate: decimal.RequireFromString("0.0005"), SpreadBps: decimal.Zero,
		MarketBand: decimal.RequireFromString("0.05"), MaxQuoteAge: 3 * time.Second, MaxMarkAge: 3 * time.Second,
	}, log)

	profiles, err := gateway.NewProfileStore(a.db, gateway.DefaultProfile, log)
	if err != nil {
		return err
	}
	router := gateway.NewRouter(a.db, profiles, log, std, syn)

	var prov *subaccount.Provisioner
	if cfg.ProvisioningEnabled {
		prov, err = subaccount.NewProvisioner(a.db, spotClient, swapClient, cfg.Vault, keys, subaccount.Config{
			Master: cfg.Master, NamePrefix: cfg.SubUserNamePrefix, KeyIPs: cfg.SubKeyIPs,
		}, log)
		if err != nil {
			return err
		}
		a.background = append(a.background, task{"subaccount_resume", cfg.ResumeInterval, func(ctx context.Context) {
			if n, err := prov.ResumePending(ctx, 50); err != nil {
				log.Error("resume provisioning failed", slog.String("error", err.Error()))
			} else if n > 0 {
				log.Info("resumed provisioning", slog.Int("ready", n))
			}
		}})
	}
	subs := subaccount.NewService(a.db, prov)

	if cfg.TransfersEnabled {
		xfer := subaccount.NewTransfers(a.db, dl, spotClient, keys, cfg.Master, true, log)
		a.background = append(a.background, task{"transfer_resume", cfg.ResumeInterval, func(ctx context.Context) {
			if _, err := xfer.Resume(ctx, 100); err != nil {
				log.Error("resume transfers failed", slog.String("error", err.Error()))
			}
		}})
	}

	health := httpx.Health(a.db)
	a.UserHandler = user.NewServer(user.Deps{
		Secret: cfg.UserJWTSecret, Router: router, Profiles: profiles, Wallet: syn, SubAccounts: subs,
		Health: health, Logger: log,
	})
	a.AdminHandler = admin.NewServer(admin.Deps{
		Secret: cfg.AdminJWTSecret, Profiles: profiles, SubAccounts: subs, Health: health, Logger: log,
	})
	return nil
}

// Run 运行后台任务直到 ctx 取消；每个任务独立 goroutine，panic 被捕获且不影响其他任务。
func (a *App) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range a.background {
		t := t
		wg.Add(1)
		safego.Go(a.logger, t.name, func() {
			defer wg.Done()
			ticker := time.NewTicker(t.every)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					func() {
						defer safego.Recover(a.logger, t.name)
						t.run(ctx)
					}()
				}
			}
		})
	}
	wg.Wait()
}

// Tasks 后台任务名称（用于启动日志与测试）。
func (a *App) Tasks() []string {
	out := make([]string, len(a.background))
	for i, t := range a.background {
		out[i] = t.name
	}
	return out
}

func (a *App) Close() error { return a.db.Close() }

// ---------------------------------------------------------------------------
// 占位实现
// ---------------------------------------------------------------------------

// disabledTrader 拒绝一切真实下单（HTX_TRADING_ENABLED=false）。
type disabledTrader struct{}

func (disabledTrader) SetLeverage(context.Context, *htx.Credentials, string, string, int) error {
	return ErrTradingDisabled
}
func (disabledTrader) SwapPlaceOrder(context.Context, *htx.Credentials, htx.SwapOrderRequest) (*htx.SwapOrderResponse, error) {
	return nil, ErrTradingDisabled
}

// unavailableMarket 第三阶段接入真实行情之前的行情源：始终不可用。
type unavailableMarket struct{}

func (unavailableMarket) Quote(string) (synthetic.Quote, bool) { return synthetic.Quote{}, false }
func (unavailableMarket) Mark(string) (decimal.Decimal, time.Time, bool) {
	return decimal.Zero, time.Time{}, false
}

type noSpecs struct{}

func (noSpecs) Spec(string) (synthetic.ContractSpec, bool) { return synthetic.ContractSpec{}, false }
