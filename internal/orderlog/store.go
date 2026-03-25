package orderlog

import (
	"encoding/json"
	"sync"
	"time"
)

// Record 单条订单记录，保留完整数据
type Record struct {
	Id         string          `json:"id"`          // 本地唯一 id（时间戳+ordId）
	OrdId      string          `json:"ordId"`
	InstId     string          `json:"instId"`
	InstType   string          `json:"instType"`
	Side       string          `json:"side"`
	PosSide    string          `json:"posSide"`
	OrdType    string          `json:"ordType"`
	TdMode     string          `json:"tdMode"`
	Sz         string          `json:"sz"`
	Px         string          `json:"px"`
	AvgPx      string          `json:"avgPx"`
	FillPx     string          `json:"fillPx"`
	FillSz     string          `json:"fillSz"`
	AccFillSz  string          `json:"accFillSz"`
	State      string          `json:"state"`
	Fee        string          `json:"fee"`
	FeeCcy     string          `json:"feeCcy"`
	CTime      int64           `json:"cTime"`
	UTime      int64           `json:"uTime"`
	FillTime   string          `json:"fillTime"`
	TradeId    string          `json:"tradeId"`
	ClOrdId    string          `json:"clOrdId"`
	Tag        string          `json:"tag"`
	Source     string          `json:"source"`
	ReceivedAt time.Time       `json:"receivedAt"` // 本地收到推送时间
	Raw        json.RawMessage `json:"raw"`        // 原始推送 JSON，保证数据全
}

// Store 订单记录存储
type Store struct {
	mu   sync.RWMutex
	list []Record
	max  int
}

func NewStore(maxRecords int) *Store {
	if maxRecords <= 0 {
		maxRecords = 5000
	}
	return &Store{list: make([]Record, 0, maxRecords), max: maxRecords}
}

// Add 追加一条记录（同一 ordId 可能多次推送，每次均记录）
func (s *Store) Add(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ReceivedAt = time.Now()
	s.list = append(s.list, r)
	if len(s.list) > s.max {
		s.list = s.list[len(s.list)-s.max:]
	}
}

// List 返回全部记录（倒序，最新在前）
func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, len(s.list))
	for i, r := range s.list {
		out[len(s.list)-1-i] = r
	}
	return out
}

// ListN 返回最近 n 条
func (s *Store) ListN(n int) []Record {
	all := s.List()
	if n <= 0 || n >= len(all) {
		return all
	}
	return all[:n]
}
