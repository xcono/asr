package audio

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gordonklaus/portaudio"
)

// TestIsOverflow guards Read's error policy: only PortAudio's InputOverflowed is
// recoverable (the buffer is still valid); every other error must stay fatal so
// a real device failure isn't silently swallowed.
func TestIsOverflow(t *testing.T) {
	if !isOverflow(portaudio.InputOverflowed) {
		t.Error("InputOverflowed must be tolerated")
	}
	// Wrapped overflow still counts (errors.Is unwraps).
	if !isOverflow(fmt.Errorf("read: %w", portaudio.InputOverflowed)) {
		t.Error("wrapped InputOverflowed must be tolerated")
	}
	if isOverflow(errors.New("device disconnected")) {
		t.Error("a generic error must stay fatal, not be tolerated")
	}
	if isOverflow(nil) {
		t.Error("nil must not be classified as overflow")
	}
}
