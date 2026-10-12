package driver_test

import (
	"strings"
	"testing"

	"github.com/kucial/cloud-print-service/internal/driver"
)

func TestSplitZPLPages(t *testing.T) {
	zpl := "^XA^FDPage 1^FS^XZ^XA^FDPage 2^FS^XZ"
	pages := driver.SplitZPLPages(zpl)
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if !strings.Contains(pages[0], "Page 1") || !strings.Contains(pages[1], "Page 2") {
		t.Fatalf("unexpected split: %+v", pages)
	}
}

func TestApplyPageCopiesStandardTailBoxScenario(t *testing.T) {
	// Scenario:
	// Page 1: Full Box Carton Label (Qty 12) -> should print 4 times
	// Page 2: Tail Remainder Box Label (Qty 2) -> should print 1 time
	zpl := "^XA^CI28^FO50,50^FDFull Box Qty: 12^FS^XZ^XA^CI28^FO50,50^FDTail Box Qty: 2 [TAIL BOX]^FS^XZ"

	result, total, err := driver.ApplyPageCopies(zpl, []int{4, 1})
	if err != nil {
		t.Fatalf("ApplyPageCopies: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected total labels 5, got %d", total)
	}

	pages := driver.SplitZPLPages(result)
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}

	if !strings.Contains(pages[0], "^PQ4^XZ") {
		t.Fatalf("Page 1 must have ^PQ4 injected before ^XZ, got: %s", pages[0])
	}
	if !strings.Contains(pages[1], "^PQ1^XZ") {
		t.Fatalf("Page 2 must have ^PQ1 injected before ^XZ, got: %s", pages[1])
	}
}

func TestApplyPageCopiesZeroRemainderSuppression(t *testing.T) {
	// Scenario:
	// Order divides evenly (e.g. 48 items in boxes of 12 = 4 full boxes, 0 remainder).
	// Page 1: Full Box (Qty 12) -> 4 copies
	// Page 2: Tail Box (Qty 0)  -> 0 copies (must be suppressed!)
	zpl := "^XA^FDFull Box^FS^XZ^XA^FDTail Box^FS^XZ"

	result, total, err := driver.ApplyPageCopies(zpl, []int{4, 0})
	if err != nil {
		t.Fatalf("ApplyPageCopies: %v", err)
	}
	if total != 4 {
		t.Fatalf("expected total labels 4, got %d", total)
	}

	pages := driver.SplitZPLPages(result)
	if len(pages) != 1 {
		t.Fatalf("Page 2 must be suppressed when copies == 0, got %d pages", len(pages))
	}
	if !strings.Contains(pages[0], "^PQ4^XZ") {
		t.Fatalf("Page 1 should have ^PQ4, got: %s", pages[0])
	}
	if strings.Contains(result, "Tail Box") {
		t.Fatal("Tail Box content must be completely omitted from the output stream")
	}
}

func TestApplyPageCopiesReplacesExistingPQ(t *testing.T) {
	// If a format already has ^PQ1 or ^PQ99, it should be cleanly replaced
	zpl := "^XA^FDLabel 1^FS^PQ1^XZ^XA^FDLabel 2^FS^PQ99,0,1,Y^XZ"

	result, total, err := driver.ApplyPageCopies(zpl, []int{3, 1})
	if err != nil {
		t.Fatalf("ApplyPageCopies: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}

	pages := driver.SplitZPLPages(result)
	if !strings.Contains(pages[0], "^PQ3^XZ") {
		t.Fatalf("expected existing ^PQ1 to be replaced by ^PQ3: %s", pages[0])
	}
	if !strings.Contains(pages[1], "^PQ1^XZ") {
		t.Fatalf("expected existing ^PQ99 to be replaced by ^PQ1: %s", pages[1])
	}
	// Verify no duplicate ^PQ commands
	if strings.Count(pages[0], "^PQ") != 1 || strings.Count(pages[1], "^PQ") != 1 {
		t.Fatalf("duplicate ^PQ found: p0=%s, p1=%s", pages[0], pages[1])
	}
}

func TestExtractPageCopies(t *testing.T) {
	zpl := "^XA^FDItem 1^FS^PQ4^XZ^XA^FDItem 2^FS^XZ^XA^FDTail^FS^PQ2,0,1,Y^XZ"
	counts := driver.ExtractPageCopies(zpl)
	if len(counts) != 3 {
		t.Fatalf("expected 3 counts, got %d", len(counts))
	}
	if counts[0] != 4 || counts[1] != 1 || counts[2] != 2 {
		t.Fatalf("unexpected counts: %+v, want [4, 1, 2]", counts)
	}
}

func TestApplyPageCopiesValidation(t *testing.T) {
	zpl := "^XA^FDSingle Page^FS^XZ"

	// More rules than pages
	if _, _, err := driver.ApplyPageCopies(zpl, []int{1, 2}); err == nil {
		t.Fatal("expected error when page_copies has more elements than pages in ZPL")
	}

	// Negative count
	if _, _, err := driver.ApplyPageCopies(zpl, []int{-1}); err == nil {
		t.Fatal("expected error on negative copy count")
	}
}
