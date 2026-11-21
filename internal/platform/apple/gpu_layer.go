//go:build darwin

package apple

import (
	"math"
	"runtime"
	"sync"

	"github.com/ebitengine/purego/objc"

	"github.com/nsgo-dev/nsgo/internal/driver"
)

const (
	mtlCmdErrAccessRevoked = 4
	mtlCmdErrOutOfMemory   = 8
	mtlCmdErrDeviceRemoved = 11
)

// GPULayer hosts a 3D Metal GPU surface for a driver.KindGPU view on macOS and
// iOS. Its CAMetalLayer sits below child views (HUD, controls) in the layer
// hierarchy, while a dedicated render goroutine executes off-commit
// driver.GPUFrame submissions without touching or blocking the main thread.
type GPULayer struct {
	// Layer is the CAMetalLayer installed on the native view.
	Layer objc.ID

	layer    objc.ID // owned by the render goroutine and released on teardown
	dispatch func(driver.GPUEvent)
	wake     chan struct{}
	done     chan struct{}

	mu          sync.Mutex
	closed      bool
	lost        bool
	active      bool
	activateReq bool
	cfgDirty    bool
	resizeDirty bool
	generation  uint32
	cfg         driver.GPUViewConfig
	w, h, scale float64
	pw, ph      int
	queue       []driver.GPUFrame

	// liveCounts tracks active Metal resource counts on the render goroutine
	// for diagnostics and teardown verification in tests.
	ctxMu sync.Mutex
	ctx   *gpuContext
}

// NewGPULayer allocates a CAMetalLayer and starts its dedicated Metal render
// goroutine, or returns nil when no Metal device is available.
func NewGPULayer(scale float64, dispatch func(driver.GPUEvent)) *GPULayer {
	if !loadMetal() {
		return nil
	}
	if scale <= 0 {
		scale = 1
	}
	layer := objc.ID(Class("CAMetalLayer")).Send(selNew)
	layer.Send(sel("setDevice:"), mtlDevice)
	layer.Send(sel("setPixelFormat:"), uint(mtlPixelFormatBGRA8Unorm))
	layer.Send(sel("setFramebufferOnly:"), false)
	layer.Send(sel("setOpaque:"), false)
	layer.Send(sel("setColorspace:"), srgb)
	layer.Send(sel("setContentsScale:"), scale)

	g := &GPULayer{
		Layer:    layer,
		layer:    layer,
		dispatch: dispatch,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		scale:    scale,
		pw:       1,
		ph:       1,
	}
	go g.loop()
	return g
}

