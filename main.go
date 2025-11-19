package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"
	"github.com/joho/godotenv"
	"github.com/sdcoffey/techan"
	"github.com/shopspring/decimal"
)

var (
	deepseekKey string
	grokKey     string
)

type Candle struct {
	Time                           time.Time
	Open, High, Low, Close, Volume decimal.Decimal
}

type SupportResistance struct {
	Price    float64
	Strength int
	Type     string // "support" or "resistance"
	Touches  int
}

type cluster struct {
	Price    float64
	Count    int
	Elements []float64
}

type Pattern struct {
	Name        string
	Type        string // "bullish", "bearish", "continuation"
	Confidence  float64
	Description string
	Breakout    bool
}

func main() {
	godotenv.Load()
	deepseekKey = os.Getenv("DEEPSEEK_API_KEY")
	grokKey = os.Getenv("GROK_API_KEY")

	if deepseekKey == "" && grokKey == "" {
		log.Fatal("⚠️  Isi minimal satu API key di .env!")
	}

	coinInput := askInput("Type coin name (contoh: sol, btc, eth): ")
	symbol := strings.ToUpper(strings.TrimSpace(coinInput)) + "USDT"

	tf := askInput("Input timeframe (15m / 1h / 4h / 1d): ")
	tf = strings.TrimSpace(tf)
	validTF := map[string]bool{"15m": true, "1h": true, "4h": true, "1d": true, "5m": true, "30m": true, "2h": true, "6h": true, "12h": true}
	if !validTF[tf] {
		log.Fatal("Timeframe tidak valid! Pilih: 15m, 1h, 4h, 1d, dll")
	}

	ai := strings.ToLower(askInput("Choose AI (deepseek / grok): "))
	if ai != "deepseek" && ai != "grok" {
		log.Fatal("Pilih deepseek atau grok!")
	}

	fmt.Printf("\n🔥 Mengambil %s %s + analisa pakai %s...\n\n", symbol, tf, strings.ToUpper(ai))

	candles, series := fetchData(symbol, tf)
	
	// Tambahan: Deteksi Support/Resistance dan Patterns
	srLevels := detectSupportResistance(candles)
	patterns := detectPatterns(candles)
	
	generateTradingChart(candles, series, symbol, tf, srLevels, patterns)

	var analysis string
	if ai == "deepseek" && deepseekKey != "" {
		analysis = callDeepSeek(candles, series, symbol, tf, srLevels, patterns)
	} else if grokKey != "" {
		analysis = callGrok(candles, series, symbol, tf, srLevels, patterns)
	} else {
		log.Fatal("API key untuk AI yang dipilih kosong!")
	}

	printBeautifulAnalysis(analysis, symbol, tf, srLevels, patterns)
}

func askInput(prompt string) string {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	return scanner.Text()
}

func fetchData(symbol, interval string) ([]Candle, *techan.TimeSeries) {
	url := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&limit=300", symbol, interval)
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal("Gagal ambil data Binance:", err)
	}
	defer resp.Body.Close()

	var raw [][]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		log.Fatal("Error decode JSON:", err)
	}

	series := techan.NewTimeSeries()
	var candles []Candle
	for _, k := range raw {
		ts := time.Unix(int64(k[0].(float64))/1000, 0)
		o, _ := decimal.NewFromString(k[1].(string))
		h, _ := decimal.NewFromString(k[2].(string))
		l, _ := decimal.NewFromString(k[3].(string))
		c, _ := decimal.NewFromString(k[4].(string))
		v, _ := decimal.NewFromString(k[5].(string))

		candles = append(candles, Candle{ts, o, h, l, c, v})

		tc := techan.NewCandle(techan.TimePeriod{Start: ts, End: ts.Add(time.Hour)})
		tc.OpenPrice, tc.HighPrice, tc.LowPrice, tc.ClosePrice, tc.Volume = o, h, l, c, v
		series.AddCandle(tc)
	}
	return candles, series
}

// ================================
// SUPPORT/RESISTANCE DETECTION
// ================================

