package wstrade

import (
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/iaping/go-okx/common"
	"go-okx-trading/internal/orderlog"
)

var errNotConnected = errors.New("wstrade: not connected")

const (
	urlPrivate          = "wss://ws.okx.com:8443/ws/v5/private"
	urlPrivateSimulated = "wss://wspap.okx.com:8443/ws/v5/private?brokerId=9999"
	pingPeriod          = 25 * time.Second
	writeWait           = 10 * time.Second
)

// Client 私有 WebSocket：登录、订阅订单/持仓、WS 下单、订单推送写入 orderlog
type Client struct {
	mu        sync.Mutex
	auth      common.Auth
	conn      *websocket.Conn
	orderLog  *orderlog.Store
	onOrder   func(record orderlog.Record)
	positions []Position
	closed    bool
	done      chan struct{}
}

// Position 持仓快照（来自 positions 频道）
type Position struct {
	InstId  string `json:"instId"`
	PosSide string `json:"posSide"`
	Pos     string `json:"pos"`
	AvgPx   string `json:"avgPx"`
	MgnMode string `json:"mgnMode"`
}

// NewClient 创建私有 WS 客户端
func NewClient(auth common.Auth, orderLog *orderlog.Store) *Client {
	return &Client{
		auth:     auth,
		orderLog: orderLog,
		done:     make(chan struct{}),
	}
}

// SetOnOrder 设置订单推送回调（在写入 orderlog 之后调用）
func (c *Client) SetOnOrder(fn func(record orderlog.Record)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOrder = fn
}

// Connect 连接、登录、订阅 orders 与 positions
func (c *Client) Connect() error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	url := urlPrivate
	if c.auth.Simulated {
		url = urlPrivateSimulated
	}
	c.mu.Unlock()

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// 登录（须先调用 Build() 才会填充 Timestamp）
	sig := c.auth.Signature("GET", "/users/self/verify", "", true)
	signStr := sig.Build()
	loginReq := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     c.auth.ApiKey,
			"passphrase": c.auth.Passphrase,
			"timestamp":  sig.Timestamp,
			"sign":       signStr,
		}},
	}
	if err := conn.WriteJSON(loginReq); err != nil {
		_ = conn.Close()
		return err
	}
	var loginResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := conn.ReadJSON(&loginResp); err != nil {
		_ = conn.Close()
		return err
	}
	if loginResp.Code != "0" {
		_ = conn.Close()
		msg := loginResp.Msg
		if strings.Contains(msg, "environment") || strings.Contains(msg, "APIKey") {
			if c.auth.Simulated {
				msg = "模拟盘需使用在 OKX「模拟交易」中创建的 API Key。请登录 OKX → 模拟交易 → 个人中心 → 创建模拟盘 API Key，再在此处填写该 Key 并勾选模拟盘。"
			} else {
				msg = "当前 Key 可能是模拟盘 Key，请取消勾选「模拟盘」；或使用实盘 API Key。"
			}
		} else {
			msg = "login failed: " + msg
		}
		return errors.New(msg)
	}

	// 订阅 orders（SWAP）与 positions
	subscribe := map[string]interface{}{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "orders", "instType": "SWAP"},
			{"channel": "positions", "instType": "SWAP"},
		},
	}
	if err := conn.WriteJSON(subscribe); err != nil {
		_ = conn.Close()
		return err
	}
	var subResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := conn.ReadJSON(&subResp); err != nil {
		_ = conn.Close()
		return err
	}

	go c.readLoop()
	go c.pingLoop()
	log.Printf("[wstrade] 已连接（模拟盘=%v）并订阅 orders/positions", c.auth.Simulated)
	return nil
}

// Close 关闭连接
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	close(c.done)
}

func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			_ = c.conn.Close()
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !c.closed {
				log.Printf("[wstrade] read err: %v", err)
			}
			return
		}

		var generic struct {
			Event string          `json:"event"`
			Arg   json.RawMessage `json:"arg"`
			Data  json.RawMessage `json:"data"`
			Code  string          `json:"code"`
			Msg   string          `json:"msg"`
		}
		if err := json.Unmarshal(data, &generic); err != nil {
			continue
		}

		// 请求-响应类（如下单返回）
		if generic.Event != "" {
			if generic.Code != "" && generic.Code != "0" {
				log.Printf("[wstrade] 响应错误: code=%s msg=%s", generic.Code, generic.Msg)
			}
			continue
		}

		// 推送：arg.channel = orders / positions
		var arg struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(generic.Arg, &arg)

		switch arg.Channel {
		case "orders":
			c.handleOrdersPush(data, generic.Data)
		case "positions":
			c.handlePositionsPush(generic.Data)
		}
	}
}

