package main

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// appConfig is the console's wiring to the service.
type appConfig struct {
	base       string
	token      string
	adminToken string
	site       string
	printerID  string
	simulate   int
	seed       int64
}

// PrinterSummary is what the demo console knows about each available printer roll.
type PrinterSummary struct {
	PrinterID     string  `json:"printer_id"`
	Name          string  `json:"name"`
	Site          string  `json:"site"`
	PaperSize     string  `json:"paper_size"`
	MediaWidthMM  float64 `json:"media_width_mm"`
	MediaHeightMM float64 `json:"media_height_mm"`
	Remark        string  `json:"remark"`
	CustomLabel   string  `json:"custom_label"`
	Usable        bool    `json:"usable"`
	Enabled       bool    `json:"enabled"`
	QueueDepth    int     `json:"queue_depth"`
}

// Order is the "business document" this demo pretends to be releasing.
type Order struct {
	ID       string    `json:"id"`
	Customer string    `json:"customer"`
	SKU      string    `json:"sku"`
	Qty      int       `json:"qty"`
	Bin      string    `json:"bin"`
	Lines    []Line    `json:"lines"`
	Created  time.Time `json:"created"`
}

type Line struct {
	No              int            `json:"no"`
	LabelType       string         `json:"label_type"`       // "shipping_4x6", "carton_4x2", "item_2x1"
	LabelName       string         `json:"label_name"`       // Display name
	PaperSize       string         `json:"paper_size"`       // "4x6", "4x2", "2x1"
	WidthMM         float64        `json:"width_mm"`
	HeightMM        float64        `json:"height_mm"`
	SelectedPrinter string         `json:"selected_printer"` // manual override if set
	TargetPrinter   string         `json:"target_printer"`   // computed target printer
	ZPL             string         `json:"-"`
	Status          dispatchResult `json:"status"`
	JobID           string         `json:"job_id,omitempty"`
}

// app holds demo state and the reference integration logic.
type app struct {
	log      *slog.Logger
	cfg      appConfig
	cli      *client
	traces   *traceRecorder
	mu       sync.RWMutex
	ords     []*Order
	printers []PrinterSummary
	live     map[string]string // job_id -> external_id, for live updates
}

func newApp(log *slog.Logger, cfg appConfig) (*app, error) {
	rec := newTraceRecorder(60)
	a := &app{
		log: log, cfg: cfg,
		traces: rec,
		cli: &client{
			base: cfg.base, token: cfg.token,
			http:        &http.Client{Timeout: 30 * time.Second},
			maxAttempts: 4,
			recorder:    rec,
		},
		live: map[string]string{},
		ords: generateOrders(cfg.seed),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.refreshPrinters(ctx); err != nil {
		return nil, fmt.Errorf("cannot read printer fleet: %w", err)
	}
	a.reconcileTargetPrinters()
	return a, nil
}

func (a *app) Run(ctx context.Context) {
	go a.watchJobs(ctx) // SSE, with polling as fallback
	if a.cfg.simulate > 0 {
		go a.simulateLoop(ctx)
	}
}

func (a *app) Printers() []PrinterSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]PrinterSummary(nil), a.printers...)
}

func (a *app) PrinterIDs() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.printers))
	for _, p := range a.printers {
		out = append(out, p.PrinterID)
	}
	return out
}

func (a *app) refreshPrinters(ctx context.Context) error {
	var out struct {
		Items []struct {
			PrinterID     string  `json:"printer_id"`
			Name          string  `json:"name"`
			Site          string  `json:"site"`
			PaperSize     string  `json:"paper_size"`
			MediaWidthMM  float64 `json:"media_width_mm"`
			MediaHeightMM float64 `json:"media_height_mm"`
			Remark        string  `json:"remark"`
			CustomLabel   string  `json:"custom_label"`
			Usable        bool    `json:"usable"`
			Enabled       bool    `json:"enabled"`
			QueueDepth    int     `json:"queue_depth"`
		} `json:"items"`
	}
	if err := a.cli.do(ctx, "GET", "/api/v1/printers/status", a.cfg.token, nil, &out); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.printers = a.printers[:0]
	for _, p := range out.Items {
		if a.cfg.printerID != "" && p.PrinterID != a.cfg.printerID {
			continue
		}
		if a.cfg.site != "" && p.Site != a.cfg.site {
			continue
		}
		a.printers = append(a.printers, PrinterSummary{
			PrinterID:     p.PrinterID,
			Name:          p.Name,
			Site:          p.Site,
			PaperSize:     p.PaperSize,
			MediaWidthMM:  p.MediaWidthMM,
			MediaHeightMM: p.MediaHeightMM,
			Remark:        p.Remark,
			CustomLabel:   p.CustomLabel,
			Usable:        p.Usable,
			Enabled:       p.Enabled,
			QueueDepth:    p.QueueDepth,
		})
	}
	sort.Slice(a.printers, func(i, j int) bool {
		return a.printers[i].PrinterID < a.printers[j].PrinterID
	})
	return nil
}

