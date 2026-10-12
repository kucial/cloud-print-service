// Package fakeprinter implements a virtual Zebra-style label printer over TCP 9100.
//
// It exists so the real driver path (print socket + status socket, host queries,
// ^XA/^XZ framing) can be exercised end-to-end without hardware, and so tests can
// assert exactly how many labels reached the printer.
//
// It deliberately misbehaves on command: going offline, staying "printing", running
// out of media, stalling status replies, or dying mid-write.
package virtual

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kucial/cloud-print-service/pkg/render"
)

// Printer is one virtual printer bound to an address.
type Printer struct {
	// ID is the human name used in logs, the gallery and rendered filenames.
	ID string

	Model    string
	Serial   string
	Firmware string

	printDuration time.Duration

	mu           sync.Mutex
	device       int // 0 idle, 1 printing, 2 busy, 3 error
	media        int // 0 ok, 1 low, 2 out
	ribbon       int
	head         int
	printing     bool
	offline      bool
	sticky       bool // ^always_printing: never returns to idle
	closeOnPrint bool
	statusDelay  time.Duration
	labels       []Label
	labelCount   int
	bytesIn      int
	conns        map[net.Conn]struct{}

	listener net.Listener
	addr     string
	closed   bool
	wg       sync.WaitGroup

	// rendering
	renderer   render.Renderer
	renderOn   bool
	renderDir  string
	renderSize render.Options
	images     map[int][]byte // label index -> encoded image

	opts Opts
}

// Label is one received label (^XA ... ^XZ), with its rendered preview when
// rendering is enabled.
type Label struct {
	Index      int       `json:"index"`
	ReceivedAt time.Time `json:"received_at"`
	Bytes      int       `json:"bytes"`
	ZPL        string    `json:"zpl"`
	Image      *Preview  `json:"image,omitempty"`
}

