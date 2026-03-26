package main

import (
	"encoding/json"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"github.com/iaping/go-okx/common"
	"go-okx-trading/internal/arb"
	"go-okx-trading/internal/monitor"
	"go-okx-trading/internal/okx"
	"go-okx-trading/internal/orderlog"
	"go-okx-trading/internal/wsfeed"
	"go-okx-trading/internal/wstrade"
)

//go:embed web3
var webFS embed.FS

var (
	monitorStore  *monitor.Store
	wsFeed        *wsfeed.Feed
	orderLogStore *orderlog.Store
	fundingCache  *wsfeed.FundingCache
	wsTradeClient *wstrade.Client
	arbRunner     *arb.Runner
	arbMu         sync.Mutex
)

func main() {
	// 加载 .env 到环境变量（若文件不存在则忽略）
	if err := godotenv.Load(); err != nil {
		log.Printf("[config] 未找到 .env 文件，将使用系统环境变量")
	}

	monitorStore = monitor.NewStore()
	orderLogStore = orderlog.NewStore(5000)
	fundingCache = wsfeed.NewFundingCache()
	wsFeed = wsfeed.New(func(instId, bidPx, askPx string) {
		monitorStore.OnTicker(instId, bidPx, askPx)
	})
	wsFeed.SetOnFundingRate(fundingCache.Set)
	defer func() {
		if wsFeed != nil {
			wsFeed.Close()
		}
		monitorStore.Stop()
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// 静态前端（去掉顶层 web 目录）
	webRoot, _ := fs.Sub(webFS, "web")
	http.Handle("/", http.FileServer(http.FS(webRoot)))

	// API
	http.HandleFunc("/api/balance", handleBalance)
	http.HandleFunc("/api/ticker", handleTicker)
	http.HandleFunc("/api/order/place", handlePlaceOrder)
	http.HandleFunc("/api/order/cancel", handleCancelOrder)
	http.HandleFunc("/api/orders/pending", handleOrdersPending)
	http.HandleFunc("/api/monitor/start", handleMonitorStart)
	http.HandleFunc("/api/monitor/list", handleMonitorList)
	http.HandleFunc("/api/monitor/stop", handleMonitorStop)
	http.HandleFunc("/api/arb/start", handleArbStart)
	http.HandleFunc("/api/arb/stop", handleArbStop)
	http.HandleFunc("/api/arb/status", handleArbStatus)
	http.HandleFunc("/api/orders/records", handleOrdersRecords)
	http.HandleFunc("/api/config/status", handleConfigStatus)

	log.Printf("OKX 量化交易界面: http://localhost:%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, corsMiddleware(http.DefaultServeMux)))
}

func corsMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func getConfig(r *http.Request) (okx.Config, bool) {
	// 优先从请求体/Query 的 JSON 或 form 取，否则从环境变量
	apiKey := r.URL.Query().Get("apiKey")
	secretKey := r.URL.Query().Get("secretKey")
	passphrase := r.URL.Query().Get("passphrase")
	if apiKey == "" {
		apiKey = os.Getenv("OKX_API_KEY")
	}
	if secretKey == "" {
		secretKey = os.Getenv("OKX_SECRET_KEY")
	}
	if passphrase == "" {
		passphrase = os.Getenv("OKX_PASSPHRASE")
	}
	if apiKey != "" && secretKey != "" && passphrase != "" {
		simulated := strings.ToLower(os.Getenv("OKX_SIMULATED")) == "1" || strings.ToLower(os.Getenv("OKX_SIMULATED")) == "true"
		return okx.Config{ApiKey: apiKey, SecretKey: secretKey, Passphrase: passphrase, Simulated: simulated}, true
	}
	return okx.Config{}, false
}

func parseBodyConfig(r *http.Request) (okx.Config, bool) {
	var body struct {
		ApiKey     string `json:"apiKey"`
		SecretKey  string `json:"secretKey"`
		Passphrase string `json:"passphrase"`
		Simulated  bool   `json:"simulated"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.ApiKey != "" && body.SecretKey != "" && body.Passphrase != "" {
		return okx.Config{
			ApiKey: body.ApiKey, SecretKey: body.SecretKey, Passphrase: body.Passphrase, Simulated: body.Simulated,
		}, true
	}
	return getConfig(r)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]string{"error": msg})
}

func handleBalance(w http.ResponseWriter, r *http.Request) {
	cfg, ok := parseBodyConfig(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺少 API 配置：请在界面配置或设置环境变量 OKX_API_KEY, OKX_SECRET_KEY, OKX_PASSPHRASE")
		return
	}
	ccy := r.URL.Query().Get("ccy")
	client := okx.NewClient(cfg)
	resp, err := client.GetBalance(ccy)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}

func handleTicker(w http.ResponseWriter, r *http.Request) {
	instId := r.URL.Query().Get("instId")
	if instId == "" {
		instId = "BTC-USDT"
	}
	client := okx.NewClient(okx.Config{}) // 公开接口无需认证
	resp, err := client.GetTicker(instId)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}

func handlePlaceOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var body struct {
		okx.Config
		InstId  string `json:"instId"`
		TdMode  string `json:"tdMode"`
		Side    string `json:"side"`
		OrdType string `json:"ordType"`
		Sz      string `json:"sz"`
		Px      string `json:"px"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体 JSON 无效")
		return
	}
	if body.ApiKey == "" || body.SecretKey == "" || body.Passphrase == "" {
		cfg, ok := getConfig(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "缺少 API 配置")
			return
		}
		body.Config = cfg
	}
	if body.TdMode == "" {
		body.TdMode = "cash"
	}
	client := okx.NewClient(body.Config)
	resp, err := client.PlaceOrder(body.InstId, body.TdMode, body.Side, body.OrdType, body.Sz, body.Px)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}

func handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var body struct {
		okx.Config
		InstId string `json:"instId"`
		OrdId  string `json:"ordId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体 JSON 无效")
		return
	}
	if body.ApiKey == "" || body.SecretKey == "" || body.Passphrase == "" {
		cfg, ok := getConfig(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "缺少 API 配置")
			return
		}
		body.Config = cfg
	}
	client := okx.NewClient(body.Config)
	resp, err := client.CancelOrder(body.InstId, body.OrdId)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}

// handleConfigStatus 返回是否已配置服务端 API（环境变量），不返回任何密钥
func handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	apiKey := os.Getenv("OKX_API_KEY")
	secretKey := os.Getenv("OKX_SECRET_KEY")
	passphrase := os.Getenv("OKX_PASSPHRASE")
	simulated := strings.ToLower(os.Getenv("OKX_SIMULATED")) == "1" || strings.ToLower(os.Getenv("OKX_SIMULATED")) == "true"
	hasServerConfig := apiKey != "" && secretKey != "" && passphrase != ""
	writeJSON(w, map[string]interface{}{
		"hasServerConfig": hasServerConfig,
		"simulated":       hasServerConfig && simulated,
	})
}

func handleOrdersPending(w http.ResponseWriter, r *http.Request) {
	cfg, ok := parseBodyConfig(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺少 API 配置")
		return
	}
	instType := r.URL.Query().Get("instType")
	if instType == "" {
		instType = "SPOT"
	}
	instId := r.URL.Query().Get("instId")
	client := okx.NewClient(cfg)
	resp, err := client.GetOrdersPending(instType, instId)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}

func handleMonitorStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var body struct {
		okx.Config
		InstId       string  `json:"instId"`
		EntryPrice   string  `json:"entryPrice"`
		Sz           string  `json:"sz"`
		Side         string  `json:"side"`
		StopLossPct  float64 `json:"stopLossPct"`
		TrailType    string  `json:"trailType"`
		TrailValue   float64 `json:"trailValue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体 JSON 无效")
		return
	}
	if body.ApiKey == "" || body.SecretKey == "" || body.Passphrase == "" {
		cfg, ok := getConfig(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "缺少 API 配置")
			return
		}
		body.Config = cfg
	}
	if body.InstId == "" || body.EntryPrice == "" || body.Sz == "" {
		writeErr(w, http.StatusBadRequest, "缺少 instId / entryPrice / sz")
		return
	}
	if body.Side == "" {
		body.Side = "buy"
	}
	if body.TrailType != "pct" && body.TrailType != "u" {
		body.TrailType = "pct"
	}
	e := &monitor.Entry{
		InstId: body.InstId, EntryPrice: body.EntryPrice, Sz: body.Sz, Side: body.Side,
		StopLossPct: body.StopLossPct, TrailType: body.TrailType, TrailValue: body.TrailValue,
		Config: body.Config,
	}
	monitorStore.Add(e)
	if wsFeed != nil {
		wsFeed.Subscribe(e.InstId)
	}
	writeJSON(w, map[string]string{"id": e.Id, "msg": "已开始监控（WebSocket 买一卖一）"})
}