func (c *Client) handleOrdersPush(full []byte, data json.RawMessage) {
	var list []struct {
		InstType   string `json:"instType"`
		InstId     string `json:"instId"`
		Side       string `json:"side"`
		PosSide    string `json:"posSide"`
		OrdType    string `json:"ordType"`
		TdMode     string `json:"tdMode"`
		Sz         string `json:"sz"`
		Px         string `json:"px"`
		AvgPx      string `json:"avgPx"`
		FillPx     string `json:"fillPx"`
		FillSz     string `json:"fillSz"`
		AccFillSz  string `json:"accFillSz"`
		State      string `json:"state"`
		Fee        string `json:"fee"`
		FeeCcy     string `json:"feeCcy"`
		CTime      int64  `json:"cTime,string"`
		UTime      int64  `json:"uTime,string"`
		FillTime   string `json:"fillTime"`
		TradeId    string `json:"tradeId"`
		ClOrdId    string `json:"clOrdId"`
		Tag        string `json:"tag"`
		Source     string `json:"source"`
		OrdId      string `json:"ordId"`
		Pnl        string `json:"pnl"`
		Lever      string `json:"lever"`
		TpTriggerPx string `json:"tpTriggerPx"`
		SlTriggerPx string `json:"slTriggerPx"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for i, o := range list {
		r := orderlog.Record{
			Id:        o.OrdId + "-" + strconv.FormatInt(o.UTime, 10) + "-" + strconv.Itoa(i),
			OrdId:     o.OrdId,
			InstId:    o.InstId,
			InstType:  o.InstType,
			Side:      o.Side,
			PosSide:   o.PosSide,
			OrdType:   o.OrdType,
			TdMode:    o.TdMode,
			Sz:        o.Sz,
			Px:        o.Px,
			AvgPx:     o.AvgPx,
			FillPx:    o.FillPx,
			FillSz:    o.FillSz,
			AccFillSz: o.AccFillSz,
			State:     o.State,
			Fee:       o.Fee,
			FeeCcy:    o.FeeCcy,
			CTime:     o.CTime,
			UTime:     o.UTime,
			FillTime:  o.FillTime,
			TradeId:   o.TradeId,
			ClOrdId:   o.ClOrdId,
			Tag:       o.Tag,
			Source:    o.Source,
			Raw:       full,
		}
		if c.orderLog != nil {
			c.orderLog.Add(r)
		}
		c.mu.Lock()
		fn := c.onOrder
		c.mu.Unlock()
		if fn != nil {
			fn(r)
		}
	}
}

func (c *Client) handlePositionsPush(data json.RawMessage) {
	var list []Position
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	c.mu.Lock()
	c.positions = list
	c.mu.Unlock()
}

func (c *Client) pingLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn == nil {
				return
			}
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
		}
	}
}

// GetPositions 返回当前持仓快照（来自 positions 频道）
func (c *Client) GetPositions(instId string) []Position {
	c.mu.Lock()
	defer c.mu.Unlock()
	if instId == "" {
		out := make([]Position, len(c.positions))
		copy(out, c.positions)
		return out
	}
	var out []Position
	for _, p := range c.positions {
		if p.InstId == instId {
			out = append(out, p)
		}
	}
	return out
}

// PlaceOrder 通过 WebSocket 下单；参数与 REST 一致
func (c *Client) PlaceOrder(instId, tdMode, side, ordType, sz, px, tgtCcy string) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errNotConnected
	}
	args := map[string]string{
		"instId":  instId,
		"tdMode":  tdMode,
		"side":    side,
		"ordType": ordType,
		"sz":      sz,
	}
	if px != "" {
		args["px"] = px
	}
	if tgtCcy != "" {
		args["tgtCcy"] = tgtCcy
	}
	msg := map[string]interface{}{
		"op":   "order",
		"args": []map[string]string{args},
	}
	return conn.WriteJSON(msg)
}