// Preview describes the rendered image of a label.
type Preview struct {
	Available  bool     `json:"available"`
	Format     string   `json:"format"`
	Backend    string   `json:"backend"`
	WidthDots  int      `json:"width_dots"`
	HeightDots int      `json:"height_dots"`
	DurationMS int64    `json:"duration_ms"`
	Bytes      int      `json:"bytes"`
	Error      string   `json:"error,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	File       string   `json:"file,omitempty"`
}

// Snapshot is the machine-readable state of the virtual printer.
type Snapshot struct {
	Addr           string  `json:"addr"`
	State          string  `json:"state"`
	Media          string  `json:"media"`
	Offline        bool    `json:"offline"`
	Printing       bool    `json:"printing"`
	CloseOnPrint   bool    `json:"close_on_print"`
	StatusDelayMS  int     `json:"status_delay_ms"`
	Labels         int     `json:"labels"`
	BytesReceived  int     `json:"bytes_received"`
	LabelList      []Label `json:"labels_detail"`
	PrintDuration  string  `json:"print_duration"`
	Renderer       string  `json:"renderer"`
	RenderDir      string  `json:"render_dir,omitempty"`
	RenderEnabled  bool    `json:"render_enabled"`
	DPI            int     `json:"dpi,omitempty"`
	DPMM           int     `json:"dpmm,omitempty"`
	PaperSize      string  `json:"paper_size,omitempty"`
	Orientation    string  `json:"orientation,omitempty"`
	PrintDirection string  `json:"print_direction,omitempty"`
	CJKFont        string  `json:"cjk_font,omitempty"`
}

// Opts configures a virtual printer.
type Opts struct {
	Model          string
	Serial         string
	Firmware       string
	PrintDuration  time.Duration
	Addr           string
	DPI            int
	DPMM           int
	PaperSize      string
	Orientation    string
	PrintDirection string
	CJKFont        string
}

func New(id string, o Opts) *Printer {
	if o.PrintDuration <= 0 {
		o.PrintDuration = 150 * time.Millisecond
	}
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.Model == "" {
		o.Model = "ZT411"
	}
	if o.Serial == "" {
		o.Serial = "VPRT" + id
	}
	if o.Firmware == "" {
		o.Firmware = "V48.0"
	}
	return &Printer{
		ID:       id,
		Model:    o.Model,
		Serial:   o.Serial,
		Firmware: o.Firmware,

		printDuration: o.PrintDuration,
		addr:          o.Addr,
		conns:         map[net.Conn]struct{}{},
		opts:          o,
	}
}

// Start binds and serves until ctx is cancelled or Close is called.
func (p *Printer) Start(ctx context.Context) error {
	if err := p.listen(); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = p.Close()
	}()
	return nil
}

func (p *Printer) listen() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offline || p.listener != nil {
		return nil
	}
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("fake printer listen %s: %w", p.addr, err)
	}
	p.listener = ln
	p.addr = ln.Addr().String()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.serve(c)
			}()
		}
	}()
	return nil
}

// Addr is the bound host:port.
func (p *Printer) Addr() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	host, port, _ := net.SplitHostPort(p.addr)
	return host, port
}

// Close stops the virtual printer.
func (p *Printer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	ln := p.listener
	p.listener = nil
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	p.wg.Wait()
	return nil
}

// Labels returns every label received, in order.
func (p *Printer) Labels() []Label {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Label(nil), p.labels...)
}

// LabelCount is the number of labels received (the "did we double print?" assertion).
func (p *Printer) LabelCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.labelCount
}

// Reset clears recorded labels.
func (p *Printer) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.labels = nil
	p.labelCount = 0
	p.bytesIn = 0
	p.images = map[int][]byte{}
}

// Control applies fault injection / state changes.
func (p *Printer) Control(c Control) {
	// wasOffline is captured before any mutation: coming back online only matters if
	// the printer was offline a moment ago.
	p.mu.Lock()
	wasOffline := p.offline
	switch c.State {
	case "":
	case "idle":
		p.device, p.media, p.ribbon, p.head, p.printing, p.sticky = 0, 0, 0, 0, false, false
	case "printing":
		p.device, p.printing = 1, true
	case "busy":
		p.device = 2
	case "media_low":
		p.media = 1
	case "media_out":
		p.media = 2
	case "ribbon_out":
		p.ribbon = 2
	case "head_error":
		p.head = 1
	case "error":
		p.device = 3
	case "always_printing": // stuck: never confirms, immune to label completions
		p.device, p.printing, p.sticky = 1, true, true
	}
	if c.Media != nil {
		p.media = *c.Media
	}
	if c.StatusDelayMS != nil {
		p.statusDelay = time.Duration(*c.StatusDelayMS) * time.Millisecond
	}
	if c.CloseOnPrint != nil {
		p.closeOnPrint = *c.CloseOnPrint
	}
	if c.Offline != nil {
		offline := *c.Offline
		p.offline = offline
	}
	offline := p.offline
	ln := p.listener
	if offline {
		p.listener = nil
	}
	var conns []net.Conn
	if offline {
		for c := range p.conns {
			conns = append(conns, c)
		}
	}
	closed := p.closed
	p.mu.Unlock()

	if !offline {
		// Coming back online: re-bind the port so a recovered printer looks identical
		// to one that never went away.
		if wasOffline && ln == nil && !closed {
			_ = p.listen()
		}
		return
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// Control is the fault-injection payload.
type Control struct {
	State         string `json:"state"`
	Media         *int   `json:"media"`
	StatusDelayMS *int   `json:"status_delay_ms"`
	CloseOnPrint  *bool  `json:"close_on_print"`
	Offline       *bool  `json:"offline"`
}

func (p *Printer) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Snapshot{
		Addr:           p.addr,
		State:          p.deviceName(),
		Media:          mediaName(p.media),
		Offline:        p.offline,
		Printing:       p.printing,
		CloseOnPrint:   p.closeOnPrint,
		StatusDelayMS:  int(p.statusDelay / time.Millisecond),
		Labels:         p.labelCount,
		BytesReceived:  p.bytesIn,
		LabelList:      append([]Label(nil), p.labels...),
		PrintDuration:  p.printDuration.String(),
		RenderDir:      p.renderDir,
		RenderEnabled:  p.renderOn,
		DPI:            p.opts.DPI,
		DPMM:           p.opts.DPMM,
		PaperSize:      p.opts.PaperSize,
		Orientation:    p.opts.Orientation,
		PrintDirection: p.opts.PrintDirection,
		CJKFont:        p.opts.CJKFont,
	}
	if s.DPMM == 0 && p.renderSize.DPMM > 0 {
		s.DPMM = p.renderSize.DPMM
	}
	if s.DPI == 0 && s.DPMM > 0 {
		s.DPI = int(float64(s.DPMM)*25.4 + 0.5)
	}
	if s.PaperSize == "" && p.renderSize.WidthMM > 0 && p.renderSize.HeightMM > 0 {
		s.PaperSize = fmt.Sprintf("%gx%gmm", p.renderSize.WidthMM, p.renderSize.HeightMM)
	}
	if p.renderOn && p.renderer != nil {
		s.Renderer = p.renderer.Name()
	} else {
		s.Renderer = "none"
	}
	return s
}

func (p *Printer) deviceName() string {
	switch {
	case p.offline:
		return "offline"
	case p.head == 1:
		return "head_error"
	case p.media == 2:
		return "media_out"
	case p.device == 3:
		return "error"
	case p.printing || p.device == 1:
		return "printing"
	case p.device == 2:
		return "busy"
	case p.media == 1:
		return "media_low"
	default:
		return "idle"
	}
}

func mediaName(m int) string {
	switch m {
	case 1:
		return "low"
	case 2:
		return "out"
	default:
		return "ok"
	}
}

// ---------------------------------------------------------------- protocol

func (p *Printer) serve(c net.Conn) {
	p.mu.Lock()
	if p.offline {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c] = struct{}{}
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.conns, c)
		p.mu.Unlock()
		_ = c.Close()
	}()

	isStatus := false
	var buf []byte
	chunk := make([]byte, 8192)

	for {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := c.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			if !isStatus && looksLikeStatusQuery(data) {
				isStatus = true
			}
			if !isStatus {
				p.mu.Lock()
				closeOnPrint := p.closeOnPrint
				p.mu.Unlock()
				if closeOnPrint {
					// Simulate the printer dying mid-write: close with unread data so
					// the client sees a reset and a partial write.
					_ = c.Close()
					return
				}
			}
			buf = append(buf, data...)
			p.mu.Lock()
			p.bytesIn += n
			p.mu.Unlock()

			// Host commands are answered on any connection: the status socket always,
			// and the print socket when it starts with a control command (e.g. ~JA).
			for {
				reply, rest, ok := p.parseStatus(buf, isStatus)
				if !ok {
					break
				}
				buf = rest
				if reply != nil {
					p.writeReply(c, reply)
				}
			}
			if isStatus {
				continue
			}
			for {
				i := indexOf(buf, "^XZ")
				if i < 0 {
					break
				}
				label := string(buf[:i+3])
				buf = buf[i+3:]
				p.recordLabel(label)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Printer) writeReply(c net.Conn, b []byte) {
	p.mu.Lock()
	delay := p.statusDelay
	p.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write(b)
}

// hostStatusReply answers ~HS the way real firmware does: three STX…ETX framed lines
// (ZPL II manual, ~HS). ~HS has no "low" flags, so media/ribbon low read as idle, exactly
// as they would on a real printer. Caller holds p.mu.
func (p *Printer) hostStatusReply() []byte {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	paperOut, paused, bufFull := b(p.media == 2), b(p.device == 3), b(p.device == 2)
	formats := b(p.printing || p.device == 1)
	headUp, ribbonOut := b(p.head == 1), b(p.ribbon == 2)
	return []byte(fmt.Sprintf(
		"\x02030,%d,%d,1245,%03d,%d,0,0,000,0,0,0\x03\r\n"+
			"\x02001,0,%d,%d,0,2,4,0,%08d,1,000\x03\r\n"+
			"\x021234,0\x03\r\n",
		paperOut, paused, formats, bufFull, headUp, ribbonOut, formats))
}

// parseStatus extracts a host command from the buffer. On the print socket only
// buffer-leading control commands are honoured, so label content is never mistaken
// for a command.
func (p *Printer) parseStatus(buf []byte, isStatus bool) (reply, rest []byte, ok bool) {
	trimmed := strings.TrimLeft(string(buf), " \r\n")
	if !isStatus && !strings.HasPrefix(trimmed, "~") {
		return nil, buf, false
	}
	if i := indexOf(buf, "~HS"); i >= 0 {
		rest = append([]byte(nil), buf[i+3:]...)
		p.mu.Lock()
		reply = p.hostStatusReply()
		p.mu.Unlock()
		return reply, rest, true
	}
	// Legacy form spoken by servers before they switched to real firmware's ~HS.
	if i := indexOf(buf, "^HS"); i >= 0 {
		rest = append([]byte(nil), buf[i+3:]...)
		p.mu.Lock()
		reply = []byte(fmt.Sprintf("^HS%d,%d,%d,%d^HS", p.device, p.media, p.ribbon, p.head))
		p.mu.Unlock()
		return reply, rest, true
	}
	if i := indexOf(buf, "~HI"); i >= 0 {
		rest = append([]byte(nil), buf[i+3:]...)
		p.mu.Lock()
		dpmmVal := float64(p.opts.DPMM)
		if dpmmVal == 0 && p.renderSize.DPMM > 0 {
			dpmmVal = float64(p.renderSize.DPMM)
		}
		if dpmmVal == 0 {
			dpmmVal = 8.0
		}
		fontVal := p.opts.CJKFont
		if fontVal == "" {
			fontVal = "STD"
		}
		reply = []byte(fmt.Sprintf(";8=Zebra;9=%s;10=%.1f;11=%s;12=%s;13=%s;",
			p.Model, dpmmVal, fontVal, p.Serial, p.Firmware))
		p.mu.Unlock()
		return reply, rest, true
	}
	if i := indexOf(buf, "~JA"); i >= 0 {
		rest = append([]byte(nil), buf[i+3:]...)
		p.mu.Lock()
		p.printing = false
		p.device = 0
		p.mu.Unlock()
		return nil, rest, true
	}
	return nil, buf, false
}

func parsePQQuantity(zpl string) int {
	upper := strings.ToUpper(zpl)
	idx := strings.Index(upper, "^PQ")
	if idx == -1 {
		idx = strings.Index(upper, "~PQ")
	}
	if idx == -1 {
		return 1
	}
	after := upper[idx+3:]
	var digits []byte
	for i := 0; i < len(after); i++ {
		c := after[i]
		if c >= '0' && c <= '9' {
			digits = append(digits, c)
		} else {
			break
		}
	}
	if len(digits) == 0 {
		return 1
	}
	if q, err := strconv.Atoi(string(digits)); err == nil && q > 0 {
		return q
	}
	return 1
}

func (p *Printer) recordLabel(zpl string) {
	qty := parsePQQuantity(zpl)
	p.mu.Lock()
	p.labelCount += qty
	index := p.labelCount
	p.labels = append(p.labels, Label{
		Index:      index,
		ReceivedAt: time.Now().UTC(),
		Bytes:      len(zpl),
		ZPL:        zpl,
	})
	dur := p.printDuration
	p.device = 1
	p.printing = true
	p.mu.Unlock()

	go p.renderLabel(context.Background(), index, zpl)

	go func() {
		time.Sleep(dur)
		p.mu.Lock()
		// Only go idle if nothing else started printing meanwhile, and we are not
		// simulating a wedged printer.
		if p.printing && !p.sticky {
			p.printing = false
			if p.device == 1 {
				p.device = 0
			}
		}
		p.mu.Unlock()
	}()
}

func looksLikeStatusQuery(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "~HS") || strings.HasPrefix(s, "^HS") || strings.HasPrefix(s, "~HI") || strings.HasPrefix(s, "~JA")
}

func indexOf(b []byte, sub string) int {
	return strings.Index(string(b), sub)
}

// ErrNotRunning is returned when a virtual printer has not been started.
var ErrNotRunning = errors.New("fake printer not started")
