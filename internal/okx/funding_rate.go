package okx

import (
	"github.com/iaping/go-okx/rest/api"
)

// GetFundingRate 使用公开接口获取永续合约资金费率（无需鉴权）
// 请求 GET /api/v5/public/funding-rate?instId=xxx
func (c *Client) GetFundingRate(instId string) (*FundingRateResponse, error) {
	param := &struct {
		InstId string `url:"instId"`
	}{InstId: instId}
	req := &api.Request{
		Path:   "/api/v5/public/funding-rate",
		Method: api.MethodGet,
		Param:  param,
	}
	resp := &FundingRateResponse{}
	if err := c.rest.Do(req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// FundingRateResponse 资金费率接口返回
type FundingRateResponse struct {
	api.Response
	Data []FundingRateItem `json:"data"`
}

// FundingRateItem 单条资金费率
type FundingRateItem struct {
	InstId          string `json:"instId"`
	FundingRate     string `json:"fundingRate"`
	NextFundingTime string `json:"nextFundingTime,omitempty"`
	NextFundingRate string `json:"nextFundingRate,omitempty"`
}
