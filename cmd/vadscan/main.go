// Command vadscan replays a recorded WAV (a session file from asr.record_dir)
// through the same VAD the live pipeline runs — vad.Model + vad.FSM with the
// timings from -config — and prints the segments it opens next to the loud
// sound it leaves out. It exists to tune the VAD on real sessions offline:
// "the VAD dropped my words" becomes a list of loud bursts outside every
// segment, with Silero's best probability on each, and a change to the model or
// the timings can be checked on the same audio before a live test.
//
// Pure Go, no network: it never calls a recogniser.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"os"

	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/config"
	"github.com/xcono/asr/pkg/vad"
)

// burstGapWindows is how many quiet windows a loud burst may contain and still
// count as one: 96 ms, the gap between syllables of one word.
const burstGapWindows = 3

func main() {
	cfgPath := flag.String("config", "config.json", "path to config.json (vad section)")
	loud := flag.Float64("loud", -35, "window level in dBFS at or above which a window is loud")
	minMs := flag.Int("min-ms", 96, "shortest loud burst to report, in ms")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: vadscan [flags] file.wav [file.wav ...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	t := cfg.VAD.ToTiming()
	minWindows := max(1, (*minMs+vad.WindowMs-1)/vad.WindowMs)

	for i, path := range flag.Args() {
		if i > 0 {
			fmt.Println()
		}
		if err := scan(path, t, *loud, minWindows); err != nil {
			log.Fatalf("%s: %v", path, err)
		}
	}
}

// segment is one FSM-confirmed utterance, in window indices. clip is the first
// window of what asr.Listen would send to the recogniser: the preroll ring
// reaches back from the onset window.
type segment struct {
	clip, onset, release int
	peak                 float32
	speech               int
}

type burst struct {
	from, to int // inclusive window indices
	peakDB   float64
	maxProb  float32
}

func scan(path string, t vad.Timing, loudDB float64, minWindows int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	samples, rate, err := audio.DecodeWAV(b)
	if err != nil {
		return err
	}
	if rate != vad.SampleRate {
		return fmt.Errorf("sample rate %d, want %d (resample first)", rate, vad.SampleRate)
	}
	if ch := wavChannels(b); ch != 1 {
		return fmt.Errorf("%d channels, want mono", ch)
	}

	n := len(samples) / vad.Window
	db := make([]float64, n)
	prob := make([]float32, n)

	m, err := vad.NewModel()
	if err != nil {
		return err
	}
	fsm := vad.NewFSM(t)
	pre := t.PrerollSamples() / vad.Window
	var segs []segment
	open := -1
	for i := range n {
		w := samples[i*vad.Window : (i+1)*vad.Window]
		db[i] = levelDB(w)
		if prob[i], err = m.Infer(w); err != nil {
			return err
		}
		switch fsm.Update(prob[i]) {
		case vad.ToSpeaking:
			open = i
		case vad.ToSilence:
			segs = append(segs, newSegment(prob, max(0, open-pre+1), open, i, t.Threshold))
			open = -1
		}
	}
	if open >= 0 { // still speaking at end of file: close it there
		segs = append(segs, newSegment(prob, max(0, open-pre+1), open, n-1, t.Threshold))
	}

	bursts := loudBursts(db, prob, loudDB, minWindows)
	var missed []burst
	for _, bu := range bursts {
		if !covered(bu, segs) {
			missed = append(missed, bu)
		}
	}

	var sumPeak float64
	for _, s := range segs {
		sumPeak += float64(s.peak)
	}
	fmt.Printf("%s: %.1fs, %d segments, mean_peak=%.2f, resets=%d, loud bursts %d, outside segments %d (loud >= %.0f dBFS, >= %d ms)\n",
		path, sec(n), len(segs), sumPeak/float64(max(1, len(segs))), m.Resets(),
		len(bursts), len(missed), loudDB, minWindows*vad.WindowMs)

	fmt.Println("segments:")
	for _, s := range segs {
		// at= and end= are the confirming windows' ends, the same as vox's
		// "speaking started/stopped at=" lines on the session recording.
		fmt.Printf("  at=%.2fs end=%.2fs clip=%.2fs vad_peak=%.2f speech_win=%d/%d\n",
			sec(s.onset+1), sec(s.release+1), sec(s.release+1-s.clip), s.peak, s.speech, s.release+1-s.clip)
	}
	fmt.Println("loud bursts outside segments:")
	for _, bu := range missed {
		fmt.Printf("  at=%.2fs %.2fs peak=%.1fdBFS max_prob=%.2f\n",
			sec(bu.from), sec(bu.to+1-bu.from), bu.peakDB, bu.maxProb)
	}
	return nil
}

func newSegment(prob []float32, clip, onset, release int, threshold float32) segment {
	s := segment{clip: clip, onset: onset, release: release}
	for _, p := range prob[clip : release+1] {
		s.peak = max(s.peak, p)
		if p >= threshold {
			s.speech++
		}
	}
	return s
}

// loudBursts groups windows at or above loudDB into bursts that tolerate
// burstGapWindows quiet windows inside, keeping those at least minWindows long.
func loudBursts(db []float64, prob []float32, loudDB float64, minWindows int) []burst {
	var out []burst
	for i := 0; i < len(db); {
		if db[i] < loudDB {
			i++
			continue
		}
		from, to := i, i
		for j, gap := i+1, 0; j < len(db) && gap <= burstGapWindows; j++ {
			if db[j] >= loudDB {
				to, gap = j, 0
			} else {
				gap++
			}
		}
		i = to + 1
		if to-from+1 < minWindows {
			continue
		}
		bu := burst{from: from, to: to, peakDB: math.Inf(-1)}
		for j := from; j <= to; j++ {
			bu.peakDB = math.Max(bu.peakDB, db[j])
			bu.maxProb = max(bu.maxProb, prob[j])
		}
		out = append(out, bu)
	}
	return out
}

// covered reports whether the burst overlaps the clip of some segment, preroll
// included: a burst inside the preroll reaches the recogniser.
func covered(bu burst, segs []segment) bool {
	for _, s := range segs {
		if bu.to >= s.clip && bu.from <= s.release {
			return true
		}
	}
	return false
}

// levelDB is the window's RMS level in dBFS (-120 for digital silence).
func levelDB(w []int16) float64 {
	var e float64
	for _, s := range w {
		e += float64(s) * float64(s)
	}
	return math.Max(-120, 10*math.Log10(e/float64(len(w))/(32768*32768)))
}

func sec(windows int) float64 { return float64(windows*vad.Window) / vad.SampleRate }

// wavChannels reads the channel count from the "fmt " chunk; audio.DecodeWAV
// returns only the rate, and a stereo file would decode as interleaved samples
// that the VAD would happily score. 0 if there is no fmt chunk.
func wavChannels(b []byte) int {
	i := bytes.Index(b, []byte("fmt "))
	if i < 0 || i+12 > len(b) {
		return 0
	}
	return int(binary.LittleEndian.Uint16(b[i+10 : i+12]))
}
