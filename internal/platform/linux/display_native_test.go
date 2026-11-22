//go:build linux && !android

package linux

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
)

func TestMissingDisplayRepeatedStartup(t *testing.T) {
	if first := os.Getenv("NSGO_TEST_MISSING_DISPLAY"); first != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		t.Setenv("GDK_BACKEND", "x11")
		app := func(*core.Scope) core.Node { return core.View{} }
		window := func() error {
			_, err := start(app, Options{})
			return err
		}
		embedded := func() error {
			_, err := NewView(app, Options{})
			return err
		}
		attempts := []func() error{window, embedded, window, embedded}
		if first == "embedded" {
			attempts = []func() error{embedded, window, embedded, window}
		}
		for i, attempt := range attempts {
			if err := attempt(); err == nil || !strings.Contains(err.Error(), "no display") {
				t.Fatalf("attempt %d: error = %v, want no display", i+1, err)
			}
		}
		return
	}
	if err := openLibs(); err != nil {
		t.Skip("needs GTK 4: ", err)
	}
	// GTK initialization cannot be undone. Each order needs a fresh process,
	// leaving the display of the package's other native tests untouched.
	for _, first := range []string{"window", "embedded"} {
		t.Run(first, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMissingDisplayRepeatedStartup$", "-test.v", "-test.timeout=20s")
			cmd.Env = append(os.Environ(), "NSGO_TEST_MISSING_DISPLAY="+first)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