func detectSupportResistance(candles []Candle) []SupportResistance {
	var swingHighs, swingLows []float64
	
	// Deteksi Swing Highs dan Swing Lows
	for i := 2; i < len(candles)-2; i++ {
		high := candles[i].High.InexactFloat64()
		low := candles[i].Low.InexactFloat64()
		
		// Swing High: higher than neighbors
		if high > candles[i-2].High.InexactFloat64() && 
		   high > candles[i-1].High.InexactFloat64() &&
		   high > candles[i+1].High.InexactFloat64() &&
		   high > candles[i+2].High.InexactFloat64() {
			swingHighs = append(swingHighs, high)
		}
		
		// Swing Low: lower than neighbors
		if low < candles[i-2].Low.InexactFloat64() && 
		   low < candles[i-1].Low.InexactFloat64() &&
		   low < candles[i+1].Low.InexactFloat64() &&
		   low < candles[i+2].Low.InexactFloat64() {
			swingLows = append(swingLows, low)
		}
	}
	
	// Clustering levels dengan tolerance 2%
	resistanceLevels := clusterLevels(swingHighs, 0.02)
	supportLevels := clusterLevels(swingLows, 0.02)
	
	var srLevels []SupportResistance
	
	// Process resistance levels
	for _, cluster := range resistanceLevels {
		if cluster.Count >= 2 { // Minimal 2 touches untuk dianggap valid
			srLevels = append(srLevels, SupportResistance{
				Price:    cluster.Price,
				Strength: cluster.Count,
				Type:     "resistance",
				Touches:  cluster.Count,
			})
		}
	}
	
	// Process support levels
	for _, cluster := range supportLevels {
		if cluster.Count >= 2 { // Minimal 2 touches untuk dianggap valid
			srLevels = append(srLevels, SupportResistance{
				Price:    cluster.Price,
				Strength: cluster.Count,
				Type:     "support",
				Touches:  cluster.Count,
			})
		}
	}
	
	// Sort by strength (number of touches)
	sort.Slice(srLevels, func(i, j int) bool {
		return srLevels[i].Strength > srLevels[j].Strength
	})
	
	// Return top 5 strongest levels
	if len(srLevels) > 5 {
		return srLevels[:5]
	}
	return srLevels
}

func clusterLevels(prices []float64, tolerance float64) []cluster {
	if len(prices) == 0 {
		return nil
	}
	
	sort.Float64s(prices)
	var clusters []cluster
	
	for _, price := range prices {
		found := false
		for i := range clusters {
			avg := clusters[i].Price
			if math.Abs(price-avg)/avg <= tolerance {
				clusters[i].Elements = append(clusters[i].Elements, price)
				clusters[i].Count = len(clusters[i].Elements)
				// Recalculate average
				sum := 0.0
				for _, p := range clusters[i].Elements {
					sum += p
				}
				clusters[i].Price = sum / float64(clusters[i].Count)
				found = true
				break
			}
		}
		
		if !found {
			clusters = append(clusters, cluster{
				Price:    price,
				Count:    1,
				Elements: []float64{price},
			})
		}
	}
	
	// Filter clusters dengan minimal 2 elements
	var filteredClusters []cluster
	for _, c := range clusters {
		if c.Count >= 2 {
			filteredClusters = append(filteredClusters, c)
		}
	}
	
	return filteredClusters
}

// ================================
// PATTERN RECOGNITION
// ================================

func detectPatterns(candles []Candle) []Pattern {
	var patterns []Pattern
	
	// Deteksi semua pattern
	if hs := detectHeadAndShoulders(candles); hs.Confidence > 0 {
		patterns = append(patterns, hs)
	}
	if ihs := detectInverseHeadAndShoulders(candles); ihs.Confidence > 0 {
		patterns = append(patterns, ihs)
	}
	if dt := detectDoubleTop(candles); dt.Confidence > 0 {
		patterns = append(patterns, dt)
	}
	if db := detectDoubleBottom(candles); db.Confidence > 0 {
		patterns = append(patterns, db)
	}
	if at := detectAscendingTriangle(candles); at.Confidence > 0 {
		patterns = append(patterns, at)
	}
	if dtri := detectDescendingTriangle(candles); dtri.Confidence > 0 {
		patterns = append(patterns, dtri)
	}
	if st := detectSymmetricalTriangle(candles); st.Confidence > 0 {
		patterns = append(patterns, st)
	}
	
	// Sort by confidence
	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Confidence > patterns[j].Confidence
	})
	
	return patterns
}

