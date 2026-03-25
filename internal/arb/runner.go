package arb

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-okx-trading/internal/monitor"
	"go-okx-trading/internal/okx"
)

// PositionInfo 持仓摘要，用于 REST 或 WS 统一
type PositionInfo struct {
	InstId  string
	PosSide string
	Pos     string
	AvgPx   string
}

// Trader 交易接口：可由 REST 或 WebSocket 实现
type Trader interface {
	PlaceOrder(instId, tdMode, side, ordType, sz, px, tgtCcy string) error
	GetPositions(instId string) []PositionInfo
}

// Runner 逐仓套利 Runner：一次只持有一个订单，无仓则按策略开仓，有仓则监控并按现有逻辑或价差回归平仓
type Runner struct {
	mu             sync.Mutex
	cfg            okx.Config
	instId         string
	swapInstId     string
	orderAmt       string
	stopLossPct    float64
	trailType      string
	trailValue     float64
	params         Params
	store          *monitor.Store
	trader         Trader              // 若设则用 WS/REST 统一接口
	getFundingRate func(instId string) string
	onSubscribe    func(instId string)
	onUnsubscribe  func(instId string)
	stop           chan struct{}
	running        bool
	entryId        string
}

// SetTrader 设置交易接口（如 WebSocket 交易），未设则用 REST
func (r *Runner) SetTrader(t Trader) {
	r.trader = t
}

// SetGetFundingRate 设置资金费率获取（如来自 WS 缓存），未设则用 REST
func (r *Runner) SetGetFundingRate(fn func(instId string) string) {
	r.getFundingRate = fn
}

// NewRunner 创建套利 Runner
func NewRunner(cfg okx.Config, instId, orderAmt string, stopLossPct float64, trailType string, trailValue float64, store *monitor.Store) *Runner {
	swapInstId := instId
	if !strings.HasSuffix(instId, "-SWAP") {
		swapInstId = instId + "-SWAP"
	}
	return &Runner{
		cfg:         cfg,
		instId:      instId,
		swapInstId:  swapInstId,
		orderAmt:    orderAmt,
		stopLossPct: stopLossPct,
		trailType:   trailType,
		trailValue:  trailValue,
		params:      DefaultParams(),
		store:       store,
		stop:        make(chan struct{}),
	}
}

// SetParams 设置策略参数
func (r *Runner) SetParams(p Params) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.params = p
}

// SetSubscribe 设置订阅/取消订阅回调（用于 WebSocket 订阅行情）
func (r *Runner) SetSubscribe(onSub, onUnsub func(instId string)) {
	r.onSubscribe = onSub
	r.onUnsubscribe = onUnsub
}

// Start 启动 runner（循环：无仓则按策略下单，有仓则监控平仓）
func (r *Runner) Start() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go r.loop()
}

// Stop 停止 runner
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	close(r.stop)
	r.mu.Unlock()
}

func (r *Runner) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.tick()
		}
	}
}

func (r *Runner) tick() {
	var positions []PositionInfo
	if r.trader != nil {
		positions = r.trader.GetPositions(r.swapInstId)
	} else {
		client := okx.NewClient(r.cfg)
		posResp, err := client.GetPositions("SWAP", r.swapInstId)
		if err != nil {
			log.Printf("[arb] GetPositions %s err: %v", r.swapInstId, err)
			return
		}
		for _, p := range posResp.Data {
			positions = append(positions, PositionInfo{InstId: p.InstId, PosSide: p.PosSide, Pos: p.Pos, AvgPx: p.AvgPx})
		}
	}

	hasPos := len(positions) > 0
	if hasPos {
		r.tickWithPosition(positions)
		return
	}

	r.removeEntryAndUnsub()
	r.tickNoPosition()
}

func (r *Runner) removeEntryAndUnsub() {
	if r.entryId != "" {
		r.store.Remove(r.entryId)
		r.entryId = ""
	}
	if r.onUnsubscribe != nil {
		r.onUnsubscribe(r.swapInstId)
	}
}

