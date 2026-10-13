package httpapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/escpos"
)

// receiptColumns is how many font-A characters fit on a roll: 32 on 58 mm paper, 48 on
// 80 mm (the printable widths are 48 and 72 mm at 12 dots per character).
func receiptColumns(widthMM float64) int {
	if widthMM > 0 && widthMM < 70 {
		return 32
	}
	return 48
}

// testReceiptESCPOS is a receipt that shows at a glance whether the printer, its
// Chinese font, its 2D-code support and its cutter work.
func testReceiptESCPOS(name, printerID string, widthMM float64, now time.Time) ([]byte, int) {
	cols := receiptColumns(widthMM)
	rule := strings.Repeat("-", cols)
	row := func(k, v string) string {
		pad := cols - len(k) - len(v)
		if pad < 1 {
			pad = 1
		}
		return k + strings.Repeat(" ", pad) + v
	}
	b := escpos.New().
		Align(escpos.Center).Bold(true).Size(2, 2).Line("CPS TEST").Size(1, 1).Bold(false).
		Line(name).
		Align(escpos.Left).Line(rule).
		Line(row("printer", printerID)).
		Line(row("paper", fmt.Sprintf("%d columns", cols))).
		Line(row("time", now.Format("2006-01-02 15:04:05"))).
		Line(rule).
		Line("中文测试:打印正常").
		Line(strings.Repeat("0123456789", cols/10+1)[:cols]).
		Line(rule).
		Align(escpos.Center).QR(printerID, 6).Feed(1).
		Code128(shortCode(printerID), 60).Feed(1).
		Line("end of test").Feed(3).Cut()
	return b.Bytes(), cols
}

// shortCode keeps a barcode printable on a narrow roll.
func shortCode(s string) string {
	if len(s) > 16 {
		return s[len(s)-16:]
	}
	return s
}