func detectHeadAndShoulders(candles []Candle) Pattern {
	if len(candles) < 20 {
		return Pattern{Confidence: 0}
	}
	
	// Cari pattern dalam window 20 candles terakhir
	recent := candles[len(candles)-20:]
	
	var peaks []struct {
		Index int
		High  float64
	}
	
	// Deteksi peaks
	for i := 2; i < len(recent)-2; i++ {
		if recent[i].High.InexactFloat64() > recent[i-1].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i-2].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i+1].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i+2].High.InexactFloat64() {
			peaks = append(peaks, struct {
				Index int
				High  float64
			}{i, recent[i].High.InexactFloat64()})
		}
	}
	
	if len(peaks) < 3 {
		return Pattern{Confidence: 0}
	}
	
	// Cari pattern Head and Shoulders: L-R-L (left shoulder, head, right shoulder)
	for i := 0; i < len(peaks)-2; i++ {
		left, head, right := peaks[i], peaks[i+1], peaks[i+2]
		
		// Head harus lebih tinggi dari shoulders
		if head.High > left.High && head.High > right.High {
			// Shoulders harus memiliki tinggi yang seimbang (dalam tolerance 3%)
			shoulderDiff := math.Abs(left.High-right.High) / math.Max(left.High, right.High)
			if shoulderDiff <= 0.03 {
				conf := 0.7 + (0.3 * (1 - shoulderDiff)) // Confidence berdasarkan symmetry
				return Pattern{
					Name:        "Head and Shoulders",
					Type:        "bearish",
					Confidence:  math.Min(conf, 0.95),
					Description: "Reversal pattern menunjukkan trend bearish",
					Breakout:    false,
				}
			}
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectInverseHeadAndShoulders(candles []Candle) Pattern {
	if len(candles) < 20 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-20:]
	
	var troughs []struct {
		Index int
		Low   float64
	}
	
	// Deteksi troughs (lows)
	for i := 2; i < len(recent)-2; i++ {
		if recent[i].Low.InexactFloat64() < recent[i-1].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i-2].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i+1].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i+2].Low.InexactFloat64() {
			troughs = append(troughs, struct {
				Index int
				Low   float64
			}{i, recent[i].Low.InexactFloat64()})
		}
	}
	
	if len(troughs) < 3 {
		return Pattern{Confidence: 0}
	}
	
	// Cari pattern Inverse Head and Shoulders: L-R-L (left shoulder, head, right shoulder)
	for i := 0; i < len(troughs)-2; i++ {
		left, head, right := troughs[i], troughs[i+1], troughs[i+2]
		
		// Head harus lebih rendah dari shoulders
		if head.Low < left.Low && head.Low < right.Low {
			// Shoulders harus memiliki rendah yang seimbang (dalam tolerance 3%)
			shoulderDiff := math.Abs(left.Low-right.Low) / math.Max(left.Low, right.Low)
			if shoulderDiff <= 0.03 {
				conf := 0.7 + (0.3 * (1 - shoulderDiff))
				return Pattern{
					Name:        "Inverse Head and Shoulders",
					Type:        "bullish",
					Confidence:  math.Min(conf, 0.95),
					Description: "Reversal pattern menunjukkan trend bullish",
					Breakout:    false,
				}
			}
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectDoubleTop(candles []Candle) Pattern {
	if len(candles) < 15 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-15:]
	
	var peaks []struct {
		Index int
		High  float64
	}
	
	// Deteksi peaks
	for i := 2; i < len(recent)-2; i++ {
		if recent[i].High.InexactFloat64() > recent[i-1].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i-2].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i+1].High.InexactFloat64() &&
		   recent[i].High.InexactFloat64() > recent[i+2].High.InexactFloat64() {
			peaks = append(peaks, struct {
				Index int
				High  float64
			}{i, recent[i].High.InexactFloat64()})
		}
	}
	
	if len(peaks) < 2 {
		return Pattern{Confidence: 0}
	}
	
	// Cari dua peaks dengan tinggi yang sama
	for i := 0; i < len(peaks)-1; i++ {
		for j := i + 1; j < len(peaks); j++ {
			diff := math.Abs(peaks[i].High-peaks[j].High) / math.Max(peaks[i].High, peaks[j].High)
			if diff <= 0.02 { // 2% tolerance
				conf := 0.8 * (1 - diff)
				return Pattern{
					Name:        "Double Top",
					Type:        "bearish",
					Confidence:  conf,
					Description: "Reversal pattern menunjukkan resistance kuat",
					Breakout:    false,
				}
			}
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectDoubleBottom(candles []Candle) Pattern {
	if len(candles) < 15 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-15:]
	
	var troughs []struct {
		Index int
		Low   float64
	}
	
	// Deteksi troughs
	for i := 2; i < len(recent)-2; i++ {
		if recent[i].Low.InexactFloat64() < recent[i-1].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i-2].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i+1].Low.InexactFloat64() &&
		   recent[i].Low.InexactFloat64() < recent[i+2].Low.InexactFloat64() {
			troughs = append(troughs, struct {
				Index int
				Low   float64
			}{i, recent[i].Low.InexactFloat64()})
		}
	}
	
	if len(troughs) < 2 {
		return Pattern{Confidence: 0}
	}
	
	// Cari dua troughs dengan rendah yang sama
	for i := 0; i < len(troughs)-1; i++ {
		for j := i + 1; j < len(troughs); j++ {
			diff := math.Abs(troughs[i].Low-troughs[j].Low) / math.Max(troughs[i].Low, troughs[j].Low)
			if diff <= 0.02 { // 2% tolerance
				conf := 0.8 * (1 - diff)
				return Pattern{
					Name:        "Double Bottom",
					Type:        "bullish",
					Confidence:  conf,
					Description: "Reversal pattern menunjukkan support kuat",
					Breakout:    false,
				}
			}
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectAscendingTriangle(candles []Candle) Pattern {
	if len(candles) < 10 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-10:]
	
	// Hitung slope untuk highs dan lows
	highSlope := calculateSlope(recent, true)
	lowSlope := calculateSlope(recent, false)
	
	// Ascending triangle: horizontal resistance, rising support
	if highSlope < 0.001 && lowSlope > 0.001 { // Highs flat, lows rising
		conf := 0.6 + 0.4*math.Min(math.Abs(lowSlope), 0.01)/0.01
		return Pattern{
			Name:        "Ascending Triangle",
			Type:        "bullish",
			Confidence:  math.Min(conf, 0.9),
			Description: "Continuation pattern, bias bullish breakout",
			Breakout:    false,
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectDescendingTriangle(candles []Candle) Pattern {
	if len(candles) < 10 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-10:]
	
	// Hitung slope untuk highs dan lows
	highSlope := calculateSlope(recent, true)
	lowSlope := calculateSlope(recent, false)
	
	// Descending triangle: horizontal support, falling resistance
	if lowSlope > -0.001 && highSlope < -0.001 { // Lows flat, highs falling
		conf := 0.6 + 0.4*math.Min(math.Abs(highSlope), 0.01)/0.01
		return Pattern{
			Name:        "Descending Triangle",
			Type:        "bearish",
			Confidence:  math.Min(conf, 0.9),
			Description: "Continuation pattern, bias bearish breakout",
			Breakout:    false,
		}
	}
	
	return Pattern{Confidence: 0}
}

func detectSymmetricalTriangle(candles []Candle) Pattern {
	if len(candles) < 10 {
		return Pattern{Confidence: 0}
	}
	
	recent := candles[len(candles)-10:]
	
	// Hitung slope untuk highs dan lows
	highSlope := calculateSlope(recent, true)
	lowSlope := calculateSlope(recent, false)
	
	// Symmetrical triangle: converging highs and lows
	if highSlope < -0.001 && lowSlope > 0.001 && math.Abs(highSlope-lowSlope) < 0.002 {
		conf := 0.5 + 0.5*math.Min(math.Abs(highSlope)+math.Abs(lowSlope), 0.02)/0.02
		return Pattern{
			Name:        "Symmetrical Triangle",
			Type:        "continuation",
			Confidence:  math.Min(conf, 0.85),
			Description: "Consolidation pattern, breakout menentukan arah",
			Breakout:    false,
		}
	}
	
	return Pattern{Confidence: 0}
}

func calculateSlope(candles []Candle, useHigh bool) float64 {
	n := float64(len(candles))
	var sumX, sumY, sumXY, sumX2 float64
	
	for i, candle := range candles {
		x := float64(i)
		var y float64
		if useHigh {
			y = candle.High.InexactFloat64()
		} else {
			y = candle.Low.InexactFloat64()
		}
		
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	
	// Linear regression slope
	slope := (n*sumXY - sumX*sumY) / (n*sumX2 - sumX*sumX)
	return slope
}

// PROFESSIONAL TRADING CHART dengan Support/Resistance dan Patterns
func generateTradingChart(candles []Candle, series *techan.TimeSeries, symbol, tf string, srLevels []SupportResistance, patterns []Pattern) {
	close := techan.NewClosePriceIndicator(series)
	ema5 := techan.NewEMAIndicator(close, 5)
	ema10 := techan.NewEMAIndicator(close, 10)
	ema30 := techan.NewEMAIndicator(close, 30)
	bb := techan.NewBollingerBandIndicator(close, 20, 2.0)
	rsi := techan.NewRSIIndicator(close, 14)
	macd := techan.NewMACDIndicator(close, 12, 26)
	signal := techan.NewEMAIndicator(macd, 9)

	var xAxis []string
	var klineData []opts.KlineData
	var volumeData []opts.BarData
	var ema5Data, ema10Data, ema30Data []opts.LineData
	var upperBand, middleBand, lowerBand []opts.LineData
	var rsiData, macdData, signalData []opts.LineData
	var macdHist []opts.BarData

	// Tambahkan data untuk Support/Resistance lines
	var supportLines, resistanceLines []opts.LineData

	for i, c := range candles {
		xAxis = append(xAxis, c.Time.Format("01-02 15:04"))
		
		klineData = append(klineData, opts.KlineData{
			Value: [4]float64{
				c.Open.InexactFloat64(),
				c.Close.InexactFloat64(),
				c.Low.InexactFloat64(),
				c.High.InexactFloat64(),
			},
		})

		// Volume dengan warna sesuai candle
		volColor := "#14b8a6" // teal untuk bullish
		if c.Close.LessThan(c.Open) {
			volColor = "#ef4444" // red untuk bearish
		}
		volumeData = append(volumeData, opts.BarData{
			Value: c.Volume.InexactFloat64(),
			ItemStyle: &opts.ItemStyle{Color: volColor},
		})

		ema5Data = append(ema5Data, opts.LineData{Value: ema5.Calculate(i).InexactFloat64()})
		ema10Data = append(ema10Data, opts.LineData{Value: ema10.Calculate(i).InexactFloat64()})
		ema30Data = append(ema30Data, opts.LineData{Value: ema30.Calculate(i).InexactFloat64()})
		
		upperBand = append(upperBand, opts.LineData{Value: bb.UpperBand(i).InexactFloat64()})
		middleBand = append(middleBand, opts.LineData{Value: bb.MiddleBand(i).InexactFloat64()})
		lowerBand = append(lowerBand, opts.LineData{Value: bb.LowerBand(i).InexactFloat64()})

		rsiData = append(rsiData, opts.LineData{Value: rsi.Calculate(i).InexactFloat64()})
		
		macdVal := macd.Calculate(i).InexactFloat64()
		signalVal := signal.Calculate(i).InexactFloat64()
		macdData = append(macdData, opts.LineData{Value: macdVal})
		signalData = append(signalData, opts.LineData{Value: signalVal})
		
		histVal := macdVal - signalVal
		histColor := "#14b8a6"
		if histVal < 0 {
			histColor = "#ef4444"
		}
		macdHist = append(macdHist, opts.BarData{
			Value: histVal,
			ItemStyle: &opts.ItemStyle{Color: histColor},
		})

		// Support/Resistance data (horizontal lines)
		supportLines = append(supportLines, opts.LineData{Value: 0}) // akan diisi nanti
		resistanceLines = append(resistanceLines, opts.LineData{Value: 0})
	}

	// 1. MAIN CHART - Candlestick + EMA + Bollinger Bands + Support/Resistance
	kline := charts.NewKLine()
	kline.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title:    fmt.Sprintf("%s %s - Support/Resistance & Pattern Detection", symbol, tf),
			Subtitle: "Price • EMA 5/10/30 • Bollinger Bands • S/R Levels",
			Left:     "center",
		}),
		charts.WithXAxisOpts(opts.XAxis{
			SplitNumber: 20,
			Scale:       true,
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Scale: true,
			SplitLine: &opts.SplitLine{Show: true},
		}),
		charts.WithDataZoomOpts(opts.DataZoom{
			Type:       "inside",
			XAxisIndex: []int{0},
			Start:      70,
			End:        100,
		}),
		charts.WithDataZoomOpts(opts.DataZoom{
			Type:       "slider",
			XAxisIndex: []int{0},
			Start:      70,
			End:        100,
		}),
		charts.WithLegendOpts(opts.Legend{
			Show: true,
			Top:  "5%",
		}),
		charts.WithGridOpts(opts.Grid{
			Top:    "15%",
			Left:   "5%",
			Right:  "5%",
			Height: "40%",
		}),
		charts.WithInitializationOpts(opts.Initialization{
			Width:  "1600px",
			Height: "900px",
			Theme:  "dark",
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show:      true,
			Trigger:   "axis",
			AxisPointer: &opts.AxisPointer{Type: "cross"},
		}),
	)

	kline.SetXAxis(xAxis).AddSeries("Candlestick", klineData)

	// Tambah EMA & Bollinger Bands
	line := charts.NewLine()
	line.SetXAxis(xAxis).
		AddSeries("EMA 5", ema5Data,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#ff3b30", Width: 2}),
		).
		AddSeries("EMA 10", ema10Data,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#ff9500", Width: 2}),
		).
		AddSeries("EMA 30", ema30Data,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#007aff", Width: 2}),
		).
		AddSeries("BB Upper", upperBand,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#8e8e93", Width: 1, Type: "dashed", Opacity: 0.5}),
		).
		AddSeries("BB Middle", middleBand,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#8e8e93", Width: 1, Opacity: 0.5}),
		).
		AddSeries("BB Lower", lowerBand,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#8e8e93", Width: 1, Type: "dashed", Opacity: 0.5}),
		)

	// Tambah Support/Resistance lines
	for _, level := range srLevels {
		var lineData []opts.LineData
		color := "#00ff00" // Green for support
		name := fmt.Sprintf("Support %.4f (%d)", level.Price, level.Strength)
		
		if level.Type == "resistance" {
			color = "#ff0000" // Red for resistance
			name = fmt.Sprintf("Resistance %.4f (%d)", level.Price, level.Strength)
		}
		
		for range xAxis {
			lineData = append(lineData, opts.LineData{Value: level.Price})
		}
		
		line.AddSeries(name, lineData,
			charts.WithLineChartOpts(opts.LineChart{Smooth: false}),
			charts.WithLineStyleOpts(opts.LineStyle{
				Color:   color,
				Width:   2,
				Type:    "dashed",
				Opacity: 0.7,
			}),
		)
	}

	kline.Overlap(line)

	// 2. VOLUME CHART
	volume := charts.NewBar()
	volume.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title: "Volume",
			Top:   "58%",
			Left:  "center",
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Show:        false,
			SplitNumber: 20,
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Scale: true,
			SplitLine: &opts.SplitLine{Show: true},
		}),
		charts.WithGridOpts(opts.Grid{
			Top:    "60%",
			Left:   "5%",
			Right:  "5%",
			Height: "10%",
		}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
	)
	volume.SetXAxis(xAxis).AddSeries("Volume", volumeData)

	// 3. RSI CHART
	rsiChart := charts.NewLine()
	rsiChart.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title: "RSI (14)",
			Top:   "73%",
			Left:  "center",
		}),
		charts.WithXAxisOpts(opts.XAxis{
			Show:        false,
			SplitNumber: 20,
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Scale: true,
			Min:   0,
			Max:   100,
			SplitLine: &opts.SplitLine{Show: true},
		}),
		charts.WithGridOpts(opts.Grid{
			Top:    "75%",
			Left:   "5%",
			Right:  "5%",
			Height: "8%",
		}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
	)
	rsiChart.SetXAxis(xAxis).
		AddSeries("RSI", rsiData,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#af52de", Width: 2}),
			charts.WithMarkLineNameTypeItemOpts(
				opts.MarkLineNameTypeItem{Name: "Overbought", YAxis: 70},
				opts.MarkLineNameTypeItem{Name: "Oversold", YAxis: 30},
			),
			charts.WithMarkLineStyleOpts(opts.MarkLineStyle{
				LineStyle: &opts.LineStyle{Color: "#636366", Type: "dashed"},
			}),
		)

	// 4. MACD CHART
	macdChart := charts.NewBar()
	macdChart.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title: "MACD (12,26,9)",
			Top:   "85%",
			Left:  "center",
		}),
		charts.WithXAxisOpts(opts.XAxis{
			SplitNumber: 20,
		}),
		charts.WithYAxisOpts(opts.YAxis{
			Scale: true,
			SplitLine: &opts.SplitLine{Show: true},
		}),
		charts.WithGridOpts(opts.Grid{
			Top:    "87%",
			Left:   "5%",
			Right:  "5%",
			Height: "10%",
		}),
		charts.WithLegendOpts(opts.Legend{Show: true, Top: "85%"}),
	)
	macdChart.SetXAxis(xAxis).AddSeries("Histogram", macdHist)

	macdLine := charts.NewLine()
	macdLine.SetXAxis(xAxis).
		AddSeries("MACD", macdData,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#30d158", Width: 2}),
		).
		AddSeries("Signal", signalData,
			charts.WithLineChartOpts(opts.LineChart{Smooth: true}),
			charts.WithLineStyleOpts(opts.LineStyle{Color: "#ff453a", Width: 2}),
		)

	macdChart.Overlap(macdLine)

	// Gabungkan semua chart dalam satu page
	page := components.NewPage()
	page.AddCharts(kline, volume, rsiChart, macdChart)

	filename := fmt.Sprintf("%s_%s.html", symbol, tf)
	f, _ := os.Create(filename)
	defer f.Close()
	page.Render(f)

	fmt.Printf("✅ Chart disimpan → %s\n", filename)
	
	// Print detected patterns
	if len(patterns) > 0 {
		fmt.Println("\n🎯 DETECTED PATTERNS:")
		for _, pattern := range patterns {
			emoji := "🟢"
			if pattern.Type == "bearish" {
				emoji = "🔴"
			} else if pattern.Type == "continuation" {
				emoji = "🟡"
			}
			fmt.Printf("%s %s (%.0f%% confidence) - %s\n", 
				emoji, pattern.Name, pattern.Confidence*100, pattern.Description)
		}
	}
}

