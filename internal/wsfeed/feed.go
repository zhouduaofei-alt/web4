package wsfeed

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	okxPublicWS        = "wss://ws.okx.com:8443/ws/v5/public"
	okxPublicWSSimulated = "wss://wspap.okx.com:8443/ws/v5/public?brokerId=9999"
	pingPeriod          = 25 * time.Second
	writeWait           = 10 * time.Second
)

// TickerMessage 与 OKX 推送的 tickers 结构一致
type TickerMessage struct {
	Arg  struct {
		Channel string `json:"channel"`
		InstId  string `json:"instId"`
	} `json:"arg"`
	Data []struct {
		InstId string `json:"instId"`
		Last   string `json:"last"`
		BidPx  string `json:"bidPx"`
		AskPx  string `json:"askPx"`
		BidSz  string `json:"bidSz"`
		AskSz  string `json:"askSz"`
		Ts     string `json:"ts"`
	} `json:"data"`
}

// FundingRateMessage 资金费率推送
type FundingRateMessage struct {
	Arg  struct { Channel string `json:"channel"`; InstId string `json:"instId"` } `json:"arg"`
	Data []struct {
		InstId          string `json:"instId"`
		FundingRate     string `json:"fundingRate"`
		NextFundingTime string `json:"nextFundingTime,omitempty"`
		NextFundingRate string `json:"nextFundingRate,omitempty"`
	} `json:"data"`
}

// Feed 维持 OKX 公开 WebSocket，订阅 tickers（买一卖一）与 funding-rate；支持模拟盘
type Feed struct {
	mu            sync.Mutex
	conn          *websocket.Conn
	subs          map[string]bool
	subsFunding   map[string]bool
	onTicker      func(instId, bidPx, askPx string)
	onFundingRate func(instId, fundingRate string)
	done          chan struct{}
	closed        bool
	simulated     bool
}

func New(onTicker func(instId, bidPx, askPx string)) *Feed {
	return &Feed{
		subs:        make(map[string]bool),
		subsFunding: make(map[string]bool),
		onTicker:    onTicker,
		done:        make(chan struct{}),
	}
}

// SetOnFundingRate 设置资金费率推送回调
func (f *Feed) SetOnFundingRate(cb func(instId, fundingRate string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onFundingRate = cb
}

// SubscribeFundingRate 订阅资金费率（永续 instId 如 BTC-USDT-SWAP）
func (f *Feed) SubscribeFundingRate(instId string) {
	f.mu.Lock()
	if f.subsFunding[instId] {
		f.mu.Unlock()
		return
	}
	f.subsFunding[instId] = true
	needConnect := f.conn == nil
	f.mu.Unlock()
	if needConnect {
		f.connectAndSubscribe()
		return
	}
	f.sendSubscribeFunding([]string{instId})
}

func (f *Feed) sendSubscribeFunding(instIds []string) {
	if len(instIds) == 0 {
		return
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	args := make([]map[string]string, 0, len(instIds))
	for _, id := range instIds {
		args = append(args, map[string]string{"channel": "funding-rate", "instId": id})
	}
	_ = conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args})
}

// SetSimulated 设置是否使用模拟盘 WebSocket 地址
func (f *Feed) SetSimulated(simulated bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.simulated = simulated
}

func (f *Feed) publicURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.simulated {
		return okxPublicWSSimulated
	}
	return okxPublicWS
}

// Subscribe 增加订阅（若未连接则先连接，若已连接则发送 subscribe）
func (f *Feed) Subscribe(instId string) {
	f.mu.Lock()
	if f.subs[instId] {
		f.mu.Unlock()
		return
	}
	f.subs[instId] = true
	needConnect := f.conn == nil
	f.mu.Unlock()

	if needConnect {
		f.connectAndSubscribe()
		return
	}
	f.sendSubscribe([]string{instId})
}

// Unsubscribe 取消订阅
func (f *Feed) Unsubscribe(instId string) {
	f.mu.Lock()
	delete(f.subs, instId)
	f.mu.Unlock()
	f.sendUnsubscribe([]string{instId})
}

