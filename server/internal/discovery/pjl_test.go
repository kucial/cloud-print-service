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

// What the user's Epson L355 advertises on _pdl-datastream: maker and model, no
// command set. It must be taken as ESC/P-R from the TXT alone, never probed; an HP
// that says nothing of its languages must not be sent ~HI either.
func TestTXTIdentifiesInkjetsWithoutProbing(t *testing.T) {
	l355 := map[string]string{"pdl": "raw", "ty": "EPSON L355 Series", "usb_MDL": "L355 Series", "usb_MFG": "EPSON"}
	id := txtDeviceID(l355)
	if id.Manufacturer != "EPSON" || id.Model != "L355 Series" || !advertisesESCPR(id) {
		t.Fatalf("L355 TXT read as %+v (escpr %v)", id, advertisesESCPR(id))
	}
	hp := txtDeviceID(map[string]string{"pdl": "raw", "ty": "HP LaserJet 1020"})
	if hp.Manufacturer != "HP" || hp.Model != "LaserJet 1020" || advertisesESCPR(hp) || hp.MayBeZebra() {
		t.Fatalf("HP TXT read as %+v: must be neither ESC/P-R nor probed", hp)
	}
	if zebra := txtDeviceID(map[string]string{"ty": "Zebra ZD421"}); !zebra.MayBeZebra() {
		t.Fatal("a Zebra must still be asked with ~HI")
	}
	if bare := txtDeviceID(map[string]string{"pdl": "raw"}); !bare.MayBeZebra() || advertisesESCPR(bare) {
		t.Fatal("a printer that says nothing is still asked with ~HI, as before")
	}
}
