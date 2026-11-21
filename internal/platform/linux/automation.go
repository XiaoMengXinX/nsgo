//go:build linux && !android && nsgoautomation

package linux

import (
	"sync"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/testhost"
)

var (
	liveMu       sync.Mutex
	liveSurfaces = map[uint32]*testSurface{}
)

func init() { testhost.RegisterLive(liveSurface) }

// registerLiveWindow records w as the live surface id for nsgo dev -automation:
// the app's primary window (or hidden own surface) as surface 0, and each
// opened window or panel by its surface ID, leaving tray icon views, embedded
// views and test windows out.
func registerLiveWindow(id uint32, w *window) {
	if w == nil || w.embedded || w.opts.test != nil || w.kind == driver.SurfaceTrayIcon {
		return
	}
	liveMu.Lock()
	liveSurfaces[id] = &testSurface{w: w}
	liveMu.Unlock()
}

// unregisterLiveWindow removes w from the live surfaces map when it closes or
// is destroyed, leaving any newer window with the same ID untouched.
func unregisterLiveWindow(w *window) {
	if w == nil {
		return
	}
	liveMu.Lock()
	if s := liveSurfaces[w.surfaceID]; s != nil && s.w == w {
		delete(liveSurfaces, w.surfaceID)
	}
	liveMu.Unlock()
}

// liveSurface returns the running app's surface id as a test host's surface
// acts on it: the app's window (or hidden own surface) as surface 0, and each
// window or panel the app opened as id; nil for a tray icon's view or a
// surface gone.
func liveSurface(id uint32) testhost.Surface {
	liveMu.Lock()
	s := liveSurfaces[id]
	liveMu.Unlock()
	if s == nil {
		return nil
	}
	return s
}
