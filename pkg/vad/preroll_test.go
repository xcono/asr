package vad

import "testing"

// TestPrerollRetainsRecentSamples checks the lead-in ring keeps only the most
// recent maxSamples and that Snapshot returns an independent copy.
func TestPrerollRetainsRecentSamples(t *testing.T) {
	p := NewPreroll(4) // keep the last 4 samples
	p.Push([]int16{1, 2})
	p.Push([]int16{3, 4})
	p.Push([]int16{5, 6}) // pushed 1..6; last 4 => 3,4,5,6

	got := p.Snapshot()
	want := []int16{3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshot = %v, want %v", got, want)
		}
	}

	got[0] = 99
	if p.Snapshot()[0] == 99 {
		t.Fatal("snapshot aliases the internal buffer; it must return a copy")
	}
}

// TestPrerollExceedsOnsetBudget guards the word-onset clipping fix: the ring must
// retain strictly more than the FSM's onset budget. Silero's probability ramps
// through the sub-threshold region before the first window crosses Threshold, so
// the true word onset precedes confirmation; a ring sized only to the onset
// budget evicts that lead-in and clips soft/short leading words.
func TestPrerollExceedsOnsetBudget(t *testing.T) {
	d := DefaultTiming()
	onsetBudget := d.OnsetWindows() * Window
	if d.PrerollSamples() <= onsetBudget {
		t.Fatalf("PrerollSamples()=%d must exceed onset budget=%d, else word onsets clip",
			d.PrerollSamples(), onsetBudget)
	}
	if want := msToWindows(PrerollMs) * Window; d.PrerollSamples() != want {
		t.Fatalf("PrerollSamples()=%d, want %d (PrerollMs rounded to whole windows)",
			d.PrerollSamples(), want)
	}
}