// Configure applies ItemGPU's surface configuration on the main thread.
func (g *GPULayer) Configure(cfg driver.GPUViewConfig) {
	if g == nil {
		return
	}
	surfFmt := resolveSurfaceFormat(cfg.ColorFormat)
	if g.Layer != 0 {
		g.Layer.Send(sel("setPixelFormat:"), mtlPixelFormat(surfFmt))
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.cfg = cfg
	if g.active {
		g.cfgDirty = true
		g.signalLocked()
	}
	g.mu.Unlock()
}

// Activate starts the GPU surface at w by h points (at backing scale) once the
// initial layout batch finishes, triggering GPUEventDeviceReady.
func (g *GPULayer) Activate(w, h, scale float64) {
	if g == nil {
		return
	}
	pw, ph := pixelSize(w, h, scale)
	g.updateLayerSize(pw, ph, scale)

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.w, g.h, g.scale = w, h, scale
	if g.active {
		changed := pw != g.pw || ph != g.ph
		g.pw, g.ph = pw, ph
		gen := g.generation
		if changed {
			g.resizeDirty = true
			g.signalLocked()
		}
		g.mu.Unlock()
		if changed {
			g.emit(driver.GPUEvent{
				Kind:       driver.GPUEventResize,
				Generation: gen,
				Width:      pw,
				Height:     ph,
			})
		}
		return
	}
	g.active = true
	g.activateReq = true
	g.lost = false
	g.generation++
	g.pw, g.ph = pw, ph
	g.signalLocked()
	g.mu.Unlock()
}

// Resize updates the CAMetalLayer drawable size on the main thread and emits
// GPUEventResize when the active physical pixel dimensions change.
func (g *GPULayer) Resize(w, h, scale float64) {
	if g == nil {
		return
	}
	pw, ph := pixelSize(w, h, scale)
	g.updateLayerSize(pw, ph, scale)

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.w, g.h, g.scale = w, h, scale
	if !g.active {
		g.pw, g.ph = pw, ph
		g.mu.Unlock()
		return
	}
	if pw == g.pw && ph == g.ph {
		g.mu.Unlock()
		return
	}
	g.pw, g.ph = pw, ph
	g.resizeDirty = true
	gen := g.generation
	g.signalLocked()
	g.mu.Unlock()

	g.emit(driver.GPUEvent{
		Kind:       driver.GPUEventResize,
		Generation: gen,
		Width:      pw,
		Height:     ph,
	})
}

// Submit queues an off-commit GPUFrame from the engine goroutine without
// blocking or hopping to the main thread.
func (g *GPULayer) Submit(frame driver.GPUFrame) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed || g.lost {
		g.mu.Unlock()
		return
	}
	if frame.Generation != 0 && g.generation != 0 && frame.Generation != g.generation {
		g.mu.Unlock()
		return
	}
	g.queue = append(g.queue, frame)
	g.signalLocked()
	g.mu.Unlock()
}

// Release tears down the GPU view, stopping the render goroutine and releasing
// all Metal buffers, textures, samplers, pipelines, depth states, and the
// CAMetalLayer without blocking the main thread.
func (g *GPULayer) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.queue = nil
	close(g.wake)
	g.mu.Unlock()
	g.Layer = 0
}

// waitClosed blocks until the render goroutine has finished releasing all
// Metal resources. Used by tests to verify deterministic teardown.
func (g *GPULayer) waitClosed() {
	if g == nil {
		return
	}
	<-g.done
}

func (g *GPULayer) signalLocked() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *GPULayer) updateLayerSize(pw, ph int, scale float64) {
	if g.Layer == 0 {
		return
	}
	if scale <= 0 {
		scale = 1
	}
	g.Layer.Send(sel("setContentsScale:"), scale)
	g.Layer.Send(sel("setDrawableSize:"), CGSize{Width: float64(pw), Height: float64(ph)})
}

func (g *GPULayer) emit(ev driver.GPUEvent) {
	if g.dispatch != nil {
		g.dispatch(ev)
	}
}