// reconcileTargetPrinters automatically pairs each line's required roll size
// with an available, usable printer loaded with that exact roll.
func (a *app) reconcileTargetPrinters() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range a.ords {
		for i := range o.Lines {
			line := &o.Lines[i]
			if line.SelectedPrinter != "" {
				line.TargetPrinter = line.SelectedPrinter
				continue
			}
			// Find first usable printer with matching paper_size
			var fallback string
			var matched string
			for _, p := range a.printers {
				if !p.Usable || !p.Enabled {
					continue
				}
				if fallback == "" {
					fallback = p.PrinterID
				}
				if p.PaperSize != "" && p.PaperSize == line.PaperSize {
					matched = p.PrinterID
					break
				}
			}
			if matched != "" {
				line.TargetPrinter = matched
			} else if fallback != "" {
				line.TargetPrinter = fallback
			}
		}
	}
}

// SetLinePrinter allows the operator to manually override the target printer for a line.
func (a *app) SetLinePrinter(orderID string, lineNo int, printerID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range a.ords {
		if o.ID == orderID {
			for i := range o.Lines {
				if o.Lines[i].No == lineNo {
					o.Lines[i].SelectedPrinter = printerID
					o.Lines[i].TargetPrinter = printerID
					return nil
				}
			}
		}
	}
	return fmt.Errorf("order %s line %d not found", orderID, lineNo)
}

// Release dispatches every line of an order to its designated roll printer.
func (a *app) Release(ctx context.Context, orderID string) ([]dispatchResult, error) {
	a.reconcileTargetPrinters()

	a.mu.RLock()
	var ord *Order
	for _, o := range a.ords {
		if o.ID == orderID {
			ord = o
			break
		}
	}
	a.mu.RUnlock()
	if ord == nil {
		return nil, fmt.Errorf("unknown order %s", orderID)
	}

	results := make([]dispatchResult, len(ord.Lines))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for i := range ord.Lines {
		line := &ord.Lines[i]
		externalID := fmt.Sprintf("%s-line-%d", ord.ID, line.No)
		targetPrinter := line.TargetPrinter
		if targetPrinter == "" {
			targetPrinter = line.SelectedPrinter
		}

		if targetPrinter == "" {
			results[i] = dispatchResult{
				State: "rejected",
				Error: fmt.Sprintf("no printer available with %s roll", line.PaperSize),
			}
			line.Status = results[i]
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, line *Line, externalID, printer string) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := a.cli.dispatch(ctx, map[string]any{
				"printer_id":  printer,
				"external_id": externalID,
				"biz_ref":     ord.ID,
				"zpl":         line.ZPL,
				"copies":      1,
				"meta": map[string]string{
					"module":     "demo-web",
					"order":      ord.ID,
					"paper_size": line.PaperSize,
				},
			})
			if err != nil {
				a.log.Warn("dispatch rejected", "order", ord.ID, "line", line.No, "printer", printer, "err", err)
			}
			line.Status = res
			line.JobID = res.JobID
			results[i] = res
			a.mu.Lock()
			if res.JobID != "" {
				a.live[res.JobID] = externalID
			}
			a.mu.Unlock()
		}(i, line, externalID, targetPrinter)
	}
	wg.Wait()
	return results, nil
}

// Resend re-submits an order unchanged. Every line must come back `replayed: true`.
func (a *app) Resend(ctx context.Context, orderID string) ([]dispatchResult, error) {
	return a.Release(ctx, orderID)
}

