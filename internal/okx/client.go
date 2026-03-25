package okx

import (
	"github.com/iaping/go-okx/common"
	"github.com/iaping/go-okx/rest"
	"github.com/iaping/go-okx/rest/api/account"
	"github.com/iaping/go-okx/rest/api/market"
	"github.com/iaping/go-okx/rest/api/trade"
)

// Config 为 API 认证与是否模拟盘
type Config struct {
	ApiKey     string `json:"apiKey"`
	SecretKey  string `json:"secretKey"`
	Passphrase string `json:"passphrase"`
	Simulated  bool   `json:"simulated"`
}

// Client 封装 OKX REST 调用
type Client struct {
	rest *rest.Client
}

func NewClient(cfg Config) *Client {
	auth := common.NewAuth("", cfg.ApiKey, cfg.SecretKey, cfg.Passphrase, cfg.Simulated)
	c := rest.New("", auth, nil)
	return &Client{rest: c}
}

// GetBalance 获取账户余额（可传空字符串查全部）
func (c *Client) GetBalance(ccy string) (*account.GetBalanceResponse, error) {
	param := &account.GetBalanceParam{Ccy: ccy}
	req, resp := account.NewGetBalance(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*account.GetBalanceResponse), nil
}

// GetTicker 获取单个交易对行情
func (c *Client) GetTicker(instId string) (*market.GetTickerResponse, error) {
	param := &market.GetTickerParam{InstId: instId}
	req, resp := market.NewGetTicker(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*market.GetTickerResponse), nil
}

// PlaceOrder 下单。ordType: "market" | "limit"，side: "buy" | "sell"
// 现货 tdMode 一般为 "cash"，限价单需填 px，市价单可不填 px
func (c *Client) PlaceOrder(instId, tdMode, side, ordType, sz, px string) (*trade.PostOrderResponse, error) {
	return c.PlaceOrderWithTarget(instId, tdMode, side, ordType, sz, px, "")
}

// PlaceOrderWithTarget 下单，支持 tgtCcy（如 quote_ccy 表示 sz 为计价币数量，用于永续按金额开仓）
func (c *Client) PlaceOrderWithTarget(instId, tdMode, side, ordType, sz, px, tgtCcy string) (*trade.PostOrderResponse, error) {
	param := &trade.PostOrderParam{
		InstId:  instId,
		TdMode:  tdMode,
		Side:    side,
		OrdType: ordType,
		Sz:      sz,
		Px:      px,
		TgtCcy:  tgtCcy,
	}
	req, resp := trade.NewPostOrder(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*trade.PostOrderResponse), nil
}

// CancelOrder 撤单
func (c *Client) CancelOrder(instId, ordId string) (*trade.PostCancelOrderResponse, error) {
	param := &trade.PostCancelOrderParam{InstId: instId, OrdId: ordId}
	req, resp := trade.NewPostCancelOrder(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*trade.PostCancelOrderResponse), nil
}

// GetOrdersPending 获取未成交委托
func (c *Client) GetOrdersPending(instType, instId string) (*trade.GetOrderResponse, error) {
	param := &trade.GetOrdersQueryParam{InstType: instType, InstId: instId}
	req, resp := trade.NewGetOrdersPending(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*trade.GetOrderResponse), nil
}

// GetOrder 根据订单 ID 查询
func (c *Client) GetOrder(instId, ordId string) (*trade.GetOrderResponse, error) {
	param := &trade.GetOrderParam{InstId: instId, OrdId: ordId}
	req, resp := trade.NewGetOrder(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*trade.GetOrderResponse), nil
}

// GetPositions 获取持仓。instType: "SWAP" 等，instId 可选
func (c *Client) GetPositions(instType, instId string) (*account.GetPositionsResponse, error) {
	param := &account.GetPositionsParam{InstType: instType, InstId: instId}
	req, resp := account.NewGetPositions(param)
	err := c.rest.Do(req, resp)
	if err != nil {
		return nil, err
	}
	return resp.(*account.GetPositionsResponse), nil
}
