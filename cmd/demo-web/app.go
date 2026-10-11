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
	No     int            `json:"no"`
	ZPL    string         `json:"-"`
	Status dispatchResult `json:"status"`
	JobID  string         `json:"job_id,omitempty"`
}

// app holds demo state and the reference integration logic.
type app struct {
	log        *slog.Logger
	cfg        appConfig
	cli        *client
	mu         sync.RWMutex
	ords       []*Order
	printerIDs []string
	live       map[string]string // job_id -> external_id, for live updates
}

func newApp(log *slog.Logger, cfg appConfig) (*app, error) {
	a := &app{
		log: log, cfg: cfg,
		cli: &client{
			base: cfg.base, token: cfg.token,
			http:        &http.Client{Timeout: 30 * time.Second},
			maxAttempts: 4,
		},
		live: map[string]string{},
		ords: generateOrders(cfg.seed),
	}
	// Fail fast on a bad token: an integration should verify credentials at startup,
	// not discover them on the first customer order.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.refreshPrinters(ctx); err != nil {
		return nil, fmt.Errorf("cannot read the printer fleet with the supplied token: %w", err)
	}
	if len(a.PrinterIDs()) == 0 {
		log.Warn("no printers visible to this token — the console will idle until some appear")
	}
	return a, nil
}

func (a *app) Run(ctx context.Context) {
	go a.watchJobs(ctx) // SSE, with polling as fallback
	if a.cfg.simulate > 0 {
		go a.simulateLoop(ctx)
	}
}

func (a *app) PrinterIDs() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	ids := a.printerIDs
	return append([]string(nil), ids...)
}

func (a *app) refreshPrinters(ctx context.Context) error {
	var out struct {
		Items []struct {
			PrinterID string `json:"printer_id"`
			Site      string `json:"site"`
			Usable    bool   `json:"usable"`
			Enabled   bool   `json:"enabled"`
		} `json:"items"`
	}
	if err := a.cli.do(ctx, "GET", "/api/v1/printers/status", a.cfg.token, nil, &out); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.printerIDs = a.printerIDs[:0]
	for _, p := range out.Items {
		if a.cfg.printerID != "" {
			if p.PrinterID == a.cfg.printerID {
				a.printerIDs = append(a.printerIDs, p.PrinterID)
			}
			continue
		}
		if a.cfg.site != "" && p.Site != a.cfg.site {
			continue
		}
		if p.Usable && p.Enabled {
			a.printerIDs = append(a.printerIDs, p.PrinterID)
		}
	}
	sort.Strings(a.printerIDs)
	return nil
}

// Release dispatches every line of an order as one wave: concurrent, bounded, and each
// line carrying its own stable external_id.
func (a *app) Release(ctx context.Context, orderID string) ([]dispatchResult, error) {
	a.mu.RLock()
	var ord *Order
	for _, o := range a.ords {
		if o.ID == orderID {
			ord = o
			break
		}
	}
	ids := a.printerIDs
	a.mu.RUnlock()
	if ord == nil {
		return nil, fmt.Errorf("unknown order %s", orderID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no usable printer visible to this token")
	}

	results := make([]dispatchResult, len(ord.Lines))
	sem := make(chan struct{}, 4) // a WMS releases waves, it does not stampede
	var wg sync.WaitGroup
	for i := range ord.Lines {
		line := &ord.Lines[i]
		externalID := fmt.Sprintf("%s-line-%d", ord.ID, line.No)
		printer := ids[i%len(ids)]

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, line *Line, externalID, printer string) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := a.cli.dispatch(ctx, map[string]any{
				"printer_id":  printer,
				"external_id": externalID, // stable business key: this is what makes retries safe
				"biz_ref":     ord.ID,
				"zpl":         line.ZPL,
				"copies":      1,
				"meta":        map[string]string{"module": "demo-web", "order": ord.ID},
			})
			if err != nil {
				a.log.Warn("dispatch rejected", "order", ord.ID, "line", line.No, "err", err)
			}
			line.Status = res
			line.JobID = res.JobID
			results[i] = res
			a.mu.Lock()
			if res.JobID != "" {
				a.live[res.JobID] = externalID
			}
			a.mu.Unlock()
		}(i, line, externalID, printer)
	}
	wg.Wait()
	return results, nil
}

