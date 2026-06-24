package audio

import (
	"os"
	"syscall"
)

// SilenceStderr redirects the process's C-level stderr (fd 2) to /dev/null and
// returns a function that restores it. PortAudio's ALSA/JACK backends write
// device-probe warnings straight to fd 2 (bypassing os.Stderr) during init, so
// this is the only way to hide that spew without losing our own logging.
// Linux-only (the _linux.go suffix gates it); the project's backend is Linux.
func SilenceStderr() (restore func(), err error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		devnull.Close()
		return nil, err
	}
	if err := syscall.Dup2(int(devnull.Fd()), 2); err != nil {
		syscall.Close(saved)
		devnull.Close()
		return nil, err
	}
	return func() {
		syscall.Dup2(saved, 2)
		syscall.Close(saved)
		devnull.Close()
	}, nil
}