func (r *Runner) tickWithPosition(positions []PositionInfo) {
	pos := positions[0]
	posSide := strings.ToLower(pos.PosSide)
	if posSide != "long" && posSide != "short" {
		posSide = "long"
		if pos.Pos != "" {
			if n, _ := strconv.ParseFloat(pos.Pos, 64); n < 0 {
				posSide = "short"
			}
		}
	}
	avgPx := pos.AvgPx
	sz := pos.Pos
	if sz == "" || avgPx == "" {
		return
	}

	r.mu.Lock()
	needAdd := r.entryId == ""
	r.mu.Unlock()
	if needAdd {
		side := "buy"
		if posSide == "short" {
			side = "sell"
		}
		e := &monitor.Entry{
			InstId: r.swapInstId, TdMode: "isolated",
			EntryPrice: avgPx, Sz: sz, Side: side,
			StopLossPct: r.stopLossPct, TrailType: r.trailType, TrailValue: r.trailValue,
			Config: r.cfg,
		}
		r.store.Add(e)
		r.mu.Lock()
		r.entryId = e.Id
		r.mu.Unlock()
		if r.onSubscribe != nil {
			r.onSubscribe(r.swapInstId)
		}
	}

	client := okx.NewClient(r.cfg)
	spotTicker, err := client.GetTicker(r.instId)
	if err != nil || len(spotTicker.Data) == 0 {
		return
	}
	swapTicker, err := client.GetTicker(r.swapInstId)
	if err != nil || len(swapTicker.Data) == 0 {
		return
	}
	var fundingRate float64
	if r.getFundingRate != nil {
		fundingRate, _ = strconv.ParseFloat(r.getFundingRate(r.swapInstId), 64)
	} else {
		frResp, err := client.GetFundingRate(r.swapInstId)
		if err == nil && len(frResp.Data) > 0 {
			fundingRate, _ = strconv.ParseFloat(frResp.Data[0].FundingRate, 64)
		}
	}
	spotPx, _ := strconv.ParseFloat(spotTicker.Data[0].Last, 64)
	swapPx, _ := strconv.ParseFloat(swapTicker.Data[0].Last, 64)

	strategy := NewStrategy(r.params)
	signal := strategy.Decide(spotPx, swapPx, fundingRate, posSide)
	if signal == SignalClose {
		closeSide := "sell"
		if posSide == "short" {
			closeSide = "buy"
		}
		var err error
		if r.trader != nil {
			err = r.trader.PlaceOrder(r.swapInstId, "isolated", closeSide, "market", sz, "", "")
		} else {
			_, err = client.PlaceOrder(r.swapInstId, "isolated", closeSide, "market", sz, "")
		}
		if err != nil {
			log.Printf("[arb] 价差回归平仓下单失败: %v", err)
			return
		}
		log.Printf("[arb] %s 价差回归中轨，已市价平仓 %s %s", r.swapInstId, closeSide, sz)
		r.removeEntryAndUnsub()
	}
}

func (r *Runner) tickNoPosition() {
	client := okx.NewClient(r.cfg)
	spotTicker, err := client.GetTicker(r.instId)
	if err != nil || len(spotTicker.Data) == 0 {
		return
	}
	swapTicker, err := client.GetTicker(r.swapInstId)
	if err != nil || len(swapTicker.Data) == 0 {
		return
	}
	var fundingRate float64
	if r.getFundingRate != nil {
		fundingRate, _ = strconv.ParseFloat(r.getFundingRate(r.swapInstId), 64)
	} else {
		frResp, err := client.GetFundingRate(r.swapInstId)
		if err == nil && len(frResp.Data) > 0 {
			fundingRate, _ = strconv.ParseFloat(frResp.Data[0].FundingRate, 64)
		}
	}
	spotPx, _ := strconv.ParseFloat(spotTicker.Data[0].Last, 64)
	swapPx, _ := strconv.ParseFloat(swapTicker.Data[0].Last, 64)

	strategy := NewStrategy(r.params)
	signal := strategy.Decide(spotPx, swapPx, fundingRate, "")
	if signal != SignalLong && signal != SignalShort {
		return
	}
	side := "buy"
	if signal == SignalShort {
		side = "sell"
	}
	var err2 error
	if r.trader != nil {
		err2 = r.trader.PlaceOrder(r.swapInstId, "isolated", side, "market", r.orderAmt, "", "quote_ccy")
	} else {
		_, err2 = client.PlaceOrderWithTarget(r.swapInstId, "isolated", side, "market", r.orderAmt, "", "quote_ccy")
	}
	if err2 != nil {
		log.Printf("[arb] 开仓下单失败: %v", err2)
		return
	}
	log.Printf("[arb] %s 按策略开仓 %s 金额 %s USDT", r.swapInstId, side, r.orderAmt)
}