// Resend re-submits an order unchanged. Every line must come back `replayed: true` with no
// new labels: that is the property this console exists to demonstrate.
func (a *app) Resend(ctx context.Context, orderID string) ([]dispatchResult, error) {
	return a.Release(ctx, orderID)
}

// pollJob fetches the authoritative state; the console never infers it.
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

// watchJobs keeps line status current: SSE first, polling as the fallback.
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
	a.log.Info("live event stream connected")

	dec := json.NewDecoder(resp.Body)
	for {
		var frame struct {
			Event string `json:"event"`
			Data  string `json:"data"`
			ID    string `json:"id"`
			Retry int    `json:"retry"`
		}
		// The service emits `event:`/`data:` frames; decode them generically.
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

// reconcile is the polling fallback: it reads the API rather than trusting the stream.
func (a *app) reconcile(ctx context.Context) {
	a.mu.RLock()
	var pending [][2]any // order, line index
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

// preview renders a label through the service (admin token required). Optional: the
// console works without it, and never asks for an admin token for dispatch.
func (a *app) preview(ctx context.Context, zpl string) ([]byte, error) {
	if a.cfg.adminToken == "" {
		return nil, errNoAdminToken
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.base+"/api/v1/admin/preview",
		strings.NewReader(fmt.Sprintf(`{"zpl":%s,"width_mm":101.6,"height_mm":152.4,"dpmm":8}`,
			mustJSONString(zpl))))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.cli.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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

// generateOrders fabricates plausible warehouse work so the console has something to
// release. Real integrations read this from their own order book instead.
func generateOrders(seed int64) []*Order {
	rng := rand.New(rand.NewSource(seed))
	customers := []string{"NORTHWIND", "HALLEIN GMBH", "MERCHANT AB", "POSTEN", "DPD GROUP"}
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
		for n := 1; n <= 1+rng.Intn(2); n++ {
			ord.Lines = append(ord.Lines, Line{
				No:  n,
				ZPL: palletLabel(ord, n),
			})
		}
		orders = append(orders, ord)
	}
	return orders
}

// palletLabel renders a realistic 4x6 outbound label — the kind of ZPL an ERP/WMS
// already produces. The service never interprets this; it just prints it.
func palletLabel(o *Order, lineNo int) string {
	tracking := fmt.Sprintf("1Z%09d%06d", 680000000+o.Qty, o.ID2())
	return strings.Join([]string{
		"^XA^CI28^PW812",
		"^FO40,28^GB732,3,3^FS",
		fmt.Sprintf("^FO40,42^A0N,44,44^FDOUTBOUND %s^FS", o.Customer),
		fmt.Sprintf("^FO40,104^A0N,30,30^FDOrder %s-%d   Bin %s^FS", o.ID, lineNo, o.Bin),
		fmt.Sprintf("^FO40,146^A0N,26,26^FDSKU %s x%d^FS", o.SKU, o.Qty),
		fmt.Sprintf("^FO40,186^BCN,90,Y,N,N^FD%s^FS", tracking),
		fmt.Sprintf("^FO560,186^BQN,2,6^FD%s^FS", o.SKU),
		fmt.Sprintf("^FO40,300^GB300,80,3^FS"),
		fmt.Sprintf("^FO56,330^A0N,30,30^FDHANDLE WITH CARE^FS"),
		fmt.Sprintf("^FO560,300^GB180,80,3^FS"),
		fmt.Sprintf("^FO575,340^A0N,30,30^FDCHECKED^FS"),
		"^XZ",
	}, "")
}

// ID2 gives the order a stable numeric suffix for building tracking numbers.
func (o *Order) ID2() int {
	n := 0
	for _, c := range strings.TrimPrefix(o.ID, "OK-") {
		n = n*10 + int(c-'0')
	}
	return n
}

// simulateLoop generates background traffic so queueing and backpressure can be seen.
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
				Lines: []Line{{No: 1, ZPL: "^XA^CI28^FO40,40^A0N,40,40^FDSIMULATED JOB^FS^FO40,110^BCN,90,Y,N,N^FD" + fmt.Sprint(time.Now().Unix()) + "^FS^XZ"}},
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
