package escpr

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// command is one ESC/P-R command read back from a job.
type command struct {
	name   string
	params []byte
}

// parse reads the ESC/P-R part of a job (between "ESCPR" and the closing reset).
func parse(t *testing.T, job []byte) []command {
	t.Helper()
	start := bytes.Index(job, []byte("\x1b(R\x06\x00\x00ESCPR"))
	if start < 0 {
		t.Fatal("job never enters ESC/P-R mode")
	}
	b := job[start+11:]
	var out []command
	for len(b) > 0 && b[0] == 0x1b && len(b) >= 10 && b[1] != '@' {
		n := int(binary.LittleEndian.Uint32(b[2:6]))
		if len(b) < 10+n {
			t.Fatalf("command %q is cut short", b[6:10])
		}
		out = append(out, command{name: string(b[6:10]), params: b[10 : 10+n]})
		b = b[10+n:]
	}
	if !bytes.HasPrefix(b, []byte("\x1b@\x1b(R\x08\x00\x00REMOTE1JE\x01\x00\x00\x1b\x00\x00\x00")) || len(b) != 24 {
		t.Fatalf("job does not end with a reset and remote JE: % x", b)
	}
	return out
}

// unpack reverses packBits.
func unpack(t *testing.T, d []byte) []byte {
	t.Helper()
	var px []byte
	for i := 0; i < len(d); {
		c := d[i]
		i++
		if c >= 128 {
			for range 257 - int(c) {
				px = append(px, d[i:i+3]...)
			}
			i += 3
		} else {
			n := (int(c) + 1) * 3
			px = append(px, d[i:i+n]...)
			i += n
		}
	}
	return px
}

func TestJobLayout(t *testing.T) {
	page := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for x := 10; x < 20; x++ {
		page.Set(x, 5, color.RGBA{255, 0, 0, 255})
	}
	job := Encode([]image.Image{page, page}, Options{Quality: Normal})
	if !bytes.HasPrefix(job, []byte("\x00\x00\x00\x1b\x01@EJL 1284.4\n@EJL     \n\x1b@\x1b(R\x08\x00\x00REMOTE1JS")) {
		t.Fatalf("job does not open with packet-mode exit, reset and remote JS: %q", job[:40])
	}
	var names []string
	for _, c := range parse(t, job) {
		names = append(names, c.name)
	}
	want := []string{"setq", "setj", "sttp", "dsnd", "endp", "sttp", "dsnd", "endp", "endj"}
	if len(names) != len(want) {
		t.Fatalf("commands %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("commands %v, want %v", names, want)
		}
	}
}

func TestPageGeometryAndLastPage(t *testing.T) {
	page := image.NewGray(image.Rect(0, 0, 10, 10))
	cmds := parse(t, Encode([]image.Image{page, page}, Options{Mono: true}))
	q, j := cmds[0].params, cmds[1].params
	if q[2] != 1 {
		t.Errorf("setq colour mode = %d, want 1 (black only)", q[2])
	}
	if got := binary.BigEndian.Uint32(j[0:]); got != 2976 {
		t.Errorf("paper width %d, want 2976 (A4 at 360 dpi)", got)
	}
	if got := binary.BigEndian.Uint32(j[12:]); got != 2976-84 {
		t.Errorf("printable width %d, want %d", got, 2976-84)
	}
	var ends []byte
	for _, c := range cmds {
		if c.name == "endp" {
			ends = append(ends, c.params[0])
		}
	}
	if !bytes.Equal(ends, []byte{1, 0}) {
		t.Errorf("endp flags %v, want [1 0]: more pages, then the last", ends)
	}
}

func TestLinesCarryOnlyInk(t *testing.T) {
	page := image.NewRGBA(image.Rect(0, 0, 300, 40))
	for x := 0; x < 300; x++ {
		for y := 0; y < 40; y++ {
			page.Set(x, y, color.White)
		}
	}
	page.Set(120, 7, color.Black)
	page.Set(130, 7, color.RGBA{0, 0, 255, 255})
	page.Set(5, 30, color.Black)
	var lines []command
	for _, c := range parse(t, Encode([]image.Image{page}, Options{})) {
		if c.name == "dsnd" {
			lines = append(lines, c)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("%d lines sent, want 2: blank lines are skipped", len(lines))
	}
	p := lines[0].params
	x, y, comp := binary.BigEndian.Uint16(p[0:]), binary.BigEndian.Uint16(p[2:]), p[4]
	data := p[7:]
	if int(binary.BigEndian.Uint16(p[5:])) != len(data) {
		t.Fatalf("dsnd size %d, data %d", binary.BigEndian.Uint16(p[5:]), len(data))
	}
	if x != 120 || y != 7 {
		t.Errorf("line at x=%d y=%d, want x=120 y=7", x, y)
	}
	px := data
	if comp == 1 {
		px = unpack(t, data)
	}
	if len(px) != 11*3 || !bytes.Equal(px[:3], []byte{0, 0, 0}) || !bytes.Equal(px[30:], []byte{0, 0, 255}) {
		t.Errorf("line pixels % x, want black … blue across 11 pixels", px)
	}
}

func TestPackBitsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := range 200 {
		n := 1 + rng.Intn(600)
		px := make([]byte, n*3)
		for p := 0; p < n; {
			run := 1 + rng.Intn(1+i%200)
			c := []byte{byte(rng.Intn(3) * 100), byte(rng.Intn(2) * 200), 7}
			for ; run > 0 && p < n; run-- {
				copy(px[p*3:], c)
				p++
			}
		}
		enc, ok := packBits(nil, px)
		if !ok {
			continue // not smaller: sent as is
		}
		if got := unpack(t, enc); !bytes.Equal(got, px) {
			t.Fatalf("round trip %d: got %d bytes, want %d", i, len(got), len(px))
		}
	}
	white := bytes.Repeat([]byte{255}, 3*1000)
	enc, ok := packBits(nil, white)
	if !ok || len(enc) != 4*8 {
		t.Errorf("1000 white pixels encode to %d bytes (ok=%v), want 32: 8 runs of up to 129", len(enc), ok)
	}
}

func TestTransparentIsPaper(t *testing.T) {
	page := image.NewNRGBA(image.Rect(0, 0, 20, 2))
	page.Set(3, 0, color.NRGBA{0, 0, 0, 0}) // fully transparent black
	for _, c := range parse(t, Encode([]image.Image{page}, Options{})) {
		if c.name == "dsnd" {
			t.Fatal("a transparent page printed ink")
		}
	}
}

func TestGreyJobsPrintBlackOnly(t *testing.T) {
	grey := image.NewGray(image.Rect(0, 0, 10, 10))
	tinted := image.NewRGBA(image.Rect(0, 0, 10, 10))
	tinted.Set(3, 3, color.RGBA{200, 0, 0, 255})
	for _, c := range []struct {
		name  string
		pages []image.Image
		o     Options
		want  byte
	}{
		{"grey", []image.Image{grey}, Options{}, 1},
		{"one colour page", []image.Image{grey, tinted}, Options{}, 0},
		{"colour forced mono", []image.Image{tinted}, Options{Mono: true}, 1},
	} {
		if got := parse(t, Encode(c.pages, c.o))[0].params[2]; got != c.want {
			t.Errorf("%s: setq colour mode %d, want %d", c.name, got, c.want)
		}
	}
}