// SetSubs 全量替换订阅列表（用于与监控列表同步）
func (f *Feed) SetSubs(instIds []string) {
	f.mu.Lock()
	f.subs = make(map[string]bool)
	for _, id := range instIds {
		if id != "" {
			f.subs[id] = true
		}
	}
	needConnect := f.conn == nil
	f.mu.Unlock()

	if len(instIds) == 0 {
		return
	}
	if needConnect {
		f.connectAndSubscribe()
	} else {
		// 简单做法：断开后用新列表重连
		f.mu.Lock()
		if f.conn != nil {
			_ = f.conn.Close()
			f.conn = nil
		}
		f.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		f.connectAndSubscribe()
	}
}

func (f *Feed) connectAndSubscribe() {
	dialer := websocket.DefaultDialer
	url := f.publicURL()
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		log.Printf("[wsfeed] dial err: %v", err)
		return
	}
	f.mu.Lock()
	f.conn = conn
	ids := make([]string, 0, len(f.subs))
	for id := range f.subs {
		ids = append(ids, id)
	}
	f.mu.Unlock()

	f.sendSubscribe(ids)
	fundingIds := make([]string, 0, len(f.subsFunding))
	for id := range f.subsFunding {
		fundingIds = append(fundingIds, id)
	}
	f.sendSubscribeFunding(fundingIds)
	go f.readLoop()
	go f.pingLoop()
}

func (f *Feed) sendSubscribe(instIds []string) {
	if len(instIds) == 0 {
		return
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	args := make([]map[string]string, 0, len(instIds))
	for _, id := range instIds {
		args = append(args, map[string]string{"channel": "tickers", "instId": id})
	}
	msg := map[string]interface{}{"op": "subscribe", "args": args}
	if err := conn.WriteJSON(msg); err != nil {
		log.Printf("[wsfeed] subscribe write err: %v", err)
		return
	}
	log.Printf("[wsfeed] subscribed %v", instIds)
}

func (f *Feed) sendUnsubscribe(instIds []string) {
	if len(instIds) == 0 {
		return
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	args := make([]map[string]string, 0, len(instIds))
	for _, id := range instIds {
		args = append(args, map[string]string{"channel": "tickers", "instId": id})
	}
	msg := map[string]interface{}{"op": "unsubscribe", "args": args}
	_ = conn.WriteJSON(msg)
}

func (f *Feed) readLoop() {
	defer func() {
		f.mu.Lock()
		if f.conn != nil {
			_ = f.conn.Close()
			f.conn = nil
		}
		f.mu.Unlock()
	}()

	for {
		_, data, err := f.conn.ReadMessage()
		if err != nil {
			if !f.closed {
				log.Printf("[wsfeed] read err: %v", err)
			}
			return
		}

		var channel struct {
			Arg  struct { Channel string `json:"channel"`; InstId string `json:"instId"` } `json:"arg"`
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &channel); err != nil || len(channel.Data) == 0 {
			continue
		}
		switch channel.Arg.Channel {
		case "tickers":
			var tick TickerMessage
			if _ = json.Unmarshal(data, &tick); len(tick.Data) > 0 && f.onTicker != nil {
				d := tick.Data[0]
				instId := d.InstId
				if instId == "" {
					instId = channel.Arg.InstId
				}
				bid, ask := d.BidPx, d.AskPx
				if bid == "" {
					bid = d.Last
				}
				if ask == "" {
					ask = d.Last
				}
				f.onTicker(instId, bid, ask)
			}
		case "funding-rate":
			var fr FundingRateMessage
			if _ = json.Unmarshal(data, &fr); len(fr.Data) > 0 && f.onFundingRate != nil {
				d := fr.Data[0]
				instId := d.InstId
				if instId == "" {
					instId = channel.Arg.InstId
				}
				f.onFundingRate(instId, d.FundingRate)
			}
		}
	}
}

func (f *Feed) pingLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-ticker.C:
			f.mu.Lock()
			conn := f.conn
			f.mu.Unlock()
			if conn == nil {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				return
			}
		}
	}
}

// Close 关闭连接
func (f *Feed) Close() {
	f.mu.Lock()
	f.closed = true
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
	close(f.done)
	f.mu.Unlock()
}
