package wsfeed

import "sync"

// FundingCache 缓存各合约最新资金费率（由 SetOnFundingRate 回调写入）
type FundingCache struct {
	mu   sync.RWMutex
	data map[string]string
}

func NewFundingCache() *FundingCache {
	return &FundingCache{data: make(map[string]string)}
}

func (f *FundingCache) Set(instId, rate string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[instId] = rate
}

func (f *FundingCache) Get(instId string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.data[instId]
}