func (g *GPULayer) loop() {
	runtime.LockOSThread()
	var ctx *gpuContext
	defer func() {
		pool := poolPush()
		if ctx != nil {
			ctx.close()
			ctx = nil
		}
		g.ctxMu.Lock()
		g.ctx = nil
		g.ctxMu.Unlock()
		if g.layer != 0 {
			g.layer.Send(selRelease)
			g.layer = 0
		}
		poolPop(pool)
		close(g.done)
	}()

	for range g.wake {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return
		}
		activate := g.activateReq
		g.activateReq = false
		cfgDirty := g.cfgDirty
		g.cfgDirty = false
		resizeDirty := g.resizeDirty
		g.resizeDirty = false
		cfg := g.cfg
		pw, ph := g.pw, g.ph
		gen := g.generation
		frames := g.queue
		g.queue = nil
		g.mu.Unlock()

		pool := poolPush()

		if ctx == nil && activate {
			var err error
			ctx, err = newGPUContext(pw, ph, cfg)
			if err != nil {
				g.emit(driver.GPUEvent{
					Kind:       driver.GPUEventError,
					Generation: gen,
					ErrorCode:  driver.GPUErrorFailed,
					Message:    err.Error(),
				})
				poolPop(pool)
				continue
			}
			g.ctxMu.Lock()
			g.ctx = ctx
			g.ctxMu.Unlock()
			g.emit(driver.GPUEvent{
				Kind:          driver.GPUEventDeviceReady,
				Generation:    gen,
				Width:         pw,
				Height:        ph,
				SurfaceFormat: ctx.surfaceFormat,
				Limits:        metalGPULimits(),
				Features:      metalGPUFeatures(),
			})
		} else if ctx != nil {
			if cfgDirty {
				if err := ctx.reconfigure(cfg); err != nil {
					g.emit(driver.GPUEvent{
						Kind:       driver.GPUEventError,
						Generation: gen,
						ErrorCode:  driver.GPUErrorFailed,
						Message:    err.Error(),
					})
				}
			}
			if resizeDirty {
				if err := ctx.resize(pw, ph); err != nil {
					g.emit(driver.GPUEvent{
						Kind:       driver.GPUEventError,
						Generation: gen,
						ErrorCode:  driver.GPUErrorFailed,
						Message:    err.Error(),
					})
				}
			}
		}

		// When multiple presenting frames queued while catching up, execute
		// every frame's resource ops, copies, and passes in FIFO order, and
		// acquire/present a CAMetalDrawable only for the latest present frame.
		lastPresent := -1
		for i, f := range frames {
			if f.Present {
				lastPresent = i
			}
		}

		for i, f := range frames {
			if ctx == nil {
				break
			}
			if f.Generation != 0 && f.Generation != gen {
				continue
			}
			var presentLayer objc.ID
			if f.Present && i == lastPresent {
				presentLayer = g.layer
			}
			readbacks, code, err := ctx.submit(f, presentLayer)
			if err != nil {
				g.emit(driver.GPUEvent{
					Kind:       driver.GPUEventError,
					Generation: gen,
					ErrorCode:  code,
					Message:    err.Error(),
				})
			}
			if ctx.lastCB != 0 && objc.Send[uint](ctx.lastCB, sel("status")) == mtlCommandBufferStatusError {
				g.handleCommandBufferError(ctx.lastCB, gen)
			}
			for _, rb := range readbacks {
				g.emit(driver.GPUEvent{
					Kind:       driver.GPUEventReadback,
					Generation: gen,
					ReadbackID: rb.id,
					Pixels:     rb.pixels,
					Message:    rb.errMsg,
				})
			}
		}

		poolPop(pool)
	}
}

func (g *GPULayer) handleCommandBufferError(cb objc.ID, gen uint32) {
	errObj := cb.Send(sel("error"))
	msg := errorText(errObj)
	var code int
	if errObj != 0 {
		code = objc.Send[int](errObj, sel("code"))
	}
	switch code {
	case mtlCmdErrDeviceRemoved, mtlCmdErrAccessRevoked:
		reason := driver.GPULostReset
		if code == mtlCmdErrAccessRevoked {
			reason = driver.GPULostDestroyed
		}
		g.mu.Lock()
		g.lost = true
		g.mu.Unlock()
		g.emit(driver.GPUEvent{
			Kind:       driver.GPUEventDeviceLost,
			Generation: gen,
			LostReason: reason,
			Message:    msg,
		})
	case mtlCmdErrOutOfMemory:
		g.emit(driver.GPUEvent{
			Kind:       driver.GPUEventError,
			Generation: gen,
			ErrorCode:  driver.GPUErrorOutOfMemory,
			Message:    msg,
		})
	default:
		g.emit(driver.GPUEvent{
			Kind:       driver.GPUEventError,
			Generation: gen,
			ErrorCode:  driver.GPUErrorFailed,
			Message:    msg,
		})
	}
}

func pixelSize(w, h, scale float64) (int, int) {
	if scale <= 0 {
		scale = 1
	}
	pw := int(math.Ceil(w * scale))
	ph := int(math.Ceil(h * scale))
	if pw <= 0 {
		pw = 1
	}
	if ph <= 0 {
		ph = 1
	}
	return pw, ph
}
