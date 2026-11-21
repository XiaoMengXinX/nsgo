//go:build linux && !android && nsgoautomation

package linux

import (
	"testing"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/testhost"
)

func TestLiveSurfaceRegistry(t *testing.T) {
	liveMu.Lock()
	clear(liveSurfaces)
	liveMu.Unlock()
	t.Cleanup(func() {
		liveMu.Lock()
		clear(liveSurfaces)
		liveMu.Unlock()
	})

	primary := &window{primary: true}
	child := &window{surfaceID: 1, kind: driver.SurfaceWindow}
	panel := &window{surfaceID: 2, kind: driver.SurfacePanel}
	tray := &window{surfaceID: 3, kind: driver.SurfaceTrayIcon}
	embedded := &window{surfaceID: 4, embedded: true}
	testWin := &window{surfaceID: 5, opts: Options{test: &testOpen{}}}

	registerLiveWindow(0, primary)
	registerLiveWindow(1, child)
	registerLiveWindow(2, panel)
	registerLiveWindow(3, tray)
	registerLiveWindow(4, embedded)
	registerLiveWindow(5, testWin)

	s0, ok := testhost.Live(0).(*testSurface)
	if !ok || s0 == nil || s0.w != primary || s0.test != nil {
		t.Fatalf("surface 0: %#v, want live *testSurface wrapping primary", testhost.Live(0))
	}
	// Repeated lookups reuse the live testSurface so field/click state persists across steps.
	s0.field = &view{}
	if again := testhost.Live(0).(*testSurface); again != s0 || again.field != s0.field {
		t.Fatalf("repeated lookup returned %p (field %p), want %p", again, again.field, s0)
	}

	s1, ok := testhost.Live(1).(*testSurface)
	if !ok || s1 == nil || s1.w != child {
		t.Fatalf("surface 1: %#v, want live *testSurface wrapping child", testhost.Live(1))
	}
	s2, ok := testhost.Live(2).(*testSurface)
	if !ok || s2 == nil || s2.w != panel {
		t.Fatalf("surface 2: %#v, want live *testSurface wrapping panel", testhost.Live(2))
	}
	for _, id := range []uint32{3, 4, 5, 99} {
		if got := testhost.Live(id); got != nil {
			t.Errorf("surface %d: got %#v, want nil", id, got)
		}
	}

	// WindowSurface on a live surface resolves through testhost.Live without panicking on nil test.
	if got := s0.WindowSurface(1); got != s1 {
		t.Errorf("WindowSurface(1) = %#v, want %#v", got, s1)
	}
	if got := s0.WindowSurface(3); got != nil {
		t.Errorf("WindowSurface(3) = %#v, want nil", got)
	}

	// Live surfaces follow the system's settings and refuse SetEnvironment cleanly.
	if err := s0.SetEnvironment(driver.Environment{}); err == nil {
		t.Error("SetEnvironment on a live surface succeeded, want error")
	}

	// Unregistering removes only the matching window.
	replacement := &window{surfaceID: 1, kind: driver.SurfaceWindow}
	registerLiveWindow(1, replacement)
	unregisterLiveWindow(child)
	if got, ok := testhost.Live(1).(*testSurface); !ok || got.w != replacement {
		t.Fatalf("stale unregister removed replacement: %#v", got)
	}
	unregisterLiveWindow(replacement)
	if got := testhost.Live(1); got != nil {
		t.Errorf("after unregister, surface 1 = %#v, want nil", got)
	}
	unregisterLiveWindow(primary)
	if got := testhost.Live(0); got != nil {
		t.Errorf("after unregister, surface 0 = %#v, want nil", got)
	}
}