func buildPrompt(series *techan.TimeSeries, symbol, tf string, srLevels []SupportResistance, patterns []Pattern) string {
	close := techan.NewClosePriceIndicator(series)
	ema5 := techan.NewEMAIndicator(close, 5)
	ema10 := techan.NewEMAIndicator(close, 10)
	ema30 := techan.NewEMAIndicator(close, 30)
	bb := techan.NewBollingerBandIndicator(close, 20, 2.0)
	rsi := techan.NewRSIIndicator(close, 14)
	macd := techan.NewMACDIndicator(close, 12, 26)
	signal := techan.NewEMAIndicator(macd, 9)

	last := series.LastIndex()
	macdVal := macd.Calculate(last).InexactFloat64()
	signalVal := signal.Calculate(last).InexactFloat64()
	
	// Build support/resistance string
	var srInfo strings.Builder
	if len(srLevels) > 0 {
		srInfo.WriteString("\n\n🎯 SUPPORT/RESISTANCE LEVELS:")
		for _, level := range srLevels {
			emoji := "🟢"
			if level.Type == "resistance" {
				emoji = "🔴"
			}
			srInfo.WriteString(fmt.Sprintf("\n%s %s: $%.4f (Strength: %d touches)", 
				emoji, strings.ToUpper(level.Type), level.Price, level.Touches))
		}
	}
	
	// Build patterns string
	var patternsInfo strings.Builder
	if len(patterns) > 0 {
		patternsInfo.WriteString("\n\n🎭 DETECTED PATTERNS:")
		for _, pattern := range patterns {
			emoji := "🟢"
			if pattern.Type == "bearish" {
				emoji = "🔴"
			} else if pattern.Type == "continuation" {
				emoji = "🟡"
			}
			patternsInfo.WriteString(fmt.Sprintf("\n%s %s (%s, %.0f%% confidence) - %s", 
				emoji, pattern.Name, pattern.Type, pattern.Confidence*100, pattern.Description))
		}
	}
	
	data := map[string]string{
		"coin":          symbol,
		"timeframe":     tf,
		"price":         close.Calculate(last).StringFixed(4),
		"ema5":          ema5.Calculate(last).StringFixed(4),
		"ema10":         ema10.Calculate(last).StringFixed(4),
		"ema30":         ema30.Calculate(last).StringFixed(4),
		"bb_upper":      bb.UpperBand(last).StringFixed(4),
		"bb_middle":     bb.MiddleBand(last).StringFixed(4),
		"bb_lower":      bb.LowerBand(last).StringFixed(4),
		"rsi":           rsi.Calculate(last).StringFixed(2),
		"macd":          fmt.Sprintf("%.4f", macdVal),
		"macd_signal":   fmt.Sprintf("%.4f", signalVal),
		"macd_hist":     fmt.Sprintf("%.4f", macdVal-signalVal),
	}
	jsonData, _ := json.MarshalIndent(data, "", "  ")

	return fmt.Sprintf(`Analisa %s timeframe %s.

DATA TEKNIKAL:
%s
%s
%s

Jawab DALAM FORMAT INI SAJA:

📊 %s ANALISA (%s)

📈 Trend Saat Ini: ...
🔥 Kekuatan Trend: Strong / Medium / Weak
🎯 Support Kuat: ...
🛡️ Resistance Kuat: ...

⚡ ENTRY ZONE / ORDER SETUP
├ Buy Zone: $xxx - $xxx
├ Entry Trigger: ...
├ Take Profit 1: $xxx (xx%%)
├ Take Profit 2: $xxx (xx%%)
└ Stop Loss: $xxx (risk xx%%)

💡 Rekomendasi: BUY / SELL / HOLD
⚠️ Risk Level: Low / Medium / High

Jawab langsung tanpa kata pengantar!`, symbol, tf, jsonData, srInfo.String(), patternsInfo.String(), symbol, tf)
}

