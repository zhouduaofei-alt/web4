package arb

import "go-okx-trading/internal/wstrade"

// WSTraderAdapter 将 wstrade.Client 适配为 arb.Trader
type WSTraderAdapter struct {
	*wstrade.Client
}

// GetPositions 实现 Trader，返回 PositionInfo 列表
func (w *WSTraderAdapter) GetPositions(instId string) []PositionInfo {
	rows := w.Client.GetPositions(instId)
	out := make([]PositionInfo, 0, len(rows))
	for _, p := range rows {
		out = append(out, PositionInfo{InstId: p.InstId, PosSide: p.PosSide, Pos: p.Pos, AvgPx: p.AvgPx})
	}
	return out
}
