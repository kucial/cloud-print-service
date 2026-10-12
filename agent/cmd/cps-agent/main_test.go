package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingWriter stands in for a paused Windows console: writes wait until released.
type blockingWriter struct {
	release chan struct{}
	mu      sync.Mutex
	got     strings.Builder
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got.Write(p)
}

func TestLoggingNeverWaitsForAPausedConsole(t *testing.T) {
	bw := &blockingWriter{release: make(chan struct{})}
	nb := newNonBlockingWriter(bw, 4)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = nb.Write([]byte("registered with server\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("log writes blocked on a paused console")
	}
	close(bw.release) // console resumes
	time.Sleep(50 * time.Millisecond)
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if !strings.Contains(bw.got.String(), "log lines dropped") {
		t.Fatalf("dropped lines should be reported once the console resumes: %q", bw.got.String())
	}
}
