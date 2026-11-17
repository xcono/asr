package asr

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// observed is every hallucination seen in live logs, with case and
// punctuation variants Whisper is free to produce.
var observed = []string{
	"Редактор субтитров А.Семкин Корректор А.Егорова",
	"редактор субтитров а. семкин, корректор а. егорова.",
	"Субтитры создавал DimaTorzok",
	"Субтитры создавал DimaTorzok.",
	"СУБТИТРЫ СОЗДАВАЛ DIMATORZOK",
	"Добавил субтитры DimaTorzok",
	"Добавил субтитры DimaTorzok!",
	"Продолжение следует...",
	"Продолжение следует",
	"продолжение следует!",
	"ПРОДОЛЖЕНИЕ  СЛЕДУЕТ…",
	"Продолжение следует. Продолжение следует.",
	"Корректор А.Егорова",
	"Редактор субтитров И.Петрова",
	"Спасибо за просмотр. Субтитры создавал DimaTorzok, всем пока и до новых встреч",
}

// speech is real speech from live logs and phrasing a person may use; none
// of it may be dropped by the default list.
var speech = []string{
	"Да.",
	"Не знаю.",
	"Я не знаю.",
	"Доделай.",
	"Подписывайтесь, ставьте лайки.",
	"Обычная речь, три-четыре фразы подряд, чтобы проверить длинный текст.",
	"Я думаю, продолжение следует, но завтра мы обсудим детали этого проекта.",
	"Позови корректора.",
	"Добавь субтитры к видео.",
}

func TestDefaultPhrasesDropObserved(t *testing.T) {
	f := newPhraseFilter(DefaultHallucinationPhrases())
	for _, text := range observed {
		if hits, _ := f.match(text); hits == nil {
			t.Errorf("match(%q) passed, want dropped", text)
		}
	}
}

func TestDefaultPhrasesKeepSpeech(t *testing.T) {
	f := newPhraseFilter(DefaultHallucinationPhrases())
	for _, text := range speech {
		if hits, covered := f.match(text); hits != nil {
			t.Errorf("match(%q) = %v, %d letters; want passed", text, hits, covered)
		}
	}
}

// TestDefaultPhrasesOmitLikes: the owner said it for real, so it is not on
// the list even though Whisper hallucinates it elsewhere.
func TestDefaultPhrasesOmitLikes(t *testing.T) {
	for _, p := range DefaultHallucinationPhrases() {
		if w := normWords(p); len(w) > 0 && w[0] == "подписывайтесь" {
			t.Fatalf("default list holds %q", p)
		}
	}
}

// TestPhraseMatchRule pins the rule: whole words only, ё = е, markers
// anywhere, other entries only when they cover half the letters.
func TestPhraseMatchRule(t *testing.T) {
	for _, c := range []struct {
		name    string
		entries []string
		text    string
		hits    []string
		covered int
	}{
		{"whole phrase", []string{"Продолжение следует"}, "Продолжение следует...", []string{"Продолжение следует"}, 18},
		{"ё folds to е", []string{"всё ещё"}, "Все еще.", []string{"всё ещё"}, 6},
		{"е matches ё", []string{"все еще"}, "Всё ещё!", []string{"все еще"}, 6},
		{"exactly half", []string{"привет"}, "привет мир!!", []string{"привет"}, 6},
		{"under half", []string{"привет"}, "привет всем вам", nil, 0},
		{"not inside a word", []string{"Корректор"}, "корректора", nil, 0},
		{"not a split phrase", []string{"Продолжение следует"}, "продолжение, наверное, следует", nil, 0},
		{"marker in a long text", []string{"*DimaTorzok"}, "это очень длинный текст, и в нём dimatorzok", []string{"*DimaTorzok"}, 10},
		{"marker beats a phrase", []string{"привет", "*Дима"}, "Дима, привет", []string{"*Дима"}, 10},
		{"phrases add up", []string{"раз", "два"}, "раз два три", []string{"раз", "два"}, 6},
		{"overlap counted once", []string{"раз два", "два три"}, "раз два три четыре пять шесть", nil, 0},
		{"empty entry skipped", []string{"", "*", "..."}, "что угодно", nil, 0},
		{"empty text", []string{"привет"}, "...", nil, 0},
	} {
		hits, covered := newPhraseFilter(c.entries).match(c.text)
		if strings.Join(hits, "|") != strings.Join(c.hits, "|") || covered != c.covered {
			t.Errorf("%s: match(%q) = %q, %d; want %q, %d", c.name, c.text, hits, covered, c.hits, c.covered)
		}
	}
}

