package discovery

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/vprint/virtual"
)

// An office printer that lists PCL is asked over PJL, and nothing it receives can come
// out as a page.
func TestOfficePrinterIsIdentifiedWithoutPrinting(t *testing.T) {
	if !advertisesOfficePDL(map[string]string{"pdl": "application/vnd.hp-PCL,application/postscript"}) ||
		advertisesOfficePDL(map[string]string{"ty": "Zebra ZT411"}) {
		t.Fatal("TXT classification")
	}
	vp := virtual.New("lj", virtual.Opts{Addr: "127.0.0.1:0", Language: "pdl", Model: "HP LaserJet 4250"})
	if err := vp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer vp.Close()
	host, port := vp.Addr()
	n, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f := Found{Detail: map[string]string{}}
	identifyPJL(prCtx{ctx: ctx, host: host, port: n}, &f, nil)
	if f.Model != "HP LaserJet 4250" || f.Detail["driver"] != "pdl9100" || f.Detail["kind"] != "document" {
		t.Fatalf("identified as %+v", f)
	}
	time.Sleep(50 * time.Millisecond)
	if vp.LabelCount() != 0 {
		t.Fatalf("identification printed %d page(s)", vp.LabelCount())
	}
}
