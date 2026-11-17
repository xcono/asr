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

const (
	speechProb  = float32(0.9)  // above Threshold
	silenceProb = float32(0.05) // below Threshold-Hysteresis; 0.2 would sit in the dead zone
)

// hold feeds n windows of prob and fails if any of them flips the state.
func hold(t *testing.T, f *FSM, prob float32, n int, what string) {
	t.Helper()
	for i := range n {
		if got := f.Update(prob); got != None {
			t.Fatalf("%s: window %d of %d flipped the state (%s), want no transition",
				what, i+1, n, names([]Transition{got})[0])
		}
	}
}

// flip feeds one window of prob and fails unless it yields want.
func flip(t *testing.T, f *FSM, prob float32, want Transition, what string) {
	t.Helper()
	if got := f.Update(prob); got != want {
		t.Fatalf("%s: got %s, want %s", what,
			names([]Transition{got})[0], names([]Transition{want})[0])
	}
}

// TestReleaseNeedsFullSilenceAfterLongSilence models the reported BUG: the mic
// idles (silence evidence saturates), the user starts talking, and the segment
// closed 64–128 ms later instead of after ReleaseMs, chopping phrases into
// fragments. The onset left the saturated silence credit almost intact, so a
// couple of silence windows topped it back up to the release threshold.
func TestReleaseNeedsFullSilenceAfterLongSilence(t *testing.T) {
	tm := DefaultTiming()
	ow, rw := tm.OnsetWindows(), tm.ReleaseWindows()
	f := NewFSM(tm)

	hold(t, f, silenceProb, rw+5, "idle silence")
	hold(t, f, speechProb, ow-1, "onset evidence")
	flip(t, f, speechProb, ToSpeaking, "onset")

	hold(t, f, silenceProb, rw-1, "silence short of the release window")
	flip(t, f, silenceProb, ToSilence, "release")
}

// TestChoppyOnsetConfirms guards what the leak is for on the onset side: a dip
// between speech windows costs one unit of evidence instead of all of it, so
// broken-up speech still confirms. The onset budget is widened here because the
// default (2 windows) leaves no room for a dip.
func TestChoppyOnsetConfirms(t *testing.T) {
	tm := DefaultTiming()
	tm.BargeInMs = 5 * WindowMs
	ow := tm.OnsetWindows()
	f := NewFSM(tm)

	hold(t, f, silenceProb, tm.ReleaseWindows()+5, "idle silence")

	// speech, speech, dip: a net gain of one per three windows.
	confirmed := false
	speech := 0
	for i := 0; i < 3*ow && !confirmed; i++ {
		prob := speechProb
		if i%3 == 2 {
			prob = silenceProb
		} else {
			speech++
		}
		switch f.Update(prob) {
		case ToSpeaking:
			confirmed = true
		case ToSilence:
			t.Fatalf("window %d: ToSilence while not speaking", i+1)
		}
	}
	if !confirmed {
		t.Fatalf("choppy speech never confirmed onset within %d windows", 3*ow)
	}
	if speech <= ow {
		t.Fatalf("onset confirmed after %d speech windows; the dips cost nothing (onset=%d)", speech, ow)
	}
}

// TestShortPauseKeepsSegmentOpen guards what the leak is for on the release
// side: a pause shorter than the release window does not close the segment, and
// the speech that follows bleeds the pause's evidence off one window at a time
// rather than zeroing it — so the next pause closes sooner by exactly what is
// left.
func TestShortPauseKeepsSegmentOpen(t *testing.T) {
	tm := DefaultTiming()
	ow, rw := tm.OnsetWindows(), tm.ReleaseWindows()
	f := NewFSM(tm)

	hold(t, f, speechProb, ow-1, "onset evidence")
	flip(t, f, speechProb, ToSpeaking, "onset")
	hold(t, f, speechProb, 30, "speech")

	pause := rw / 2
	resume := pause / 2
	left := pause - resume // silence evidence surviving the resumed speech
	if pause < 2 || left < 1 {
		t.Fatalf("release window too short for this test: rw=%d", rw)
	}

	hold(t, f, silenceProb, pause, "pause inside the utterance")
	hold(t, f, speechProb, resume, "resumed speech")

	hold(t, f, silenceProb, rw-left-1, "final silence short of release")
	flip(t, f, silenceProb, ToSilence, "release")
}

// TestOnsetNeedsFullEvidenceAfterRelease is the mirror of the release bug: the
// speech credit of the segment that just closed must not confirm the next onset
// early. With the defaults the release window outlasts the onset budget, so the
// credit always bleeds out before the segment closes; the timing here inverts
// that to expose it.
func TestOnsetNeedsFullEvidenceAfterRelease(t *testing.T) {
	tm := DefaultTiming()
	tm.BargeInMs = 10 * WindowMs
	tm.ReleaseMs = 3 * WindowMs
	ow, rw := tm.OnsetWindows(), tm.ReleaseWindows()
	f := NewFSM(tm)

	hold(t, f, speechProb, ow-1, "first onset evidence")
	flip(t, f, speechProb, ToSpeaking, "first onset")
	hold(t, f, speechProb, 30, "speech")
	hold(t, f, silenceProb, rw-1, "silence short of release")
	flip(t, f, silenceProb, ToSilence, "release")

	hold(t, f, speechProb, ow-1, "second onset evidence")
	flip(t, f, speechProb, ToSpeaking, "second onset")
}