func (a *app) pollJob(ctx context.Context, jobID string) (*job, error) {
	if jobID == "" {
		return nil, nil
	}
	var j job
	if err := a.cli.do(ctx, "GET", "/api/v1/jobs/"+jobID, a.cfg.token, nil, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (a *app) watchJobs(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.streamEvents(ctx); err != nil {
			a.log.Debug("event stream ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *app) streamEvents(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.cfg.base+"/api/v1/events?topics=job.state", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.cli.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream http %d", resp.StatusCode)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var frame struct {
			Event string `json:"event"`
			Data  string `json:"data"`
		}
		if err := dec.Decode(&frame); err != nil {
			if errors_IsEOF(err) {
				return nil
			}
			return err
		}
		if frame.Event != "job.state" || frame.Data == "" {
			continue
		}
		var ev struct {
			JobID    string `json:"job_id"`
			State    string `json:"state"`
			Attempts int    `json:"attempts"`
		}
		if err := json.Unmarshal([]byte(frame.Data), &ev); err != nil {
			continue
		}
		a.applyState(ev.JobID, ev.State, ev.Attempts)
	}
}

func (a *app) applyState(jobID, state string, attempts int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range a.ords {
		for i := range o.Lines {
			if o.Lines[i].JobID == jobID {
				o.Lines[i].Status.State = state
				o.Lines[i].Status.Attempts = attempts
				o.Lines[i].Status.UpdatedAt = time.Now()
				return
			}
		}
	}
}

func (a *app) reconcile(ctx context.Context) {
	a.mu.RLock()
	var pending [][2]any
	for _, o := range a.ords {
		for i := range o.Lines {
			if o.Lines[i].JobID != "" && o.Lines[i].Status.State != "printed" {
				pending = append(pending, [2]any{o.ID, i})
			}
		}
	}
	a.mu.RUnlock()

	for _, p := range pending {
		ordID, _ := p[0].(string)
		idx, _ := p[1].(int)
		a.mu.RLock()
		jobID := a.findLine(ordID, idx)
		a.mu.RUnlock()
		if jobID == "" {
			continue
		}
		j, err := a.pollJob(ctx, jobID)
		if err != nil || j == nil {
			continue
		}
		a.mu.Lock()
		a.setLine(ordID, idx, dispatchResult{
			JobID: j.JobID, State: j.State, Attempts: j.Attempts,
			UpdatedAt: time.Now(), Job: j,
		})
		a.mu.Unlock()
	}
}

func (a *app) findLine(ordID string, idx int) string {
	for _, o := range a.ords {
		if o.ID == ordID && idx < len(o.Lines) {
			return o.Lines[idx].JobID
		}
	}
	return ""
}

func (a *app) setLine(ordID string, idx int, res dispatchResult) {
	for _, o := range a.ords {
		if o.ID == ordID && idx < len(o.Lines) {
			o.Lines[idx].Status = res
			o.Lines[idx].JobID = res.JobID
			return
		}
	}
}

func (a *app) preview(ctx context.Context, zpl string, widthMM, heightMM float64) ([]byte, error) {
	if a.cfg.adminToken == "" {
		return nil, errNoAdminToken
	}
	if widthMM <= 0 {
		widthMM = 101.6
	}
	if heightMM <= 0 {
		heightMM = 152.4
	}
	start := time.Now()
	bodyJSON := fmt.Sprintf(`{"zpl":%s,"width_mm":%f,"height_mm":%f,"dpmm":8}`,
		mustJSONString(zpl), widthMM, heightMM)
	path := "/api/v1/admin/preview"
	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.base+path,
		strings.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.cli.http.Do(req)
	latency := time.Since(start).Milliseconds()

	reqHeaders := map[string]string{
		"Authorization": "Bearer " + maskToken(a.cfg.adminToken),
		"Content-Type":  "application/json",
	}

	if err != nil {
		if a.traces != nil {
			a.traces.Record(&APITrace{
				Method: "POST", URL: a.cfg.base + path, Path: path,
				RequestHeaders: reqHeaders, RequestBody: formatJSON([]byte(bodyJSON)),
				Status: 0, StatusText: "Transport Error", ResponseBody: err.Error(),
				LatencyMS: latency, Category: "preview",
				Title: "POST /api/v1/admin/preview",
			})
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))

	resHeaders := map[string]string{}
	for k := range resp.Header {
		resHeaders[k] = resp.Header.Get(k)
	}

	if a.traces != nil {
		resBody := fmt.Sprintf("[Binary Image: %s, %d bytes]", resp.Header.Get("Content-Type"), len(body))
		if resp.StatusCode >= 400 {
			resBody = formatJSON(body)
		}
		a.traces.Record(&APITrace{
			Method: "POST", URL: a.cfg.base + path, Path: path,
			RequestHeaders: reqHeaders, RequestBody: formatJSON([]byte(bodyJSON)),
			Status: resp.StatusCode, StatusText: fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
			ResponseHeaders: resHeaders, ResponseBody: resBody,
			LatencyMS: latency, Category: "preview",
			Title: "POST /api/v1/admin/preview",
		})
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("preview http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

var errNoAdminToken = stderrors.New("preview needs an admin token (-admin-token); dispatch does not")

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func errors_IsEOF(err error) bool { return err == io.EOF || err == context.Canceled }

// ------------------------------------------------------------- demo data

// generateOrders creates multi-line warehouse packages with distinct label sizes:
//   - Line 1: Outbound Shipping & Pallet Label (4"x6") -> routed to 4x6 printer
//   - Line 2: Carton / Case Box Label          (4"x2") -> routed to 4x2 printer
//   - Line 3: Item SKU / Bin Pick Tag          (2"x1") -> routed to 2x1 printer
func generateOrders(seed int64) []*Order {
	rng := rand.New(rand.NewSource(seed))
	customers := []string{"NORTHWIND LOGISTICS", "HALLEIN GMBH", "MERCHANT GLOBAL", "POSTEN NORDIC", "DPD EXPRESS"}
	orders := make([]*Order, 0, 8)

	for i := 0; i < 8; i++ {
		ord := &Order{
			ID:       fmt.Sprintf("OK-%04d", 8800+i),
			Customer: customers[rng.Intn(len(customers))],
			SKU:      fmt.Sprintf("SKU-%05d", 10000+rng.Intn(89999)),
			Qty:      1 + rng.Intn(6),
			Bin:      fmt.Sprintf("A-%02d", 1+rng.Intn(20)),
			Created:  time.Now().Add(-time.Duration(rng.Intn(240)) * time.Minute),
		}

		// Line 1: 4"x6" Outbound Pallet / Shipping Label
		ord.Lines = append(ord.Lines, Line{
			No:        1,
			LabelType: "shipping_4x6",
			LabelName: "Outbound Pallet Label",
			PaperSize: "4x6",
			WidthMM:   101.6,
			HeightMM:  152.4,
			ZPL:       shippingLabel4x6(ord),
		})

		// Line 2: 4"x2" Carton / Case Label
		ord.Lines = append(ord.Lines, Line{
			No:        2,
			LabelType: "carton_4x2",
			LabelName: "Carton Box Label",
			PaperSize: "4x2",
			WidthMM:   101.6,
			HeightMM:  50.8,
			ZPL:       cartonLabel4x2(ord),
		})

		// Line 3: 2"x1" Item SKU Pick Tag
		ord.Lines = append(ord.Lines, Line{
			No:        3,
			LabelType: "item_2x1",
			LabelName: "Item SKU Tag",
			PaperSize: "2x1",
			WidthMM:   50.8,
			HeightMM:  25.4,
			ZPL:       itemLabel2x1(ord),
		})

		orders = append(orders, ord)
	}
	return orders
}

// 4"x6" Outbound Shipping / Pallet Label
func shippingLabel4x6(o *Order) string {
	tracking := fmt.Sprintf("1Z%09d%06d", 680000000+o.Qty, o.ID2())
	return strings.Join([]string{
		"^XA^CI28^PW812^LL1218",
		"^FO40,28^GB732,3,3^FS",
		fmt.Sprintf("^FO40,44^A0N,44,44^FDOUTBOUND LOGISTICS: %s^FS", o.Customer),
		fmt.Sprintf("^FO40,105^A0N,30,30^FDOrder %s (Pallet Tag)   Bin %s^FS", o.ID, o.Bin),
		fmt.Sprintf("^FO40,150^A0N,26,26^FDSKU: %s   Total Qty: %d CTN^FS", o.SKU, o.Qty),
		fmt.Sprintf("^FO40,195^BCN,100,Y,N,N^FD%s^FS", tracking),
		fmt.Sprintf("^FO560,195^BQN,2,6^FD%s^FS", o.SKU),
		"^FO40,330^GB732,3,3^FS",
		fmt.Sprintf("^FO40,360^A0N,30,30^FDDESTINATION: BERLIN LOGISTICS HUB DOCK 4^FS"),
		fmt.Sprintf("^FO40,410^A0N,24,24^FDTRACKING: %s^FS", tracking),
		"^FO40,460^GB200,80,3^FS",
		"^FO55,485^A0N,32,32^FDPALLET^FS",
		"^FO560,460^GB180,80,3^FS",
		"^FO575,485^A0N,32,32^FDCHECKED^FS",
		"^XZ",
	}, "")
}

// 4"x2" Carton / Case Label
func cartonLabel4x2(o *Order) string {
	cartonCode := fmt.Sprintf("CAR-%s-01", o.ID)
	return strings.Join([]string{
		"^XA^CI28^PW812^LL406",
		"^FO30,20^GB752,3,3^FS",
		fmt.Sprintf("^FO30,35^A0N,32,32^FDCARTON BOX #1 - %s^FS", o.Customer),
		fmt.Sprintf("^FO30,78^A0N,22,22^FDOrder: %s | SKU: %s x%d | Loc: %s^FS", o.ID, o.SKU, o.Qty, o.Bin),
		fmt.Sprintf("^FO30,115^BCN,75,Y,N,N^FD%s^FS", cartonCode),
		fmt.Sprintf("^FO600,115^BQN,2,4^FD%s^FS", cartonCode),
		"^FO30,230^GB752,2,2^FS",
		fmt.Sprintf("^FO30,245^A0N,20,20^FDVERIFIED BY PACKING STATION - DO NOT DROP^FS"),
		"^XZ",
	}, "")
}

// 2"x1" Item SKU Pick Tag
func itemLabel2x1(o *Order) string {
	return strings.Join([]string{
		"^XA^CI28^PW406^LL203",
		fmt.Sprintf("^FO20,15^A0N,24,24^FD%s^FS", o.SKU),
		fmt.Sprintf("^FO20,44^A0N,18,18^FDBin: %s | Qty: %d EA^FS", o.Bin, o.Qty),
		fmt.Sprintf("^FO20,72^BCN,48,Y,N,N^FD%s^FS", o.SKU),
		fmt.Sprintf("^FO310,72^BQN,2,3^FD%s^FS", o.SKU),
		"^XZ",
	}, "")
}

func (o *Order) ID2() int {
	n := 0
	for _, c := range strings.TrimPrefix(o.ID, "OK-") {
		n = n*10 + int(c-'0')
	}
	return n
}

func (a *app) simulateLoop(ctx context.Context) {
	if a.cfg.simulate <= 0 {
		return
	}
	interval := time.Minute / time.Duration(a.cfg.simulate)
	t := time.NewTicker(interval)
	defer t.Stop()
	a.log.Info("simulation started", "jobs_per_minute", a.cfg.simulate)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reconcile(ctx)
			ord := &Order{
				ID: fmt.Sprintf("SIM-%d", time.Now().Unix()%100000), Customer: "SIMULATED",
				SKU: fmt.Sprintf("SKU-%05d", rand.Intn(99999)), Qty: 1 + rand.Intn(3),
				Bin: fmt.Sprintf("B-%02d", 1+rand.Intn(20)), Created: time.Now(),
				Lines: []Line{{
					No: 1, LabelType: "shipping_4x6", LabelName: "Simulated Pallet",
					PaperSize: "4x6", WidthMM: 101.6, HeightMM: 152.4,
					ZPL: "^XA^CI28^FO40,40^A0N,40,40^FDSIMULATED 4x6^FS^FO40,110^BCN,90,Y,N,N^FD" + fmt.Sprint(time.Now().Unix()) + "^FS^XZ",
				}},
			}
			a.mu.Lock()
			a.ords = append([]*Order{ord}, a.ords...)
			if len(a.ords) > 60 {
				a.ords = a.ords[:60]
			}
			a.mu.Unlock()
			if _, err := a.Release(ctx, ord.ID); err != nil {
				a.log.Debug("simulated release failed", "err", err)
			}
		}
	}
}
