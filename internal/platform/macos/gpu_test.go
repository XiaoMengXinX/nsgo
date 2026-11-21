//go:build darwin && !ios

package macos

import (
	"testing"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/platform/apple"
)

// Closing a surface releases its mounted KindGPU views, their CAMetalLayers,
// and their off-commit Metal render goroutines.
func TestClosingASurfaceReleasesItsGPUViews(t *testing.T) {
	if !apple.GPUAvailable() {
		t.Skip("no Metal device")
	}
	onMain(func() {
		loadAppKit()
		s := newSurface(Options{Width: 200, Height: 100}, false)
		defer func() {
			delete(views, s.native)
			s.native.Send(selRelease)
		}()
		env := s.environment()
		if !env.GPU {
			t.Error("expected macOS environment().GPU == true when Metal device is available")
		}
		v := s.create(driver.KindGPU, 1)
		s.views = []*view{nil, v}
		s.native.Send(selAddSubview, v.native)
		gl := v.gpu
		if gl == nil {
			t.Fatal("expected KindGPU view to allocate an apple.GPULayer")
		}
		s.configureGPU(v, driver.GPUViewConfig{
			ColorFormat: driver.GPUFormatBGRA8Unorm,
			DepthFormat: driver.GPUFormatDepth32Float,
			SampleCount: 1,
		})
		v.frame = driver.Bounds{Size: driver.Size{Width: 160, Height: 90}}
		s.finishGPU()

		// Submit an off-commit clear frame directly through surface.SubmitGPU.
		s.SubmitGPU(driver.GPUFrame{
			ViewID: 1,
			Passes: []driver.GPUPass{{
				ColorAttachments: []driver.GPUColorAttachment{{
					LoadOp:     driver.GPULoadClear,
					StoreOp:    driver.GPUStoreStore,
					ClearColor: driver.RGB(0x112233),
				}},
			}},
		})

		s.close()
		if gl.Layer != 0 {
			t.Error("a closed surface's GPU view keeps its CAMetalLayer")
		}
		s.gpu.mu.RLock()
		remaining := len(s.gpu.views)
		s.gpu.mu.RUnlock()
		if remaining != 0 {
			t.Errorf("closed surface retained %d GPU views in SubmitGPU map", remaining)
		}
		if _, ok := views[v.native]; ok {
			t.Error("a closed surface's GPU view stays among the native views")
		}
		if s.views[1] != nil {
			t.Error("a closed surface keeps its GPU view entry")
		}
	})
}
