package discovery

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// A browse must leave nothing behind. dnssd's readers stop only on context.Canceled;
// handing it a deadline left two goroutines per browse spinning on a closed socket
// forever — several cores after a few hours of minute-by-minute sweeps.
func TestBrowseLeavesNoGoroutinesBehind(t *testing.T) {
	m := MDNS{Types: []string{"_cps-leak-test._tcp"}, BrowseTimeout: 150 * time.Millisecond}
	settle := func() int {
		time.Sleep(300 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	base := settle()
	for i := 0; i < 5; i++ {
		_, _ = m.Scan(context.Background())
	}
	if n := settle(); n > base+2 {
		t.Fatalf("goroutines grew from %d to %d over 5 browse passes: readers are not stopping", base, n)
	}
}