func handleMonitorList(w http.ResponseWriter, r *http.Request) {
	list := monitorStore.List()
	writeJSON(w, map[string]interface{}{"data": list})
}

func handleMonitorStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var body struct {
		Id string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := body.Id
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	entry := monitorStore.Get(id)
	monitorStore.Remove(id)
	if entry != nil && wsFeed != nil {
		ids := monitorStore.InstIds()
		stillNeeded := false
		for _, x := range ids {
			if x == entry.InstId {
				stillNeeded = true
				break
			}
		}
		if !stillNeeded {
			wsFeed.Unsubscribe(entry.InstId)
		}
	}
	writeJSON(w, map[string]string{"msg": "已停止监控"})
}

// handleArbStart 启动逐仓套利：外部传入下单金额，无仓则按策略开多/开空，有仓则监控平仓
func handleArbStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var body struct {
		okx.Config
		InstId       string  `json:"instId"`       // 如 BTC-USDT（现货 ID，永续自动为 BTC-USDT-SWAP）
		OrderAmt     string  `json:"orderAmt"`     // 下单金额（USDT），外部传入
		StopLossPct  float64 `json:"stopLossPct"`
		TrailType    string  `json:"trailType"`
		TrailValue   float64 `json:"trailValue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体 JSON 无效")
		return
	}
	if body.ApiKey == "" || body.SecretKey == "" || body.Passphrase == "" {
		cfg, ok := getConfig(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "缺少 API 配置")
			return
		}
		body.Config = cfg
	}
	if body.InstId == "" {
		body.InstId = "BTC-USDT"
	}
	if body.OrderAmt == "" {
		body.OrderAmt = os.Getenv("ARB_ORDER_AMT")
	}
	if body.OrderAmt == "" {
		writeErr(w, http.StatusBadRequest, "缺少 orderAmt（或设置环境变量 ARB_ORDER_AMT）")
		return
	}
	if body.TrailType != "pct" && body.TrailType != "u" {
		body.TrailType = "pct"
	}
	arbMu.Lock()
	if arbRunner != nil {
		arbRunner.Stop()
		arbRunner = nil
	}
	if wsTradeClient != nil {
		wsTradeClient.Close()
		wsTradeClient = nil
	}

	auth := common.NewAuth("", body.ApiKey, body.SecretKey, body.Passphrase, body.Simulated)
	wsTradeClient = wstrade.NewClient(auth, orderLogStore)
	if err := wsTradeClient.Connect(); err != nil {
		arbMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "WebSocket 交易连接失败: "+err.Error())
		return
	}

	wsFeed.SetSimulated(body.Simulated)
	swapInstId := body.InstId
	if swapInstId != "" && (len(swapInstId) < 5 || swapInstId[len(swapInstId)-5:] != "-SWAP") {
		swapInstId = body.InstId + "-SWAP"
	}
	if wsFeed != nil && swapInstId != "" {
		wsFeed.SubscribeFundingRate(swapInstId)
	}

	monitorStore.SetPlaceOrder(func(instId, tdMode, side, ordType, sz, px string) error {
		return wsTradeClient.PlaceOrder(instId, tdMode, side, ordType, sz, px, "")
	})
	runner := arb.NewRunner(body.Config, body.InstId, body.OrderAmt, body.StopLossPct, body.TrailType, body.TrailValue, monitorStore)
	runner.SetTrader(&arb.WSTraderAdapter{Client: wsTradeClient})
	runner.SetGetFundingRate(fundingCache.Get)
	if wsFeed != nil {
		runner.SetSubscribe(wsFeed.Subscribe, wsFeed.Unsubscribe)
	}
	arbRunner = runner
	arbMu.Unlock()
	runner.Start()
	sim := "false"
	if body.Simulated {
		sim = "true"
	}
	writeJSON(w, map[string]string{"msg": "套利已启动（全 WebSocket，模拟盘=" + sim + "，买卖由内部执行，订单已记录）"})
}

func handleArbStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	arbMu.Lock()
	if arbRunner != nil {
		arbRunner.Stop()
		arbRunner = nil
	}
	if wsTradeClient != nil {
		wsTradeClient.Close()
		wsTradeClient = nil
	}
	monitorStore.SetPlaceOrder(nil)
	arbMu.Unlock()
	writeJSON(w, map[string]string{"msg": "套利已停止"})
}

func handleArbStatus(w http.ResponseWriter, r *http.Request) {
	arbMu.Lock()
	running := arbRunner != nil
	arbMu.Unlock()
	writeJSON(w, map[string]interface{}{"running": running})
}

func handleOrdersRecords(w http.ResponseWriter, r *http.Request) {
	n := 0
	if nStr := r.URL.Query().Get("n"); nStr != "" {
		n, _ = strconv.Atoi(nStr)
	}
	list := orderLogStore.ListN(n)
	writeJSON(w, map[string]interface{}{"data": list})
}
