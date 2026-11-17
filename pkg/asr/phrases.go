package asr

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MarkerPrefix marks a hallucination phrase entry as a marker: text that
// contains it anywhere is dropped. An entry without it is dropped only when
// the listed phrases cover at least half of the text's letters.
const MarkerPrefix = "*"

// DefaultHallucinationPhrases is the phrase list WithHallucinationPhrases is
// given by stt.New and vox when the config has no list: the subtitle credits
// and sign-off Whisper produced on live sessions, written so their observed
// variants match ("Субтитры создавал DimaTorzok", "Добавил субтитры
// DimaTorzok", "Редактор субтитров А.Семкин Корректор А.Егорова",
// "Продолжение следует..."). Each call returns a fresh slice.
//
// Markers are the entries no one says to a voice agent: the credited name
// and "Редактор субтитров". "Продолжение следует", "Корректор" and the two
// subtitle verbs are ordinary Russian a person may say inside a longer
// sentence, so they drop text only when they make up most of it.
//
// "Подписывайтесь, ставьте лайки" is deliberately absent: it is a Whisper
// hallucination elsewhere, but here it was said for real and transcribed
// correctly.
func DefaultHallucinationPhrases() []string {
	return []string{
		MarkerPrefix + "DimaTorzok",
		MarkerPrefix + "Редактор субтитров",
		"Субтитры создавал",
		"Добавил субтитры",
		"Корректор",
		"Продолжение следует",
	}
}

// phrase is one compiled list entry.
type phrase struct {
	entry    string   // as configured, for the log line
	words    []string // normalised words, matched as a run of whole words
	anywhere bool     // MarkerPrefix entry
}

// phraseFilter is the compiled WithHallucinationPhrases list; nil is off.
type phraseFilter []phrase

// newPhraseFilter compiles entries, skipping those with no letters or digits
// (they could only match everything or nothing).
func newPhraseFilter(entries []string) phraseFilter {
	var f phraseFilter
	for _, e := range entries {
		body, anywhere := strings.CutPrefix(strings.TrimSpace(e), MarkerPrefix)
		words := normWords(body)
		if len(words) == 0 {
			continue
		}
		f = append(f, phrase{entry: e, words: words, anywhere: anywhere})
	}
	return f
}

// match reports why text is a hallucination: the entries that decided it and
// the letters their occurrences cover, or nil hits when text passes. Matching
// is on whole normalised words (see normWords), so "Корректор" does not fire
// inside "корректора" and "DimaTorzok" fires in any punctuation or case.
//
// A marker hit drops the text however long it is. Other entries drop it only
// when all their occurrences together cover at least half its letters: the
// live "Продолжение следует..." is all phrase, while a person saying "я думаю,
// продолжение следует, но завтра обсудим детали" is not. Half, not "all",
// because Whisper pads a credit with a name or a greeting ("Корректор
// А.Егорова" is 9 of 17 letters). Counting letters, not words, keeps a long
// phrase from being outvoted by short words.
func (f phraseFilter) match(text string) (hits []string, covered int) {
	if len(f) == 0 {
		return nil, 0
	}
	words := normWords(text)
	total := 0
	for _, w := range words {
		total += utf8.RuneCountInString(w)
	}
	cover := make([]bool, len(words))
	var markers, phrases []string
	for _, p := range f {
		found := false
		for i := 0; i+len(p.words) <= len(words); i++ {
			if !equalWords(words[i:i+len(p.words)], p.words) {
				continue
			}
			found = true
			for j := i; j < i+len(p.words); j++ {
				cover[j] = true
			}
		}
		switch {
		case found && p.anywhere:
			markers = append(markers, p.entry)
		case found:
			phrases = append(phrases, p.entry)
		}
	}
	for i, c := range cover {
		if c {
			covered += utf8.RuneCountInString(words[i])
		}
	}
	if len(markers) > 0 {
		return markers, covered
	}
	if len(phrases) > 0 && 2*covered >= total {
		return phrases, covered
	}
	return nil, 0
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normWords splits s into lower-case words of letters and digits, with "ё"
// folded to "е": everything else — punctuation, spaces, the dots of
// "А.Семкин" — only separates words. Whisper varies exactly these between
// runs ("Продолжение следует...", "продолжение следует!"), and Russian
// writers drop the dots over "ё" at will.
func normWords(s string) []string {
	return strings.FieldsFunc(strings.Map(func(r rune) rune {
		r = unicode.ToLower(r)
		switch {
		case r == 'ё':
			return 'е'
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return r
		default:
			return ' '
		}
	}, s), func(r rune) bool { return r == ' ' })
}
