package arb

import (
	"math"
	"sync"
)

// Signal 策略信号：开多、开空、或平仓（回归中轨）
const (
	SignalLong  = "long"
	SignalShort = "short"
	SignalClose = "close"
	SignalNone  = ""
)

// Params 套利策略参数
type Params struct {
	// FundingRateThreshold 资金费率阈值（绝对值），超过则跟费率方向：正→空，负→多
	FundingRateThreshold float64
	// SpreadPctUpper 价差百分比上轨阈值，超过则开空
	SpreadPctUpper float64
	// SpreadPctLower 价差百分比下轨阈值，低于则开多
	SpreadPctLower float64
	// BBPeriod 布林带周期（价差序列）
	BBPeriod int
	// BBStd 布林带标准差倍数
	BBStd float64
	// SpreadBackToMiddle 价差回归中轨比例（相对中轨距离）视为可平仓
	SpreadBackToMiddle float64
}

// DefaultParams 默认参数
func DefaultParams() Params {
	return Params{
		FundingRateThreshold: 0.0001,
		SpreadPctUpper:       0.1,
		SpreadPctLower:       -0.1,
		BBPeriod:             20,
		BBStd:                2.0,
		SpreadBackToMiddle:   0.02,
	}
}

// Strategy 套利策略：基差 + 资金费率 + 价差布林带
type Strategy struct {
	mu       sync.Mutex
	params   Params
	spreads  []float64
	maxSpreads int
}

// NewStrategy 新建策略
func NewStrategy(p Params) *Strategy {
	if p.BBPeriod <= 0 {
		p.BBPeriod = 20
	}
	maxSpreads := p.BBPeriod + 50
	return &Strategy{
		params:     p,
		spreads:    make([]float64, 0, maxSpreads),
		maxSpreads: maxSpreads,
	}
}

// SpreadPct 价差百分比 = (合约价 - 现货价) / 现货价 * 100
func SpreadPct(spotPx, swapPx float64) float64 {
	if spotPx <= 0 {
		return 0
	}
	return (swapPx - spotPx) / spotPx * 100
}

// Basis 基差 = 合约价 - 现货价
func Basis(spotPx, swapPx float64) float64 {
	return swapPx - spotPx
}

// Decide 根据现货价、合约价、资金费率与当前持仓方向，决定信号
// posSide: "long" | "short" | ""（无仓）
// 返回: SignalLong / SignalShort / SignalClose / SignalNone
func (s *Strategy) Decide(spotPx, swapPx, fundingRate float64, posSide string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	spreadPct := SpreadPct(spotPx, swapPx)

	// 维护价差序列用于布林带
	s.spreads = append(s.spreads, spreadPct)
	if len(s.spreads) > s.maxSpreads {
		s.spreads = s.spreads[len(s.spreads)-s.maxSpreads:]
	}

	p := s.params
	mid, upper, lower := s.bollinger()

	// 有持仓时：优先看是否回归中轨，决定平仓
	if posSide != "" {
		if mid != mid || math.Abs(spreadPct-mid) <= p.SpreadBackToMiddle*math.Max(math.Abs(mid), 0.01) {
			return SignalClose
		}
		// 否则继续持有，不新开
		return SignalNone
	}

	// 无仓：按资金费率与价差决定开多/开空

	// 1. 资金费率：正→多头付钱给空头，开空吃息；负→开多吃息
	if fundingRate >= p.FundingRateThreshold {
		return SignalShort
	}
	if fundingRate <= -p.FundingRateThreshold {
		return SignalLong
	}

	// 2. 价差极值：上方太远开空，下方太远开多
	if spreadPct >= p.SpreadPctUpper {
		return SignalShort
	}
	if spreadPct <= p.SpreadPctLower {
		return SignalLong
	}

	// 3. 布林带：价差触及上轨开空，触及下轨开多
	if upper == upper && spreadPct >= upper {
		return SignalShort
	}
	if lower == lower && spreadPct <= lower {
		return SignalLong
	}

	return SignalNone
}

func (s *Strategy) bollinger() (mid, upper, lower float64) {
	n := len(s.spreads)
	period := s.params.BBPeriod
	if period <= 0 || n < period {
		return 0, math.NaN(), math.NaN()
	}
	slice := s.spreads[n-period:]
	var sum float64
	for _, v := range slice {
		sum += v
	}
	mid = sum / float64(len(slice))
	var varSum float64
	for _, v := range slice {
		varSum += (v - mid) * (v - mid)
	}
	std := math.Sqrt(varSum / float64(len(slice)))
	upper = mid + s.params.BBStd*std
	lower = mid - s.params.BBStd*std
	return mid, upper, lower
}

// MidUpperLower 返回当前布林带中轨、上轨、下轨（用于展示）
func (s *Strategy) MidUpperLower() (mid, upper, lower float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bollinger()
}
