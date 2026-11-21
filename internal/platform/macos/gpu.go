//go:build darwin && !ios

package macos

import (
	"sync"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/platform/apple"
)

// gpuState holds a surface's mounted GPU views. SubmitGPU reads the map from
// the engine goroutine off-commit while the main thread registers and releases
// GPU views during mount batches.
type gpuState struct {
	mu    sync.RWMutex
	views map[driver.NodeID]*apple.GPULayer
	ready []*view
}

func (s *surface) SubmitGPU(frame driver.GPUFrame) {
	s.gpu.mu.RLock()
	g := s.gpu.views[frame.ViewID]
	s.gpu.mu.RUnlock()
	if g != nil {
		g.Submit(frame)
	}
}

// newGPU gives a KindGPU view its Metal layer below its subviews (HUD, menus).
func (v *view) newGPU() {
	gl := apple.NewGPULayer(float64(v.surface.env.PixelRatio), v.dispatchGPU)
	if gl == nil {
		return
	}
	gl.Layer.Send(selActions, contentActions)
	gl.Layer.Send(selSetZPosition, apple.Z(contentZ))
	v.native.Send(selLayer).Send(sel("addSublayer:"), gl.Layer)
	v.gpu = gl

	s := v.surface
	s.gpu.mu.Lock()
	if s.gpu.views == nil {
		s.gpu.views = map[driver.NodeID]*apple.GPULayer{}
	}
	s.gpu.views[v.id] = gl
	s.gpu.mu.Unlock()
}

func (v *view) dispatchGPU(ev driver.GPUEvent) {
	e := ev
	if eng := v.surface.engine; eng != nil {
		eng.Dispatch(driver.Event{
			Kind: driver.EventGPU,
			ID:   v.id,
			Data: &e,
		})
	}
}

func (s *surface) configureGPU(v *view, cfg driver.GPUViewConfig) {
	if v.gpu == nil {
		v.dispatchGPU(driver.GPUEvent{
			Kind:      driver.GPUEventError,
			ErrorCode: driver.GPUErrorUnsupported,
			Message:   "Metal GPU device is unavailable",
		})
		return
	}
	v.gpu.Configure(cfg)
	s.gpu.ready = append(s.gpu.ready, v)
}

// finishGPU activates configured GPU views once the batch's ItemFrame items
// have laid out their initial bounds.
func (s *surface) finishGPU() {
	for _, v := range s.gpu.ready {
		if s.views[v.id] != v || v.gpu == nil {
			continue
		}
		w, h := float64(v.frame.Size.Width), float64(v.frame.Size.Height)
		apple.WithoutAnimation(func() {
			v.gpu.Layer.Send(selSetFrame, apple.Rect(0, 0, w, h))
		})
		v.gpu.Activate(w, h, float64(s.env.PixelRatio))
	}
	clear(s.gpu.ready)
	s.gpu.ready = s.gpu.ready[:0]
}

// sizeGPU keeps v's Metal layer over its bounds at the window's backing scale.
func (v *view) sizeGPU() {
	w, h := float64(v.frame.Size.Width), float64(v.frame.Size.Height)
	apple.WithoutAnimation(func() {
		v.gpu.Layer.Send(selSetFrame, apple.Rect(0, 0, w, h))
	})
	v.gpu.Resize(w, h, float64(v.surface.env.PixelRatio))
}

func (s *surface) deleteGPU(v *view) {
	s.gpu.mu.Lock()
	delete(s.gpu.views, v.id)
	s.gpu.mu.Unlock()
	v.gpu.Release()
	v.gpu = nil
}
