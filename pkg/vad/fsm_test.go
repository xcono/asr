package vad

import "testing"

// feed runs a probability sequence through a fresh FSM and returns the list of
// transitions it emitted (ignoring None).
func feed(probs []float32) []Transition {
	f := NewFSM(DefaultTiming())
	var out []Transition
	for _, p := range probs {
		if t := f.Update(p); t != None {
			out = append(out, t)
		}
	}
	return out
}

func repeatProb(p float32, n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = p
	}
	return s
}

func names(ts []Transition) []string {
	m := map[Transition]string{None: "None", ToSpeaking: "ToSpeaking", ToSilence: "ToSilence"}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = m[t]
	}
	return out
}

// TestResumeChoppySpeech models the reported BUG: after silence is declared, the
// user keeps speaking but the speech dips below threshold briefly (plosives,
// gaps). Every 3rd window dips to 0.2. The state must eventually flip back to
// speaking; the bug was that it never did.
func TestResumeChoppySpeech(t *testing.T) {
	rw := DefaultTiming().ReleaseWindows()
	var seq []float32
	seq = append(seq, repeatProb(0.9, 30)...)    // clean speech -> ToSpeaking
	seq = append(seq, repeatProb(0.05, rw+3)...) // pause past release -> ToSilence
	for i := 0; i < 15; i++ {
		seq = append(seq, 0.9, 0.9, 0.2)
	}

	got := feed(seq)
	t.Logf("transitions: %v", names(got))

	if len(got) == 0 || got[len(got)-1] != ToSpeaking {
		t.Fatalf("stuck after silence: got %v, want last transition ToSpeaking", names(got))
	}
}

// TestResumeAfterSilence: speak -> pause past the release window (silence) ->
// resume. Expected: ToSpeaking, ToSilence, ToSpeaking. The pause is sized from
// releaseWindow so it stays a turn-ending silence regardless of ReleaseMs.
func TestResumeAfterSilence(t *testing.T) {
	rw := DefaultTiming().ReleaseWindows()
	var seq []float32
	seq = append(seq, repeatProb(0.9, 30)...)    // ~1s of speech
	seq = append(seq, repeatProb(0.05, rw+5)...) // pause past release -> ToSilence
	seq = append(seq, repeatProb(0.9, 30)...)    // resume speech

	got := feed(seq)
	want := []Transition{ToSpeaking, ToSilence, ToSpeaking}

	t.Logf("onsetWindows=%d releaseWindow=%d WindowMs=%d", DefaultTiming().OnsetWindows(), DefaultTiming().ReleaseWindows(), WindowMs)
	t.Logf("transitions: %v", names(got))

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", names(got), names(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", names(got), names(want))
		}
	}
}
