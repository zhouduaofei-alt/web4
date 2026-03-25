package monitor

import (
	"log"
	"strconv"
	"sync"
	"time"

	"go-okx-trading/internal/okx"
)

// Entry 单条监控：持仓 + 百分比止损 + 追踪止盈（百分比或 U）
type Entry struct {
	Id           string    `json:"id"`
	InstId       string    `json:"instId"`
	TdMode       string    `json:"tdMode"`   // "cash" 现货 / "isolated" 逐仓 等
	EntryPrice   string    `json:"entryPrice"`
	Sz           string    `json:"sz"`
	Side         string    `json:"side"`   // "buy"=多头持仓(平仓用 sell)，"sell"=空头(平仓用 buy)
	StopLossPct  float64   `json:"stopLossPct"`  // 止损比例，如 5 表示 5%
	TrailType    string    `json:"trailType"`   // "pct" 或 "u"
	TrailValue   float64   `json:"trailValue"`  // 追踪回撤：百分比点数 或 USDT 价格差
	HighPrice    float64   `json:"highPrice"`   // 持仓期间最高价（多头）/ 最低价（空头）
	Config       okx.Config `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

// PlaceOrderFunc 下单函数，可由 REST 或 WebSocket 实现
type PlaceOrderFunc func(instId, tdMode, side, ordType, sz, px string) error

type Store struct {
	mu           sync.RWMutex
	m            map[string]*Entry
	stop         chan struct{}
	onPlaceOrder PlaceOrderFunc // 若设则平仓时用此替代 REST
}

// SetPlaceOrder 设置平仓下单回调（如 WebSocket 下单）
func (s *Store) SetPlaceOrder(fn PlaceOrderFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPlaceOrder = fn
}

func NewStore() *Store {
	return &Store{m: make(map[string]*Entry), stop: make(chan struct{})}
}

func (s *Store) Add(e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Id == "" {
		e.Id = e.InstId + "-" + strconv.FormatInt(time.Now().UnixNano()/1e6, 10)
	}
	e.CreatedAt = time.Now()
	s.m[e.Id] = e
}

func (s *Store) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

func (s *Store) Get(id string) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[id]
}

func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.m))
	for _, e := range s.m {
		cp := *e
		cp.Config = okx.Config{} // 不返回密钥
		out = append(out, cp)
	}
	return out
}

// InstIds 返回当前所有监控的交易对（去重），供 WebSocket 订阅
func (s *Store) InstIds() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	for _, e := range s.m {
		if e.InstId != "" {
			seen[e.InstId] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// OnTicker 由 WebSocket 买一卖一推送驱动：bidPx=买一，askPx=卖一；多头平仓用买一(bid)，空头平仓用卖一(ask)
func (s *Store) OnTicker(instId, bidPx, askPx string) {
	s.mu.RLock()
	list := make([]*Entry, 0)
	for _, e := range s.m {
		if e.InstId == instId {
			list = append(list, e)
		}
	}
	s.mu.RUnlock()
	for _, e := range list {
		var priceStr string
		if e.Side == "buy" {
			priceStr = bidPx // 多头卖出，看买一
		} else {
			priceStr = askPx // 空头买入，看卖一
		}
		s.checkOneWithPrice(e, priceStr)
	}
}

func (s *Store) Stop() { close(s.stop) }

// Run 轮询行情并触发止损/追踪止盈
func (s *Store) Run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.checkAll()
		}
	}
}

func (s *Store) checkAll() {
	s.mu.RLock()
	list := make([]*Entry, 0, len(s.m))
	for _, e := range s.m {
		list = append(list, e)
	}
	s.mu.RUnlock()

	for _, e := range list {
		s.checkOne(e)
	}
}

func (e *Entry) closeSide() string {
	if e.Side == "buy" {
		return "sell"
	}
	return "buy"
}

func (s *Store) checkOne(e *Entry) {
	client := okx.NewClient(e.Config)
	tickerResp, err := client.GetTicker(e.InstId)
	if err != nil {
		log.Printf("[monitor] ticker %s err: %v", e.InstId, err)
		return
	}
	if len(tickerResp.Data) == 0 {
		return
	}
	lastStr := tickerResp.Data[0].Last
	s.checkOneWithPrice(e, lastStr)
}

func (s *Store) checkOneWithPrice(e *Entry, priceStr string) {
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return
	}
	entry, _ := strconv.ParseFloat(e.EntryPrice, 64)

	if e.Side == "buy" {
		// 多头：止损为入场下方百分比；追踪为从最高价回撤
		slPrice := entry * (1 - e.StopLossPct/100)
		if price <= slPrice {
			client := okx.NewClient(e.Config)
			s.triggerClose(e, client, price, "止损")
			return
		}
		if e.HighPrice == 0 {
			e.HighPrice = entry
		}
		if price > e.HighPrice {
			e.HighPrice = price
		}
		if e.HighPrice <= entry {
			return // 未浮盈不触发追踪
		}
		var trigger bool
		if e.TrailType == "pct" {
			trailPrice := e.HighPrice * (1 - e.TrailValue/100)
			trigger = price <= trailPrice
		} else {
			trigger = price <= e.HighPrice-e.TrailValue
		}
		if trigger {
			client := okx.NewClient(e.Config)
			s.triggerClose(e, client, price, "追踪止盈")
		}
	} else {
		// 空头：止损为入场上方百分比；追踪为从最低价回升
		slPrice := entry * (1 + e.StopLossPct/100)
		if price >= slPrice {
			client := okx.NewClient(e.Config)
			s.triggerClose(e, client, price, "止损")
			return
		}
		if e.HighPrice == 0 {
			e.HighPrice = entry
		}
		if price < e.HighPrice {
			e.HighPrice = price // 空头用 HighPrice 存最低价
		}
		if e.HighPrice >= entry {
			return
		}
		var trigger bool
		if e.TrailType == "pct" {
			trailPrice := e.HighPrice * (1 + e.TrailValue/100)
			trigger = price >= trailPrice
		} else {
			trigger = price >= e.HighPrice+e.TrailValue
		}
		if trigger {
			client := okx.NewClient(e.Config)
			s.triggerClose(e, client, price, "追踪止盈")
		}
	}
}

func (s *Store) triggerClose(e *Entry, client *okx.Client, price float64, reason string) {
	tdMode := e.TdMode
	if tdMode == "" {
		tdMode = "cash"
	}
	closeSide := e.closeSide()
	s.mu.Lock()
	fn := s.onPlaceOrder
	s.mu.Unlock()
	var err error
	if fn != nil {
		err = fn(e.InstId, tdMode, closeSide, "market", e.Sz, "")
	} else {
		_, err = client.PlaceOrder(e.InstId, tdMode, closeSide, "market", e.Sz, "")
	}
	if err != nil {
		log.Printf("[monitor] %s 触发%s 下单失败: %v", e.InstId, reason, err)
		return
	}
	log.Printf("[monitor] %s 已触发%s 市价%s %s @ %s", e.InstId, reason, closeSide, e.Sz, e.InstId)
	s.Remove(e.Id)
}
