package audio

import (
	"errors"
	"io"
	"testing"
	"time"
)

// TestFramerWindows checks that packets of any size come out as exact windows,
// in order, with the remainder carried into the next window.
func TestFramerWindows(t *testing.T) {
	f := newFramer(4, 8, time.Second)
	f.push([]int16{1, 2, 3})
	f.push([]int16{4, 5})
	f.push([]int16{6, 7, 8, 9, 10})

	for _, want := range [][]int16{{1, 2, 3, 4}, {5, 6, 7, 8}} {
		got, err := f.next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("window len %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("window %v, want %v", got, want)
			}
		}
	}
	if len(f.pending) != 2 {
		t.Errorf("pending %d samples, want 2", len(f.pending))
	}
}

// TestFramerCopiesPacket guards against aliasing: the library reuses the packet
// buffer after push returns.
func TestFramerCopiesPacket(t *testing.T) {
	f := newFramer(2, 4, time.Second)
	buf := []int16{1, 2}
	f.push(buf)
	buf[0], buf[1] = 9, 9
	got, err := f.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("window %v, want [1 2]", got)
	}
}

// TestFramerDropsWhenFull checks that push never blocks: a full queue drops the
// packet and counts it.
func TestFramerDropsWhenFull(t *testing.T) {
	f := newFramer(1, 2, time.Second)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			f.push([]int16{int16(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("push blocked on a full queue")
	}
	if n := f.dropped.Load(); n != 3 {
		t.Errorf("dropped %d, want 3", n)
	}
}

// TestFramerTimeout checks that a silent source ends Read with an error rather
// than blocking forever, and that the stream's own error is preferred.
func TestFramerTimeout(t *testing.T) {
	f := newFramer(4, 4, 10*time.Millisecond)
	if _, err := f.next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("want a timeout error, got %v", err)
	}
	lost := errors.New("connection closed")
	f.streamErr = func() error { return lost }
	if _, err := f.next(); !errors.Is(err, lost) {
		t.Fatalf("want the stream error, got %v", err)
	}
}

// TestFramerClose checks that close unblocks a waiting read with io.EOF, which
// asr.Listen treats as a clean end.
func TestFramerClose(t *testing.T) {
	f := newFramer(4, 4, time.Minute)
	errc := make(chan error, 1)
	go func() { _, err := f.next(); errc <- err }()
	f.close()
	f.close() // idempotent
	select {
	case err := <-errc:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("want io.EOF, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock next")
	}
}