// captureLog sends the log package's output to a buffer for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return &buf
}

// TestListenDropsPhrase: within the budget, a listed phrase still ends the
// turn at SpeechEnd, and the log names the entry, coverage and budget.
func TestListenDropsPhrase(t *testing.T) {
	buf := captureLog(t)
	got := listenOnce(fakeRecognizer{text: "Добавил субтитры DimaTorzok"},
		WithHallucinationPhrases(DefaultHallucinationPhrases()))
	if len(got) != 2 || got[1].Kind != SpeechEnd {
		t.Fatalf("got %+v, want start,end", got)
	}
	line := buf.String()
	want := `asr: dropped "Добавил субтитры DimaTorzok": at=`
	if !strings.HasPrefix(line, want) {
		t.Fatalf("log %q, want prefix %q", line, want)
	}
	if want := `, phrase "*DimaTorzok" 25/25 letters, budget +Inf, `; !strings.Contains(line, want) {
		t.Errorf("log %q, want %q", line, want)
	}
}

// TestListenBudgetReasonFirst: when both lines would fire, the budget is the
// reason and the phrase is not mentioned.
func TestListenBudgetReasonFirst(t *testing.T) {
	buf := captureLog(t)
	got := listenOnce(fakeRecognizer{text: "Добавил субтитры DimaTorzok"},
		WithMaxCharsPerSec(25), WithHallucinationPhrases(DefaultHallucinationPhrases()))
	if len(got) != 2 {
		t.Fatalf("got %+v, want start,end", got)
	}
	line := buf.String()
	if !strings.Contains(line, ", 25 letters > budget 12.0, ") || strings.Contains(line, "phrase") {
		t.Errorf("log %q, want the budget reason alone", line)
	}
}

// TestListenPhrasePassesSpeech: real speech goes through with the default
// list on, alongside the budget it is used with.
func TestListenPhrasePassesSpeech(t *testing.T) {
	for _, text := range speech {
		got := listenOnce(fakeRecognizer{text: text}, WithHallucinationPhrases(DefaultHallucinationPhrases()))
		if len(got) != 3 || got[2].Kind != SpeechText || got[2].Text != text {
			t.Errorf("%q: got %+v, want it emitted", text, got)
		}
	}
}

// TestListenPhrasesOff: no option, nil and an empty list all emit a phrase.
func TestListenPhrasesOff(t *testing.T) {
	for name, opts := range map[string][]ListenOption{
		"no option": nil,
		"nil":       {WithHallucinationPhrases(nil)},
		"empty":     {WithHallucinationPhrases([]string{})},
	} {
		got := listenOnce(fakeRecognizer{text: "Продолжение следует..."}, opts...)
		if len(got) != 3 || got[2].Kind != SpeechText {
			t.Errorf("%s: got %+v, want the text emitted", name, got)
		}
	}
}

// TestListenCustomPhrases: a caller's list is used as given — its entries
// drop, the default ones do not.
func TestListenCustomPhrases(t *testing.T) {
	buf := captureLog(t)
	opt := WithHallucinationPhrases([]string{"Спасибо за просмотр"})
	if got := listenOnce(fakeRecognizer{text: "Спасибо за просмотр!"}, opt); len(got) != 2 {
		t.Errorf("custom phrase: got %+v, want start,end", got)
	}
	if !strings.Contains(buf.String(), `phrase "Спасибо за просмотр" 17/17 letters`) {
		t.Errorf("log %q, want the custom entry as reason", buf.String())
	}
	if got := listenOnce(fakeRecognizer{text: "Продолжение следует"}, opt); len(got) != 3 {
		t.Errorf("default phrase under a custom list: got %+v, want it emitted", got)
	}
}

// TestListenDumpsPhraseDrop: a phrase drop saves its clip like a budget drop.
func TestListenDumpsPhraseDrop(t *testing.T) {
	buf := captureLog(t)
	dir := filepath.Join(t.TempDir(), "dropped")
	got, _ := listenDump("Продолжение следует", WithMaxCharsPerSec(0),
		WithHallucinationPhrases(DefaultHallucinationPhrases()), WithDumpDropped(dir))
	if len(got) != 2 {
		t.Fatalf("got %+v, want start,end", got)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("ReadDir = %v, %v; want one file", files, err)
	}
	if want := " wav=" + filepath.Join(dir, files[0].Name()); !strings.Contains(buf.String(), want) {
		t.Errorf("log %q, want %q", buf.String(), want)
	}
}
