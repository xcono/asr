package vad

import "testing"

// TestEndOfTurnFiresAfterSilence: the timer fires exactly once, after a full
// EndOfTurnMs of continuous silence, and never during speech.
func TestEndOfTurnFiresAfterSilence(t *testing.T) {
	win := DefaultTiming().EndOfTurnWindows()
	e := NewEndOfTurn(DefaultTiming())

	for i := 0; i < 5; i++ {
		if e.Update(0.9) {
			t.Fatal("end-of-turn fired during speech")
		}
	}
	fired := -1
	for i := 0; i < win+5; i++ {
		if e.Update(0.05) {
			fired = i
			break
		}
	}
	if fired != win-1 {
		t.Fatalf("fired on silence window %d, want %d", fired+1, win)
	}
}

// TestEndOfTurnResetsOnSpeech: a speech window mid-silence resets the countdown.
func TestEndOfTurnResetsOnSpeech(t *testing.T) {
	win := DefaultTiming().EndOfTurnWindows()
	e := NewEndOfTurn(DefaultTiming())

	for i := 0; i < win-1; i++ {
		e.Update(0.05) // almost fires
	}
	e.Update(0.9) // speech resets the run
	for i := 0; i < win-1; i++ {
		if e.Update(0.05) {
			t.Fatal("fired too early after a speech reset")
		}
	}
	if !e.Update(0.05) {
		t.Fatal("should fire after a full silence window post-reset")
	}
}