func callDeepSeek(candles []Candle, series *techan.TimeSeries, symbol, tf string, srLevels []SupportResistance, patterns []Pattern) string {
	prompt := buildPrompt(series, symbol, tf, srLevels, patterns)
	payload := map[string]any{
		"model":    "deepseek-chat",
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	return callAI("https://api.deepseek.com/chat/completions", deepseekKey, payload)
}

func callGrok(candles []Candle, series *techan.TimeSeries, symbol, tf string, srLevels []SupportResistance, patterns []Pattern) string {
	prompt := buildPrompt(series, symbol, tf, srLevels, patterns)
	payload := map[string]any{
		"model":    "grok-beta",
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	return callAI("https://api.x.ai/v1/chat/completions", grokKey, payload)
}

func callAI(url, key string, payload map[string]any) string {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "Error API: " + err.Error()
	}
	defer resp.Body.Close()

	var res map[string]any
	json.NewDecoder(resp.Body).Decode(&res)
	choices := res["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	return msg["content"].(string)
}

func printBeautifulAnalysis(text, symbol, tf string, srLevels []SupportResistance, patterns []Pattern) {
	fmt.Println("\n════════════════════════════════════════════════")
	fmt.Printf("       %s ANALISA (%s)\n", symbol, tf)
	fmt.Println("════════════════════════════════════════════════\n")
	
	// Print Support/Resistance summary
	if len(srLevels) > 0 {
		fmt.Println("🎯 KEY LEVELS:")
		for _, level := range srLevels {
			emoji := "🟢"
			if level.Type == "resistance" {
				emoji = "🔴"
			}
			fmt.Printf("%s %s: $%.4f (Strength: %d)\n", emoji, strings.ToUpper(level.Type), level.Price, level.Strength)
		}
		fmt.Println()
	}
	
	// Print Patterns summary
	if len(patterns) > 0 {
		fmt.Println("🎭 PATTERNS DETECTED:")
		for _, pattern := range patterns {
			emoji := "🟢"
			if pattern.Type == "bearish" {
				emoji = "🔴"
			} else if pattern.Type == "continuation" {
				emoji = "🟡"
			}
			fmt.Printf("%s %s (%.0f%% confidence)\n", emoji, pattern.Name, pattern.Confidence*100)
		}
		fmt.Println()
	}
	
	fmt.Println(text)
	fmt.Printf("\n✅ Chart disimpan → %s_%s.html\n", symbol, tf)
	fmt.Println("   Good luck trading, bossku! 🚀🚀🚀")
}
