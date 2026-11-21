//go:build darwin

package apple

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"structs"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/shaderir"
	"github.com/nsgo-dev/nsgo/internal/shaderir/msl"
)

// Metal enum values verified against the macOS/iOS Metal SDK headers.
const (
	mtlPixelFormatR8Unorm              = 10
	mtlPixelFormatRG8Unorm             = 30
	mtlPixelFormatRGBA8UnormSRGB       = 71
	mtlPixelFormatRGBA16Float          = 115
	mtlPixelFormatDepth32Float         = 252
	mtlPixelFormatDepth32FloatStencil8 = 260

	mtlVertexFormatUChar4Normalized = 9
	mtlVertexFormatFloat            = 28
	mtlVertexFormatFloat2           = 29
	mtlVertexFormatFloat3           = 30
	mtlVertexFormatFloat4           = 31
	mtlVertexFormatInt              = 32
	mtlVertexFormatInt2             = 33
	mtlVertexFormatInt3             = 34
	mtlVertexFormatInt4             = 35
	mtlVertexFormatUInt             = 36
	mtlVertexFormatUInt2            = 37
	mtlVertexFormatUInt3            = 38
	mtlVertexFormatUInt4            = 39

	mtlVertexStepPerVertex   = 1
	mtlVertexStepPerInstance = 2

	mtlCompareNever        = 0
	mtlCompareLess         = 1
	mtlCompareEqual        = 2
	mtlCompareLessEqual    = 3
	mtlCompareGreater      = 4
	mtlCompareNotEqual     = 5
	mtlCompareGreaterEqual = 6
	mtlCompareAlways       = 7

	mtlPrimitiveTypePoint         = 0
	mtlPrimitiveTypeLine          = 1
	mtlPrimitiveTypeLineStrip     = 2
	mtlPrimitiveTypeTriangleStrip = 4

	mtlIndexTypeUInt16 = 0
	mtlIndexTypeUInt32 = 1

	mtlWindingClockwise        = 0
	mtlWindingCounterClockwise = 1

	mtlCullModeNone  = 0
	mtlCullModeFront = 1
	mtlCullModeBack  = 2

	mtlBlendFactorZero                     = 0
	mtlBlendFactorOne                      = 1
	mtlBlendFactorSourceColor              = 2
	mtlBlendFactorOneMinusSourceColor      = 3
	mtlBlendFactorSourceAlpha              = 4
	mtlBlendFactorOneMinusSourceAlpha      = 5
	mtlBlendFactorDestinationColor         = 6
	mtlBlendFactorOneMinusDestinationColor = 7
	mtlBlendFactorDestinationAlpha         = 8
	mtlBlendFactorOneMinusDestinationAlpha = 9
	mtlBlendFactorBlendColor               = 11
	mtlBlendFactorOneMinusBlendColor       = 12

	mtlBlendOpAdd             = 0
	mtlBlendOpSubtract        = 1
	mtlBlendOpReverseSubtract = 2
	mtlBlendOpMin             = 3
	mtlBlendOpMax             = 4

	mtlSamplerMinMagNearest = 0
	mtlSamplerMinMagLinear  = 1

	mtlSamplerMipNearest = 1
	mtlSamplerMipLinear  = 2

	mtlSamplerAddressClampToEdge  = 0
	mtlSamplerAddressRepeat       = 2
	mtlSamplerAddressMirrorRepeat = 3

	mtlTextureType2D            = 2
	mtlTextureType2DArray       = 3
	mtlTextureType2DMultisample = 4
	mtlTextureTypeCube          = 5

	mtlTextureUsageShaderRead   = 1
	mtlTextureUsageRenderTarget = 4

	mtlStorageModeShared  = 0
	mtlStorageModeManaged = 1
	mtlStorageModePrivate = 2

	mtlLoadActionDontCare = 0
	mtlLoadActionLoad     = 1

	mtlStoreActionDontCare               = 0
	mtlStoreActionMultisampleResolve     = 2
	mtlStoreActionStoreAndMultisampleRes = 3

	mtlCommandBufferStatusCompleted = 4
	mtlCommandBufferStatusError     = 5
)

type mtlViewport struct {
	_                       structs.HostLayout
	X, Y, W, H, ZNear, ZFar float64
}

type mtlScissorRect struct {
	_          structs.HostLayout
	X, Y, W, H uint
}

type mtlOrigin struct {
	_       structs.HostLayout
	X, Y, Z uint
}

type mtlSize struct {
	_       structs.HostLayout
	W, H, D uint
}

// GPUAvailable reports whether this Apple device has a Metal GPU capable of
// hosting a gpu.View.
func GPUAvailable() bool {
	return loadMetal()
}

func metalGPULimits() driver.GPULimits {
	lim := driver.DefaultGPULimits()
	lim.MaxTextureDimension2D = 8192
	return lim
}

func metalGPUFeatures() driver.GPUFeatures {
	return driver.GPUFeatures{
		StorageBuffers: true,
		Compute:        false,
		MSAA4x:         true,
		Depth32Float:   true,
		Anisotropy:     true,
	}
}

type mtlBuffer struct {
	id    objc.ID
	size  uint64
	usage driver.GPUBufferUsage
}

type mtlTexture struct {
	id     objc.ID
	desc   driver.GPUTextureDesc
	mtlFmt uint
}

type mtlSampler struct {
	id objc.ID
}

type mtlBindGroupLayout struct {
	desc            driver.GPUBindGroupLayoutDesc
	dynamicBindings []uint32 // sorted binding indices with HasDynamicOffset
}

type mtlBindGroup struct {
	layoutID driver.GPUID
	entries  map[uint32]driver.GPUBindGroupEntry
}

// pipeStateKey uniquely identifies attachment formats and MSAA sample count
// for a compiled MTLRenderPipelineState variant.
type pipeStateKey struct {
	colorCount  uint8
	colorFmts   [4]uint16
	depthFmt    uint16
	sampleCount uint8
}

type mtlPipeline struct {
	desc       driver.GPUPipelineDesc
	vsFn       objc.ID
	fsFn       objc.ID
	vsSlots    []msl.BindingSlot
	fsSlots    []msl.BindingSlot
	depthState objc.ID
	states     map[pipeStateKey]objc.ID
	hasDepth   bool
}

type boundGroupState struct {
	id         driver.GPUID
	dynOffsets []uint32
	valid      bool
}

type gpuReadback struct {
	id     uint64
	pixels []byte
	errMsg string
}

// gpuContext owns all Metal resources and executes driver.GPUFrame batches on
// the Metal render thread (or directly in offscreen tests). All methods are
// called serially from the owning render goroutine.
type gpuContext struct {
	cfg                driver.GPUViewConfig
	width, height      int
	surfaceFormat      driver.GPUTextureFormat
	surfaceMTLFmt      uint
	surfaceDepthMTLFmt uint
	sampleCount        int

	surfaceTex      objc.ID
	surfaceMSAATex  objc.ID
	surfaceDepthTex objc.ID
	noDepthState    objc.ID
	lastCB          objc.ID

	buffers    map[driver.GPUID]*mtlBuffer
	textures   map[driver.GPUID]*mtlTexture
	samplers   map[driver.GPUID]*mtlSampler
	layouts    map[driver.GPUID]*mtlBindGroupLayout
	bindGroups map[driver.GPUID]*mtlBindGroup
	pipelines  map[driver.GPUID]*mtlPipeline
}

func newGPUContext(width, height int, cfg driver.GPUViewConfig) (*gpuContext, error) {
	if !loadMetal() {
		return nil, fmt.Errorf("apple: Metal device unavailable")
	}
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	surfFmt := resolveSurfaceFormat(cfg.ColorFormat)
	samples := cfg.SampleCount
	if samples != 4 {
		samples = 1
	}
	ctx := &gpuContext{
		cfg:                cfg,
		width:              width,
		height:             height,
		surfaceFormat:      surfFmt,
		surfaceMTLFmt:      mtlPixelFormat(surfFmt),
		surfaceDepthMTLFmt: mtlPixelFormat(cfg.DepthFormat),
		sampleCount:        samples,
		buffers:            map[driver.GPUID]*mtlBuffer{},
		textures:           map[driver.GPUID]*mtlTexture{},
		samplers:           map[driver.GPUID]*mtlSampler{},
		layouts:            map[driver.GPUID]*mtlBindGroupLayout{},
		bindGroups:         map[driver.GPUID]*mtlBindGroup{},
		pipelines:          map[driver.GPUID]*mtlPipeline{},
	}
	ctx.noDepthState = newDepthStencilState(mtlCompareAlways, false)
	if err := ctx.allocateSurfaceTargets(); err != nil {
		ctx.close()
		return nil, err
	}
	return ctx, nil
}

func resolveSurfaceFormat(req driver.GPUTextureFormat) driver.GPUTextureFormat {
	if req == driver.GPUFormatRGBA16Float {
		return driver.GPUFormatRGBA16Float
	}
	return driver.GPUFormatBGRA8Unorm
}

func (c *gpuContext) allocateSurfaceTargets() error {
	c.releaseSurfaceTargets()

	colorDesc := objc.ID(Class("MTLTextureDescriptor")).Send(selNew)
	defer colorDesc.Send(selRelease)
	colorDesc.Send(sel("setTextureType:"), uint(mtlTextureType2D))
	colorDesc.Send(sel("setPixelFormat:"), c.surfaceMTLFmt)
	colorDesc.Send(sel("setWidth:"), uint(c.width))
	colorDesc.Send(sel("setHeight:"), uint(c.height))
	colorDesc.Send(sel("setDepth:"), uint(1))
	colorDesc.Send(sel("setMipmapLevelCount:"), uint(1))
	colorDesc.Send(sel("setSampleCount:"), uint(1))
	colorDesc.Send(sel("setArrayLength:"), uint(1))
	colorDesc.Send(sel("setUsage:"), uint(mtlTextureUsageRenderTarget|mtlTextureUsageShaderRead))
	colorDesc.Send(sel("setStorageMode:"), uint(mtlStorageModePrivate))
	c.surfaceTex = mtlDevice.Send(sel("newTextureWithDescriptor:"), colorDesc)
	if c.surfaceTex == 0 {
		return fmt.Errorf("apple: failed to allocate %dx%d Metal surface color texture", c.width, c.height)
	}

	if c.sampleCount > 1 {
		msDesc := objc.ID(Class("MTLTextureDescriptor")).Send(selNew)
		defer msDesc.Send(selRelease)
		msDesc.Send(sel("setTextureType:"), uint(mtlTextureType2DMultisample))
		msDesc.Send(sel("setPixelFormat:"), c.surfaceMTLFmt)
		msDesc.Send(sel("setWidth:"), uint(c.width))
		msDesc.Send(sel("setHeight:"), uint(c.height))
		msDesc.Send(sel("setDepth:"), uint(1))
		msDesc.Send(sel("setMipmapLevelCount:"), uint(1))
		msDesc.Send(sel("setSampleCount:"), uint(c.sampleCount))
		msDesc.Send(sel("setArrayLength:"), uint(1))
		msDesc.Send(sel("setUsage:"), uint(mtlTextureUsageRenderTarget))
		msDesc.Send(sel("setStorageMode:"), uint(mtlStorageModePrivate))
		c.surfaceMSAATex = mtlDevice.Send(sel("newTextureWithDescriptor:"), msDesc)
		if c.surfaceMSAATex == 0 {
			return fmt.Errorf("apple: failed to allocate %dx%d MSAA surface texture", c.width, c.height)
		}
	}

	if c.surfaceDepthMTLFmt != 0 {
		depthDesc := objc.ID(Class("MTLTextureDescriptor")).Send(selNew)
		defer depthDesc.Send(selRelease)
		if c.sampleCount > 1 {
			depthDesc.Send(sel("setTextureType:"), uint(mtlTextureType2DMultisample))
		} else {
			depthDesc.Send(sel("setTextureType:"), uint(mtlTextureType2D))
		}
		depthDesc.Send(sel("setPixelFormat:"), c.surfaceDepthMTLFmt)
		depthDesc.Send(sel("setWidth:"), uint(c.width))
		depthDesc.Send(sel("setHeight:"), uint(c.height))
		depthDesc.Send(sel("setDepth:"), uint(1))
		depthDesc.Send(sel("setMipmapLevelCount:"), uint(1))
		depthDesc.Send(sel("setSampleCount:"), uint(c.sampleCount))
		depthDesc.Send(sel("setArrayLength:"), uint(1))
		depthDesc.Send(sel("setUsage:"), uint(mtlTextureUsageRenderTarget))
		depthDesc.Send(sel("setStorageMode:"), uint(mtlStorageModePrivate))
		c.surfaceDepthTex = mtlDevice.Send(sel("newTextureWithDescriptor:"), depthDesc)
		if c.surfaceDepthTex == 0 {
			return fmt.Errorf("apple: failed to allocate %dx%d Metal depth texture", c.width, c.height)
		}
	}
	return nil
}

func (c *gpuContext) releaseSurfaceTargets() {
	if c.surfaceTex != 0 {
		c.surfaceTex.Send(selRelease)
		c.surfaceTex = 0
	}
	if c.surfaceMSAATex != 0 {
		c.surfaceMSAATex.Send(selRelease)
		c.surfaceMSAATex = 0
	}
	if c.surfaceDepthTex != 0 {
		c.surfaceDepthTex.Send(selRelease)
		c.surfaceDepthTex = 0
	}
}

func (c *gpuContext) resize(width, height int) error {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	if width == c.width && height == c.height {
		return nil
	}
	c.waitLastCB()
	c.width, c.height = width, height
	return c.allocateSurfaceTargets()
}

func (c *gpuContext) reconfigure(cfg driver.GPUViewConfig) error {
	c.waitLastCB()
	c.cfg = cfg
	c.surfaceFormat = resolveSurfaceFormat(cfg.ColorFormat)
	c.surfaceMTLFmt = mtlPixelFormat(c.surfaceFormat)
	c.surfaceDepthMTLFmt = mtlPixelFormat(cfg.DepthFormat)
	if cfg.SampleCount == 4 {
		c.sampleCount = 4
	} else {
		c.sampleCount = 1
	}
	return c.allocateSurfaceTargets()
}

func (c *gpuContext) waitLastCB() {
	if c.lastCB != 0 {
		c.lastCB.Send(sel("waitUntilCompleted"))
		c.lastCB.Send(selRelease)
		c.lastCB = 0
	}
}

func (c *gpuContext) setLastCB(cb objc.ID) {
	if cb != 0 {
		cb.Send(sel("retain"))
	}
	if c.lastCB != 0 {
		c.lastCB.Send(selRelease)
	}
	c.lastCB = cb
}

func (c *gpuContext) close() {
	c.waitLastCB()
	for id, b := range c.buffers {
		if b.id != 0 {
			b.id.Send(selRelease)
		}
		delete(c.buffers, id)
	}
	for id, t := range c.textures {
		if t.id != 0 {
			t.id.Send(selRelease)
		}
		delete(c.textures, id)
	}
	for id, s := range c.samplers {
		if s.id != 0 {
			s.id.Send(selRelease)
		}
		delete(c.samplers, id)
	}
	for id, p := range c.pipelines {
		p.release()
		delete(c.pipelines, id)
	}
	clear(c.layouts)
	clear(c.bindGroups)
	c.releaseSurfaceTargets()
	if c.noDepthState != 0 {
		c.noDepthState.Send(selRelease)
		c.noDepthState = 0
	}
}

func (c *gpuContext) resourceCounts() (buffers, textures, samplers, pipelines int) {
	return len(c.buffers), len(c.textures), len(c.samplers), len(c.pipelines)
}

func (p *mtlPipeline) release() {
	for k, st := range p.states {
		if st != 0 {
			st.Send(selRelease)
		}
		delete(p.states, k)
	}
	if p.depthState != 0 {
		p.depthState.Send(selRelease)
		p.depthState = 0
	}
	if p.vsFn != 0 {
		p.vsFn.Send(selRelease)
		p.vsFn = 0
	}
	if p.fsFn != 0 {
		p.fsFn.Send(selRelease)
		p.fsFn = 0
	}
}

// submit executes one GPUFrame through Metal. If presentLayer != 0 and
// frame.Present is true, it acquires a CAMetalDrawable, blits the surface
// color target into it, and presents it on the same command buffer.
func (c *gpuContext) submit(frame driver.GPUFrame, presentLayer objc.ID) ([]gpuReadback, driver.GPUErrorCode, error) {
	var readbacks []gpuReadback
	waited := false
	ensureIdle := func() {
		if !waited {
			c.waitLastCB()
			waited = true
		}
	}

	// Execute retained resource ops in submission order.
	for _, op := range frame.Ops {
		switch op.Kind {
		case driver.GPUOpCreateBuffer:
			if err := c.createBuffer(op.ID, op.Buffer); err != nil {
				return readbacks, driver.GPUErrorOutOfMemory, err
			}
		case driver.GPUOpWriteBuffer:
			ensureIdle()
			if err := c.writeBuffer(op.ID, op.Offset, op.Data); err != nil {
				return readbacks, driver.GPUErrorFailed, err
			}
		case driver.GPUOpDestroyBuffer:
			ensureIdle()
			if b, ok := c.buffers[op.ID]; ok {
				if b.id != 0 {
					b.id.Send(selRelease)
				}
				delete(c.buffers, op.ID)
			}
		case driver.GPUOpCreateTexture:
			if err := c.createTexture(op.ID, op.Texture); err != nil {
				return readbacks, driver.GPUErrorOutOfMemory, err
			}
		case driver.GPUOpWriteTexture:
			ensureIdle()
			if err := c.writeTexture(op.TextureDst, op.Data, op.DataLayout); err != nil {
				return readbacks, driver.GPUErrorFailed, err
			}
		case driver.GPUOpDestroyTexture:
			ensureIdle()
			if t, ok := c.textures[op.ID]; ok {
				if t.id != 0 {
					t.id.Send(selRelease)
				}
				delete(c.textures, op.ID)
			}
		case driver.GPUOpCreateSampler:
			if err := c.createSampler(op.ID, op.Sampler); err != nil {
				return readbacks, driver.GPUErrorFailed, err
			}
		case driver.GPUOpDestroySampler:
			ensureIdle()
			if s, ok := c.samplers[op.ID]; ok {
				if s.id != 0 {
					s.id.Send(selRelease)
				}
				delete(c.samplers, op.ID)
			}
		case driver.GPUOpCreateBindGroupLayout:
			if op.BindGroupLayout != nil {
				var dyn []uint32
				for _, e := range op.BindGroupLayout.Entries {
					if e.HasDynamicOffset {
						dyn = append(dyn, e.Binding)
					}
				}
				slices.Sort(dyn)
				c.layouts[op.ID] = &mtlBindGroupLayout{
					desc:            *op.BindGroupLayout,
					dynamicBindings: dyn,
				}
			}
		case driver.GPUOpDestroyBindGroupLayout:
			delete(c.layouts, op.ID)
		case driver.GPUOpCreateBindGroup:
			if op.BindGroup != nil {
				m := make(map[uint32]driver.GPUBindGroupEntry, len(op.BindGroup.Entries))
				for _, e := range op.BindGroup.Entries {
					m[e.Binding] = e
				}
				c.bindGroups[op.ID] = &mtlBindGroup{
					layoutID: op.BindGroup.Layout,
					entries:  m,
				}
			}
		case driver.GPUOpDestroyBindGroup:
			delete(c.bindGroups, op.ID)
		case driver.GPUOpCreatePipeline:
			code, err := c.createPipeline(op.ID, op.Pipeline)
			if err != nil {
				return readbacks, code, err
			}
		case driver.GPUOpDestroyPipeline:
			ensureIdle()
			if p, ok := c.pipelines[op.ID]; ok {
				p.release()
				delete(c.pipelines, op.ID)
			}
		case driver.GPUOpReadTexture:
			rb := c.readTexture(op)
			readbacks = append(readbacks, rb)
			waited = true
		}
	}

	needCB := len(frame.Copies) > 0 || len(frame.Passes) > 0 || (frame.Present && presentLayer != 0)
	if !needCB {
		return readbacks, driver.GPUErrorNone, nil
	}

	cb := mtlQueue.Send(sel("commandBuffer"))
	if cb == 0 {
		return readbacks, driver.GPUErrorFailed, fmt.Errorf("apple: Metal commandBuffer returned nil")
	}

	if len(frame.Copies) > 0 {
		if err := c.encodeCopies(cb, frame.Copies); err != nil {
			return readbacks, driver.GPUErrorFailed, err
		}
	}

	for _, pass := range frame.Passes {
		if err := c.encodePass(cb, pass); err != nil {
			return readbacks, driver.GPUErrorFailed, err
		}
	}

	if frame.Present && presentLayer != 0 && c.surfaceTex != 0 {
		drawable := presentLayer.Send(selNextDrawable)
		if drawable != 0 {
			dstTex := drawable.Send(sel("texture"))
			if dstTex != 0 {
				dw := objc.Send[uint](dstTex, sel("width"))
				dh := objc.Send[uint](dstTex, sel("height"))
				cw := min(uint(c.width), dw)
				ch := min(uint(c.height), dh)
				if cw > 0 && ch > 0 {
					blit := cb.Send(sel("blitCommandEncoder"))
					blit.Send(
						sel("copyFromTexture:sourceSlice:sourceLevel:sourceOrigin:sourceSize:toTexture:destinationSlice:destinationLevel:destinationOrigin:"),
						c.surfaceTex, uint(0), uint(0),
						mtlOrigin{}, mtlSize{W: cw, H: ch, D: 1},
						dstTex, uint(0), uint(0), mtlOrigin{},
					)
					blit.Send(sel("endEncoding"))
				}
			}
			cb.Send(sel("presentDrawable:"), drawable)
		}
	}

	cb.Send(sel("commit"))
	c.setLastCB(cb)
	return readbacks, driver.GPUErrorNone, nil
}

func (c *gpuContext) createBuffer(id driver.GPUID, desc *driver.GPUBufferDesc) error {
	if desc == nil {
		return fmt.Errorf("apple: nil buffer descriptor")
	}
	size := desc.Size
	if size == 0 {
		size = uint64(len(desc.Data))
	}
	if size == 0 {
		return fmt.Errorf("apple: zero-size Metal buffer")
	}
	var buf objc.ID
	if uint64(len(desc.Data)) == size {
		buf = mtlDevice.Send(sel("newBufferWithBytes:length:options:"), unsafe.Pointer(&desc.Data[0]), uint(size), uint(mtlStorageModeShared))
	} else {
		buf = mtlDevice.Send(sel("newBufferWithLength:options:"), uint(size), uint(mtlStorageModeShared))
		if buf != 0 && len(desc.Data) > 0 {
			ptr := objc.Send[unsafe.Pointer](buf, sel("contents"))
			if ptr != nil {
				copy(unsafe.Slice((*byte)(ptr), size), desc.Data)
			}
		}
	}
	if buf == 0 {
		return fmt.Errorf("apple: failed to allocate %d-byte Metal buffer", size)
	}
	if old, ok := c.buffers[id]; ok && old.id != 0 {
		old.id.Send(selRelease)
	}
	c.buffers[id] = &mtlBuffer{id: buf, size: size, usage: desc.Usage}
	return nil
}

func (c *gpuContext) writeBuffer(id driver.GPUID, offset uint64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	b, ok := c.buffers[id]
	if !ok || b.id == 0 {
		return fmt.Errorf("apple: write to unknown GPU buffer %d", id)
	}
	if offset+uint64(len(data)) > b.size {
		return fmt.Errorf("apple: buffer %d write out of bounds (%d+%d > %d)", id, offset, len(data), b.size)
	}
	ptr := objc.Send[unsafe.Pointer](b.id, sel("contents"))
	if ptr == nil {
		return fmt.Errorf("apple: Metal buffer %d has nil contents", id)
	}
	dst := unsafe.Slice((*byte)(unsafe.Add(ptr, offset)), len(data))
	copy(dst, data)
	return nil
}

func (c *gpuContext) createTexture(id driver.GPUID, desc *driver.GPUTextureDesc) error {
	if desc == nil {
		return fmt.Errorf("apple: nil texture descriptor")
	}
	fmtVal := mtlPixelFormat(desc.Format)
	if fmtVal == 0 {
		return fmt.Errorf("apple: unsupported Metal texture format %d", desc.Format)
	}
	d := objc.ID(Class("MTLTextureDescriptor")).Send(selNew)
	defer d.Send(selRelease)

	samples := max(desc.SampleCount, 1)
	mipLevels := max(desc.MipLevels, 1)
	layers := max(desc.Layers, 1)

	var texType uint = mtlTextureType2D
	var arrayLen uint = 1
	switch {
	case samples > 1:
		texType = mtlTextureType2DMultisample
	case desc.Dimension == driver.GPUDimCube:
		texType = mtlTextureTypeCube
	case desc.Dimension == driver.GPUDim2DArray:
		texType = mtlTextureType2DArray
		arrayLen = uint(layers)
	}

	var usage uint
	if desc.Usage&driver.GPUTextureSampled != 0 {
		usage |= mtlTextureUsageShaderRead
	}
	if desc.Usage&driver.GPUTextureRenderAttachment != 0 {
		usage |= mtlTextureUsageRenderTarget
	}
	if usage == 0 {
		usage = mtlTextureUsageShaderRead
	}

	d.Send(sel("setTextureType:"), texType)
	d.Send(sel("setPixelFormat:"), fmtVal)
	d.Send(sel("setWidth:"), uint(desc.Width))
	d.Send(sel("setHeight:"), uint(desc.Height))
	d.Send(sel("setDepth:"), uint(1))
	d.Send(sel("setMipmapLevelCount:"), uint(mipLevels))
	d.Send(sel("setSampleCount:"), uint(samples))
	d.Send(sel("setArrayLength:"), arrayLen)
	d.Send(sel("setUsage:"), usage)
	if desc.Format.IsDepth() || samples > 1 {
		d.Send(sel("setStorageMode:"), uint(mtlStorageModePrivate))
	}

	tex := mtlDevice.Send(sel("newTextureWithDescriptor:"), d)
	if tex == 0 {
		return fmt.Errorf("apple: failed to allocate %dx%d Metal texture", desc.Width, desc.Height)
	}
	if old, ok := c.textures[id]; ok && old.id != 0 {
		old.id.Send(selRelease)
	}
	c.textures[id] = &mtlTexture{id: tex, desc: *desc, mtlFmt: fmtVal}
	return nil
}

func (c *gpuContext) writeTexture(dst driver.GPUTextureCopy, data []byte, layout driver.GPUDataLayout) error {
	if len(data) == 0 {
		return nil
	}
	tex, ok := c.textures[dst.Texture]
	if !ok || tex.id == 0 {
		return fmt.Errorf("apple: write to unknown GPU texture %d", dst.Texture)
	}
	if tex.desc.Format.IsDepth() || tex.desc.SampleCount > 1 {
		return nil
	}
	if layout.Offset >= uint64(len(data)) {
		return nil
	}
	src := data[layout.Offset:]
	bpp := tex.desc.Format.BytesPerPixel()
	if bpp == 0 {
		bpp = 4
	}
	mw := max(tex.desc.Width>>dst.MipLevel, 1)
	mh := max(tex.desc.Height>>dst.MipLevel, 1)
	if dst.OriginX >= mw || dst.OriginY >= mh {
		return nil
	}
	maxW := mw - dst.OriginX
	maxH := mh - dst.OriginY

	bpr := layout.BytesPerRow
	if bpr == 0 {
		bpr = maxW * bpp
	}
	copyW := maxW
	if bpr/bpp < copyW && bpr >= bpp {
		copyW = bpr / bpp
	}
	copyH := maxH
	if layout.RowsPerImage > 0 && layout.RowsPerImage < copyH {
		copyH = layout.RowsPerImage
	}
	if availRows := uint32(len(src)) / bpr; availRows < copyH {
		copyH = availRows
	}
	if copyW == 0 || copyH == 0 {
		return nil
	}
	region := mtlRegion{
		X: uint(dst.OriginX),
		Y: uint(dst.OriginY),
		Z: 0,
		W: uint(copyW),
		H: uint(copyH),
		D: 1,
	}
	tex.id.Send(
		sel("replaceRegion:mipmapLevel:slice:withBytes:bytesPerRow:bytesPerImage:"),
		region,
		uint(dst.MipLevel),
		uint(dst.ArrayLayer),
		unsafe.Pointer(&src[0]),
		uint(bpr),
		uint(bpr)*uint(copyH),
	)
	return nil
}

func (c *gpuContext) createSampler(id driver.GPUID, desc *driver.GPUSamplerDesc) error {
	if desc == nil {
		return fmt.Errorf("apple: nil sampler descriptor")
	}
	d := objc.ID(Class("MTLSamplerDescriptor")).Send(selNew)
	defer d.Send(selRelease)
	d.Send(sel("setMinFilter:"), mtlMinMagFilter(desc.MinFilter))
	d.Send(sel("setMagFilter:"), mtlMinMagFilter(desc.MagFilter))
	d.Send(sel("setMipFilter:"), mtlMipFilter(desc.MipmapFilter))
	d.Send(sel("setSAddressMode:"), mtlAddressMode(desc.AddressU))
	d.Send(sel("setTAddressMode:"), mtlAddressMode(desc.AddressV))
	d.Send(sel("setRAddressMode:"), mtlAddressMode(desc.AddressW))
	d.Send(sel("setNormalizedCoordinates:"), true)
	if desc.MaxAnisotropy > 1 {
		aniso := min(desc.MaxAnisotropy, 16)
		d.Send(sel("setMaxAnisotropy:"), uint(aniso))
	}
	if desc.Compare != driver.GPUCompareUndefined {
		d.Send(sel("setCompareFunction:"), mtlCompareFunc(desc.Compare))
	}
	samp := mtlDevice.Send(sel("newSamplerStateWithDescriptor:"), d)
	if samp == 0 {
		return fmt.Errorf("apple: failed to create Metal sampler state")
	}
	if old, ok := c.samplers[id]; ok && old.id != 0 {
		old.id.Send(selRelease)
	}
	c.samplers[id] = &mtlSampler{id: samp}
	return nil
}

func compileStageMSL(ir *shaderir.Program) (msl.PipelineSource, objc.ID, error) {
	compiled, err := msl.CompilePipeline(ir)
	if err != nil {
		return msl.PipelineSource{}, 0, err
	}
	var errObj objc.ID
	lib := mtlDevice.Send(sel("newLibraryWithSource:options:error:"), NSString(compiled.Source), objc.ID(0), unsafe.Pointer(&errObj))
	if lib == 0 {
		return msl.PipelineSource{}, 0, fmt.Errorf("Metal shader compile failed: %s", errorText(errObj))
	}
	return compiled, lib, nil
}

func (c *gpuContext) createPipeline(id driver.GPUID, desc *driver.GPUPipelineDesc) (driver.GPUErrorCode, error) {
	if desc == nil || desc.Vertex.IR == nil {
		return driver.GPUErrorInvalidDescriptor, fmt.Errorf("apple: pipeline missing vertex shader IR")
	}

	var (
		vsFn, fsFn       objc.ID
		vsSlots, fsSlots []msl.BindingSlot
	)

	if desc.Vertex.IR == desc.Fragment.IR || desc.Fragment.IR == nil {
		compiled, lib, err := compileStageMSL(desc.Vertex.IR)
		if err != nil {
			return driver.GPUErrorShaderCompile, err
		}
		defer lib.Send(selRelease)

		vsFn = lib.Send(sel("newFunctionWithName:"), NSString(compiled.VertexEntry))
		if vsFn == 0 {
			return driver.GPUErrorShaderCompile, fmt.Errorf("apple: vertex entry %q not found in compiled MSL", compiled.VertexEntry)
		}
		vsSlots = compiled.Slots
		if desc.Fragment.IR != nil {
			fsFn = lib.Send(sel("newFunctionWithName:"), NSString(compiled.FragmentEntry))
			if fsFn == 0 {
				vsFn.Send(selRelease)
				return driver.GPUErrorShaderCompile, fmt.Errorf("apple: fragment entry %q not found in compiled MSL", compiled.FragmentEntry)
			}
			fsSlots = compiled.Slots
		}
	} else {
		vsCompiled, vsLib, err := compileStageMSL(desc.Vertex.IR)
		if err != nil {
			return driver.GPUErrorShaderCompile, err
		}
		vsFn = vsLib.Send(sel("newFunctionWithName:"), NSString(vsCompiled.VertexEntry))
		vsLib.Send(selRelease)
		if vsFn == 0 {
			return driver.GPUErrorShaderCompile, fmt.Errorf("apple: vertex entry %q not found in compiled MSL", vsCompiled.VertexEntry)
		}
		vsSlots = vsCompiled.Slots

		fsCompiled, fsLib, err := compileStageMSL(desc.Fragment.IR)
		if err != nil {
			vsFn.Send(selRelease)
			return driver.GPUErrorShaderCompile, err
		}
		fsFn = fsLib.Send(sel("newFunctionWithName:"), NSString(fsCompiled.FragmentEntry))
		fsLib.Send(selRelease)
		if fsFn == 0 {
			vsFn.Send(selRelease)
			return driver.GPUErrorShaderCompile, fmt.Errorf("apple: fragment entry %q not found in compiled MSL", fsCompiled.FragmentEntry)
		}
		fsSlots = fsCompiled.Slots
	}

	var depthState objc.ID
	if desc.DepthStencil != nil {
		cmp := mtlCompareFunc(desc.DepthStencil.DepthCompare)
		depthState = newDepthStencilState(cmp, desc.DepthStencil.DepthWrite)
	}

	p := &mtlPipeline{
		desc:       *desc,
		vsFn:       vsFn,
		fsFn:       fsFn,
		vsSlots:    vsSlots,
		fsSlots:    fsSlots,
		depthState: depthState,
		states:     map[pipeStateKey]objc.ID{},
		hasDepth:   desc.DepthStencil != nil,
	}

	key := c.defaultPipelineKey(desc)
	if _, err := c.buildPipelineState(p, key); err != nil {
		p.release()
		return driver.GPUErrorShaderCompile, err
	}
	if old, ok := c.pipelines[id]; ok {
		old.release()
	}
	c.pipelines[id] = p
	return driver.GPUErrorNone, nil
}

func (c *gpuContext) defaultPipelineKey(desc *driver.GPUPipelineDesc) pipeStateKey {
	var key pipeStateKey
	n := min(len(desc.Targets), 4)
	key.colorCount = uint8(n)
	for i := 0; i < n; i++ {
		f := desc.Targets[i].Format
		if f == driver.GPUFormatUndefined {
			f = c.surfaceFormat
		}
		key.colorFmts[i] = uint16(mtlPixelFormat(f))
	}
	if desc.DepthStencil != nil {
		df := desc.DepthStencil.Format
		if df == driver.GPUFormatUndefined {
			df = c.cfg.DepthFormat
		}
		key.depthFmt = uint16(mtlPixelFormat(df))
	}
	key.sampleCount = uint8(max(desc.SampleCount, 1))
	return key
}

func (c *gpuContext) buildPipelineState(p *mtlPipeline, key pipeStateKey) (objc.ID, error) {
	if st, ok := p.states[key]; ok {
		return st, nil
	}
	rpd := objc.ID(Class("MTLRenderPipelineDescriptor")).Send(selNew)
	defer rpd.Send(selRelease)

	rpd.Send(sel("setVertexFunction:"), p.vsFn)
	if p.fsFn != 0 {
		rpd.Send(sel("setFragmentFunction:"), p.fsFn)
	}
	rpd.Send(sel("setRasterSampleCount:"), uint(max(key.sampleCount, 1)))

	if len(p.desc.VertexBuffers) > 0 {
		vd := objc.ID(Class("MTLVertexDescriptor")).Send(sel("vertexDescriptor"))
		attrs := vd.Send(sel("attributes"))
		layouts := vd.Send(sel("layouts"))
		for slot, vb := range p.desc.VertexBuffers {
			lay := layouts.Send(selObjectAtIndexedSubscript, uint(slot))
			lay.Send(sel("setStride:"), uint(vb.Stride))
			if vb.StepMode == driver.GPUStepInstance {
				lay.Send(sel("setStepFunction:"), uint(mtlVertexStepPerInstance))
			} else {
				lay.Send(sel("setStepFunction:"), uint(mtlVertexStepPerVertex))
			}
			lay.Send(sel("setStepRate:"), uint(1))
			for _, a := range vb.Attributes {
				attr := attrs.Send(selObjectAtIndexedSubscript, uint(a.Location))
				attr.Send(sel("setFormat:"), mtlVertexFormat(a.Format))
				attr.Send(sel("setOffset:"), uint(a.Offset))
				attr.Send(sel("setBufferIndex:"), uint(slot))
			}
		}
		rpd.Send(sel("setVertexDescriptor:"), vd)
	}

	cas := rpd.Send(selColorAttachments)
	for i := 0; i < int(key.colorCount); i++ {
		ca := cas.Send(selObjectAtIndexedSubscript, uint(i))
		ca.Send(sel("setPixelFormat:"), uint(key.colorFmts[i]))
		if i < len(p.desc.Targets) {
			t := p.desc.Targets[i]
			ca.Send(sel("setWriteMask:"), mtlColorWriteMask(t.WriteMask))
			if t.Blend != nil {
				ca.Send(sel("setBlendingEnabled:"), true)
				ca.Send(sel("setSourceRGBBlendFactor:"), mtlBlendFactor(t.Blend.Color.SrcFactor))
				ca.Send(sel("setDestinationRGBBlendFactor:"), mtlBlendFactor(t.Blend.Color.DstFactor))
				ca.Send(sel("setRgbBlendOperation:"), mtlBlendOp(t.Blend.Color.Operation))
				ca.Send(sel("setSourceAlphaBlendFactor:"), mtlBlendFactor(t.Blend.Alpha.SrcFactor))
				ca.Send(sel("setDestinationAlphaBlendFactor:"), mtlBlendFactor(t.Blend.Alpha.DstFactor))
				ca.Send(sel("setAlphaBlendOperation:"), mtlBlendOp(t.Blend.Alpha.Operation))
			}
		}
	}

	if key.depthFmt != 0 {
		rpd.Send(sel("setDepthAttachmentPixelFormat:"), uint(key.depthFmt))
		if key.depthFmt == mtlPixelFormatDepth32FloatStencil8 {
			rpd.Send(sel("setStencilAttachmentPixelFormat:"), uint(key.depthFmt))
		}
	}

	var errObj objc.ID
	state := mtlDevice.Send(sel("newRenderPipelineStateWithDescriptor:error:"), rpd, unsafe.Pointer(&errObj))
	if state == 0 {
		return 0, fmt.Errorf("Metal render pipeline failed: %s", errorText(errObj))
	}
	p.states[key] = state
	return state, nil
}

func (c *gpuContext) encodeCopies(cb objc.ID, copies []driver.GPUCopyOp) error {
	blit := cb.Send(sel("blitCommandEncoder"))
	if blit == 0 {
		return fmt.Errorf("apple: Metal blitCommandEncoder returned nil")
	}
	defer blit.Send(sel("endEncoding"))

	for _, cp := range copies {
		switch cp.Kind {
		case driver.GPUCopyBufferToBuffer:
			src, ok1 := c.buffers[cp.SrcID]
			dst, ok2 := c.buffers[cp.DstID]
			if !ok1 || !ok2 {
				return fmt.Errorf("apple: CopyBufferToBuffer unknown buffer (%d -> %d)", cp.SrcID, cp.DstID)
			}
			blit.Send(sel("copyFromBuffer:sourceOffset:toBuffer:destinationOffset:size:"),
				src.id, uint(cp.SrcOffset), dst.id, uint(cp.DstOffset), uint(cp.Size))

		case driver.GPUCopyBufferToTexture:
			src, ok1 := c.buffers[cp.SrcID]
			dst, ok2 := c.textures[cp.DstTex.Texture]
			if !ok1 || !ok2 {
				return fmt.Errorf("apple: CopyBufferToTexture unknown resource (%d -> %d)", cp.SrcID, cp.DstTex.Texture)
			}
			bpp := dst.desc.Format.BytesPerPixel()
			if bpp == 0 {
				bpp = 4
			}
			bpr := cp.SrcLayout.BytesPerRow
			if bpr == 0 {
				bpr = cp.Width * bpp
			}
			rows := cp.SrcLayout.RowsPerImage
			if rows == 0 {
				rows = cp.Height
			}
			bpi := uint64(bpr) * uint64(rows)
			layers := max(cp.Layers, 1)
			for layer := uint32(0); layer < layers; layer++ {
				blit.Send(
					sel("copyFromBuffer:sourceOffset:sourceBytesPerRow:sourceBytesPerImage:sourceSize:toTexture:destinationSlice:destinationLevel:destinationOrigin:"),
					src.id,
					uint(cp.SrcLayout.Offset+uint64(layer)*bpi),
					uint(bpr),
					uint(bpi),
					mtlSize{W: uint(cp.Width), H: uint(cp.Height), D: 1},
					dst.id,
					uint(cp.DstTex.ArrayLayer+layer),
					uint(cp.DstTex.MipLevel),
					mtlOrigin{X: uint(cp.DstTex.OriginX), Y: uint(cp.DstTex.OriginY), Z: 0},
				)
			}

		case driver.GPUCopyTextureToTexture:
			src, ok1 := c.textures[cp.SrcTex.Texture]
			dst, ok2 := c.textures[cp.DstTex.Texture]
			if !ok1 || !ok2 {
				return fmt.Errorf("apple: CopyTextureToTexture unknown texture (%d -> %d)", cp.SrcTex.Texture, cp.DstTex.Texture)
			}
			layers := max(cp.Layers, 1)
			for layer := uint32(0); layer < layers; layer++ {
				blit.Send(
					sel("copyFromTexture:sourceSlice:sourceLevel:sourceOrigin:sourceSize:toTexture:destinationSlice:destinationLevel:destinationOrigin:"),
					src.id,
					uint(cp.SrcTex.ArrayLayer+layer),
					uint(cp.SrcTex.MipLevel),
					mtlOrigin{X: uint(cp.SrcTex.OriginX), Y: uint(cp.SrcTex.OriginY), Z: 0},
					mtlSize{W: uint(cp.Width), H: uint(cp.Height), D: 1},
					dst.id,
					uint(cp.DstTex.ArrayLayer+layer),
					uint(cp.DstTex.MipLevel),
					mtlOrigin{X: uint(cp.DstTex.OriginX), Y: uint(cp.DstTex.OriginY), Z: 0},
				)
			}
		}
	}
	return nil
}

func (c *gpuContext) passNeedsDepth(pass driver.GPUPass) bool {
	for _, cmd := range pass.Commands {
		if cmd.Kind == driver.GPUCmdSetPipeline {
			if p := c.pipelines[cmd.ID]; p != nil && p.hasDepth {
				return true
			}
		}
	}
	return false
}

func (c *gpuContext) encodePass(cb objc.ID, pass driver.GPUPass) error {
	rp := objc.ID(Class("MTLRenderPassDescriptor")).Send(sel("renderPassDescriptor"))
	cas := rp.Send(selColorAttachments)

	attachments := pass.ColorAttachments
	if len(attachments) == 0 {
		attachments = []driver.GPUColorAttachment{{
			LoadOp:  driver.GPULoadLoad,
			StoreOp: driver.GPUStoreStore,
		}}
	}

	var (
		passKey       pipeStateKey
		passW, passH  int
		targetSurface bool
	)
	passKey.colorCount = uint8(min(len(attachments), 4))
	passKey.sampleCount = 1

	for i, ca := range attachments {
		if i >= 4 {
			break
		}
		att := cas.Send(selObjectAtIndexedSubscript, uint(i))
		if ca.Texture == 0 {
			targetSurface = true
			if c.surfaceTex == 0 {
				if err := c.allocateSurfaceTargets(); err != nil {
					return err
				}
			}
			if c.sampleCount > 1 && c.surfaceMSAATex != 0 {
				att.Send(sel("setTexture:"), c.surfaceMSAATex)
				att.Send(sel("setResolveTexture:"), c.surfaceTex)
				att.Send(sel("setStoreAction:"), uint(mtlStoreActionMultisampleResolve))
				passKey.sampleCount = uint8(c.sampleCount)
			} else {
				att.Send(sel("setTexture:"), c.surfaceTex)
				att.Send(sel("setStoreAction:"), mtlStoreAction(ca.StoreOp))
			}
			passKey.colorFmts[i] = uint16(c.surfaceMTLFmt)
			if i == 0 {
				passW, passH = c.width, c.height
			}
		} else {
			tex, ok := c.textures[ca.Texture]
			if !ok || tex.id == 0 {
				return fmt.Errorf("apple: render pass color attachment %d not found", ca.Texture)
			}
			att.Send(sel("setTexture:"), tex.id)
			att.Send(sel("setLevel:"), uint(ca.MipLevel))
			att.Send(sel("setSlice:"), uint(ca.ArrayLayer))
			if ca.ResolveTarget != 0 {
				if rtex, ok := c.textures[ca.ResolveTarget]; ok && rtex.id != 0 {
					att.Send(sel("setResolveTexture:"), rtex.id)
					att.Send(sel("setStoreAction:"), uint(mtlStoreActionMultisampleResolve))
				} else {
					att.Send(sel("setStoreAction:"), mtlStoreAction(ca.StoreOp))
				}
			} else {
				att.Send(sel("setStoreAction:"), mtlStoreAction(ca.StoreOp))
			}
			passKey.colorFmts[i] = uint16(tex.mtlFmt)
			if i == 0 {
				passW = int(max(tex.desc.Width>>ca.MipLevel, 1))
				passH = int(max(tex.desc.Height>>ca.MipLevel, 1))
				passKey.sampleCount = uint8(max(tex.desc.SampleCount, 1))
			}
		}
		att.Send(sel("setLoadAction:"), mtlLoadAction(ca.LoadOp))
		if ca.LoadOp == driver.GPULoadClear {
			r, g, b, a := ca.ClearColor.Components()
			att.Send(sel("setClearColor:"), mtlClearColor{R: r, G: g, B: b, A: a})
		}
	}

	// Configure depth/stencil attachment when requested by the pass or needed by
	// a depth-enabled pipeline drawing to the swapchain surface.
	if pass.DepthStencil != nil {
		ds := pass.DepthStencil
		var depthTex objc.ID
		var depthFmt uint
		if ds.Texture == 0 {
			depthTex = c.surfaceDepthTex
			depthFmt = c.surfaceDepthMTLFmt
		} else if t, ok := c.textures[ds.Texture]; ok && t.id != 0 {
			depthTex = t.id
			depthFmt = t.mtlFmt
			if passW == 0 {
				passW, passH = int(t.desc.Width), int(t.desc.Height)
			}
		}
		if depthTex != 0 {
			da := rp.Send(sel("depthAttachment"))
			da.Send(sel("setTexture:"), depthTex)
			da.Send(sel("setLoadAction:"), mtlLoadAction(ds.DepthLoadOp))
			da.Send(sel("setStoreAction:"), mtlStoreAction(ds.DepthStoreOp))
			da.Send(sel("setClearDepth:"), float64(ds.ClearDepth))
			passKey.depthFmt = uint16(depthFmt)
			if depthFmt == mtlPixelFormatDepth32FloatStencil8 {
				sa := rp.Send(sel("stencilAttachment"))
				sa.Send(sel("setTexture:"), depthTex)
				sa.Send(sel("setLoadAction:"), mtlLoadAction(ds.StencilLoadOp))
				sa.Send(sel("setStoreAction:"), mtlStoreAction(ds.StencilStoreOp))
				sa.Send(sel("setClearStencil:"), ds.ClearStencil)
			}
		}
	} else if targetSurface && c.surfaceDepthTex != 0 && c.passNeedsDepth(pass) {
		da := rp.Send(sel("depthAttachment"))
		da.Send(sel("setTexture:"), c.surfaceDepthTex)
		da.Send(sel("setLoadAction:"), uint(mtlLoadActionLoad))
		da.Send(sel("setStoreAction:"), uint(mtlStoreActionStore))
		passKey.depthFmt = uint16(c.surfaceDepthMTLFmt)
		if c.surfaceDepthMTLFmt == mtlPixelFormatDepth32FloatStencil8 {
			sa := rp.Send(sel("stencilAttachment"))
			sa.Send(sel("setTexture:"), c.surfaceDepthTex)
			sa.Send(sel("setLoadAction:"), uint(mtlLoadActionLoad))
			sa.Send(sel("setStoreAction:"), uint(mtlStoreActionStore))
		}
	}

	enc := cb.Send(sel("renderCommandEncoderWithDescriptor:"), rp)
	if enc == 0 {
		return fmt.Errorf("apple: Metal renderCommandEncoderWithDescriptor returned nil")
	}
	defer enc.Send(sel("endEncoding"))

	if passW > 0 && passH > 0 {
		enc.Send(sel("setViewport:"), mtlViewport{
			X: 0, Y: 0, W: float64(passW), H: float64(passH), ZNear: 0, ZFar: 1,
		})
		enc.Send(sel("setScissorRect:"), mtlScissorRect{
			X: 0, Y: 0, W: uint(passW), H: uint(passH),
		})
	}

	var (
		activePipe      *mtlPipeline
		indexBuf        objc.ID
		indexFmt        driver.GPUIndexFormat
		indexBaseOffset uint64
		boundGroups     [8]boundGroupState
		groupsDirty     bool
	)

	for _, cmd := range pass.Commands {
		switch cmd.Kind {
		case driver.GPUCmdSetViewport:
			enc.Send(sel("setViewport:"), mtlViewport{
				X:     float64(cmd.F0),
				Y:     float64(cmd.F1),
				W:     float64(cmd.F2),
				H:     float64(cmd.F3),
				ZNear: float64(cmd.F4),
				ZFar:  float64(cmd.F5),
			})

		case driver.GPUCmdSetScissor:
			sx := max(int(cmd.I0), 0)
			sy := max(int(cmd.I1), 0)
			sw := max(int(cmd.I2), 0)
			sh := max(int(cmd.I3), 0)
			if passW > 0 {
				if sx > passW {
					sx = passW
				}
				if sx+sw > passW {
					sw = passW - sx
				}
			}
			if passH > 0 {
				if sy > passH {
					sy = passH
				}
				if sy+sh > passH {
					sh = passH - sy
				}
			}
			enc.Send(sel("setScissorRect:"), mtlScissorRect{
				X: uint(sx), Y: uint(sy), W: uint(sw), H: uint(sh),
			})

		case driver.GPUCmdSetPipeline:
			p, ok := c.pipelines[cmd.ID]
			if !ok {
				return fmt.Errorf("apple: unknown GPU pipeline %d", cmd.ID)
			}
			state, err := c.buildPipelineState(p, passKey)
			if err != nil {
				return err
			}
			enc.Send(sel("setRenderPipelineState:"), state)
			enc.Send(sel("setFrontFacingWinding:"), mtlWinding(p.desc.Primitive.FrontFace))
			enc.Send(sel("setCullMode:"), mtlCullMode(p.desc.Primitive.CullMode))
			if p.depthState != 0 && passKey.depthFmt != 0 {
				enc.Send(sel("setDepthStencilState:"), p.depthState)
				if p.desc.DepthStencil != nil && (p.desc.DepthStencil.DepthBias != 0 || p.desc.DepthStencil.DepthBiasSlopeScale != 0) {
					enc.Send(sel("setDepthBias:slopeScale:clamp:"),
						float32(p.desc.DepthStencil.DepthBias),
						p.desc.DepthStencil.DepthBiasSlopeScale,
						float32(0),
					)
				} else {
					enc.Send(sel("setDepthBias:slopeScale:clamp:"), float32(0), float32(0), float32(0))
				}
			} else if c.noDepthState != 0 {
				enc.Send(sel("setDepthStencilState:"), c.noDepthState)
			}
			activePipe = p
			groupsDirty = true

		case driver.GPUCmdSetBindGroup:
			if int(cmd.Slot) < len(boundGroups) {
				boundGroups[cmd.Slot] = boundGroupState{
					id:         cmd.ID,
					dynOffsets: cmd.DynamicOffsets,
					valid:      true,
				}
				groupsDirty = true
			}

		case driver.GPUCmdSetVertexBuffer:
			if b, ok := c.buffers[cmd.ID]; ok && b.id != 0 {
				enc.Send(sel("setVertexBuffer:offset:atIndex:"), b.id, uint(cmd.Offset), uint(cmd.Slot))
			}

		case driver.GPUCmdSetIndexBuffer:
			if b, ok := c.buffers[cmd.ID]; ok && b.id != 0 {
				indexBuf = b.id
				indexFmt = cmd.IndexFormat
				indexBaseOffset = cmd.Offset
			}

		case driver.GPUCmdSetStencilReference:
			enc.Send(sel("setStencilReferenceValue:"), cmd.U0)

		case driver.GPUCmdSetBlendConstant:
			enc.Send(sel("setBlendColorRed:green:blue:alpha:"), cmd.F0, cmd.F1, cmd.F2, cmd.F3)

		case driver.GPUCmdDraw:
			if activePipe != nil && cmd.U0 > 0 {
				c.flushBindGroups(enc, activePipe, &boundGroups, &groupsDirty)
				enc.Send(sel("drawPrimitives:vertexStart:vertexCount:"),
					mtlPrimitiveType(activePipe.desc.Primitive.Topology),
					uint(cmd.U1), uint(cmd.U0))
			}

		case driver.GPUCmdDrawInstanced:
			if activePipe != nil && cmd.U0 > 0 && cmd.U1 > 0 {
				c.flushBindGroups(enc, activePipe, &boundGroups, &groupsDirty)
				enc.Send(sel("drawPrimitives:vertexStart:vertexCount:instanceCount:baseInstance:"),
					mtlPrimitiveType(activePipe.desc.Primitive.Topology),
					uint(cmd.U2), uint(cmd.U0), uint(cmd.U1), uint(cmd.U3))
			}

		case driver.GPUCmdDrawIndexed:
			if activePipe != nil && indexBuf != 0 && cmd.U0 > 0 {
				c.flushBindGroups(enc, activePipe, &boundGroups, &groupsDirty)
				idxType, stride := mtlIndexType(indexFmt)
				off := indexBaseOffset + uint64(cmd.U1)*stride
				if cmd.I0 == 0 {
					enc.Send(sel("drawIndexedPrimitives:indexCount:indexType:indexBuffer:indexBufferOffset:"),
						mtlPrimitiveType(activePipe.desc.Primitive.Topology),
						uint(cmd.U0), idxType, indexBuf, uint(off))
				} else {
					enc.Send(sel("drawIndexedPrimitives:indexCount:indexType:indexBuffer:indexBufferOffset:instanceCount:baseVertex:baseInstance:"),
						mtlPrimitiveType(activePipe.desc.Primitive.Topology),
						uint(cmd.U0), idxType, indexBuf, uint(off), uint(1), int(cmd.I0), uint(0))
				}
			}

		case driver.GPUCmdDrawIndexedInstanced:
			if activePipe != nil && indexBuf != 0 && cmd.U0 > 0 && cmd.U1 > 0 {
				c.flushBindGroups(enc, activePipe, &boundGroups, &groupsDirty)
				idxType, stride := mtlIndexType(indexFmt)
				off := indexBaseOffset + uint64(cmd.U2)*stride
				enc.Send(sel("drawIndexedPrimitives:indexCount:indexType:indexBuffer:indexBufferOffset:instanceCount:baseVertex:baseInstance:"),
					mtlPrimitiveType(activePipe.desc.Primitive.Topology),
					uint(cmd.U0), idxType, indexBuf, uint(off), uint(cmd.U1), int(cmd.I0), uint(cmd.U3))
			}
		}
	}
	return nil
}

func (c *gpuContext) flushBindGroups(enc objc.ID, pipe *mtlPipeline, bound *[8]boundGroupState, dirty *bool) {
	if !*dirty || pipe == nil {
		return
	}
	*dirty = false
	c.bindStageSlots(enc, pipe.vsSlots, bound, true)
	if pipe.fsFn != 0 {
		c.bindStageSlots(enc, pipe.fsSlots, bound, false)
	}
}

func (c *gpuContext) bindStageSlots(enc objc.ID, slots []msl.BindingSlot, bound *[8]boundGroupState, vertex bool) {
	for _, sl := range slots {
		if int(sl.Group) >= len(bound) || !bound[sl.Group].valid {
			continue
		}
		gs := bound[sl.Group]
		bg, ok := c.bindGroups[gs.id]
		if !ok {
			continue
		}
		entry, ok := bg.entries[sl.Binding]
		if !ok {
			continue
		}
		switch sl.Kind {
		case shaderir.BindingUniformBuffer, shaderir.BindingStorageBufferRead:
			b, ok := c.buffers[entry.Buffer]
			if !ok || b.id == 0 {
				continue
			}
			offset := entry.Offset
			if len(gs.dynOffsets) > 0 {
				if lay := c.layouts[bg.layoutID]; lay != nil {
					for i, db := range lay.dynamicBindings {
						if db == sl.Binding && i < len(gs.dynOffsets) {
							offset += uint64(gs.dynOffsets[i])
							break
						}
					}
				}
			}
			if vertex {
				enc.Send(sel("setVertexBuffer:offset:atIndex:"), b.id, uint(offset), uint(sl.Slot))
			} else {
				enc.Send(sel("setFragmentBuffer:offset:atIndex:"), b.id, uint(offset), uint(sl.Slot))
			}
		case shaderir.BindingTexture:
			t, ok := c.textures[entry.Texture]
			if !ok || t.id == 0 {
				continue
			}
			if vertex {
				enc.Send(sel("setVertexTexture:atIndex:"), t.id, uint(sl.Slot))
			} else {
				enc.Send(sel("setFragmentTexture:atIndex:"), t.id, uint(sl.Slot))
			}
		case shaderir.BindingSampler:
			s, ok := c.samplers[entry.Sampler]
			if !ok || s.id == 0 {
				continue
			}
			if vertex {
				enc.Send(sel("setVertexSamplerState:atIndex:"), s.id, uint(sl.Slot))
			} else {
				enc.Send(sel("setFragmentSamplerState:atIndex:"), s.id, uint(sl.Slot))
			}
		}
	}
}

func (c *gpuContext) readTexture(op driver.GPUResourceOp) gpuReadback {
	var (
		texID  objc.ID
		texW   uint32
		texH   uint32
		texFmt driver.GPUTextureFormat
	)
	if op.ReadbackSurface || op.ID == 0 {
		if c.surfaceTex == 0 {
			return gpuReadback{id: op.ReadbackID, errMsg: "surface texture not allocated"}
		}
		texID = c.surfaceTex
		texW = uint32(c.width)
		texH = uint32(c.height)
		texFmt = c.surfaceFormat
	} else {
		t, ok := c.textures[op.ID]
		if !ok || t.id == 0 {
			return gpuReadback{id: op.ReadbackID, errMsg: fmt.Sprintf("unknown texture %d", op.ID)}
		}
		texID = t.id
		texW = max(t.desc.Width>>op.ReadbackRegion.MipLevel, 1)
		texH = max(t.desc.Height>>op.ReadbackRegion.MipLevel, 1)
		texFmt = t.desc.Format
	}
	if texFmt.IsDepth() {
		return gpuReadback{id: op.ReadbackID, errMsg: "depth texture readback is not supported"}
	}
	rx, ry := op.ReadbackRegion.X, op.ReadbackRegion.Y
	rw, rh := op.ReadbackRegion.Width, op.ReadbackRegion.Height
	if rw == 0 && rx < texW {
		rw = texW - rx
	}
	if rh == 0 && ry < texH {
		rh = texH - ry
	}
	if rw == 0 || rh == 0 || rx+rw > texW || ry+rh > texH {
		return gpuReadback{id: op.ReadbackID, errMsg: "readback region out of bounds"}
	}

	bpp := texFmt.BytesPerPixel()
	if bpp == 0 {
		bpp = 4
	}
	rowBytes := ((rw*bpp + 3) / 4) * 4
	totalBytes := uint64(rowBytes) * uint64(rh)

	staging := mtlDevice.Send(sel("newBufferWithLength:options:"), uint(totalBytes), uint(mtlStorageModeShared))
	if staging == 0 {
		return gpuReadback{id: op.ReadbackID, errMsg: "failed to allocate Metal readback staging buffer"}
	}
	defer staging.Send(selRelease)

	cb := mtlQueue.Send(sel("commandBuffer"))
	if cb == 0 {
		return gpuReadback{id: op.ReadbackID, errMsg: "failed to create Metal readback command buffer"}
	}
	blit := cb.Send(sel("blitCommandEncoder"))
	blit.Send(
		sel("copyFromTexture:sourceSlice:sourceLevel:sourceOrigin:sourceSize:toBuffer:destinationOffset:destinationBytesPerRow:destinationBytesPerImage:"),
		texID,
		uint(op.ReadbackRegion.ArrayLayer),
		uint(op.ReadbackRegion.MipLevel),
		mtlOrigin{X: uint(rx), Y: uint(ry), Z: 0},
		mtlSize{W: uint(rw), H: uint(rh), D: 1},
		staging,
		uint(0),
		uint(rowBytes),
		uint(totalBytes),
	)
	blit.Send(sel("endEncoding"))
	cb.Send(sel("commit"))
	cb.Send(sel("waitUntilCompleted"))
	if status := objc.Send[uint](cb, sel("status")); status != mtlCommandBufferStatusCompleted {
		return gpuReadback{id: op.ReadbackID, errMsg: fmt.Sprintf("Metal readback failed (status %d): %s", status, errorText(cb.Send(sel("error"))))}
	}
	if c.lastCB != 0 {
		c.lastCB.Send(selRelease)
		c.lastCB = 0
	}

	ptr := objc.Send[unsafe.Pointer](staging, sel("contents"))
	if ptr == nil {
		return gpuReadback{id: op.ReadbackID, errMsg: "Metal staging buffer has nil contents"}
	}
	raw := unsafe.Slice((*byte)(ptr), totalBytes)
	rgba := unpackToRGBA8(raw, int(rw), int(rh), int(rowBytes), texFmt)
	return gpuReadback{id: op.ReadbackID, pixels: rgba}
}

func unpackToRGBA8(raw []byte, w, h, rowBytes int, format driver.GPUTextureFormat) []byte {
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		srow := raw[y*rowBytes:]
		drow := out[y*w*4:]
		switch format {
		case driver.GPUFormatBGRA8Unorm:
			for x := 0; x < w; x++ {
				si, di := x*4, x*4
				drow[di+0] = srow[si+2]
				drow[di+1] = srow[si+1]
				drow[di+2] = srow[si+0]
				drow[di+3] = srow[si+3]
			}
		case driver.GPUFormatR8Unorm:
			for x := 0; x < w; x++ {
				di := x * 4
				drow[di+0] = srow[x]
				drow[di+1] = 0
				drow[di+2] = 0
				drow[di+3] = 255
			}
		case driver.GPUFormatRG8Unorm:
			for x := 0; x < w; x++ {
				si, di := x*2, x*4
				drow[di+0] = srow[si+0]
				drow[di+1] = srow[si+1]
				drow[di+2] = 0
				drow[di+3] = 255
			}
		case driver.GPUFormatRGBA16Float:
			for x := 0; x < w; x++ {
				si, di := x*8, x*4
				drow[di+0] = floatToByte(float16ToFloat32(binary.LittleEndian.Uint16(srow[si+0:])))
				drow[di+1] = floatToByte(float16ToFloat32(binary.LittleEndian.Uint16(srow[si+2:])))
				drow[di+2] = floatToByte(float16ToFloat32(binary.LittleEndian.Uint16(srow[si+4:])))
				drow[di+3] = floatToByte(float16ToFloat32(binary.LittleEndian.Uint16(srow[si+6:])))
			}
		default:
			copy(drow[:w*4], srow[:w*4])
		}
	}
	return out
}

func floatToByte(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return byte(math.Round(float64(v * 255)))
}

func float16ToFloat32(h uint16) float32 {
	sign := uint32(h>>15) & 1
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch exp {
	case 0:
		if frac == 0 {
			return math.Float32frombits(sign << 31)
		}
		// Subnormal half-float
		for frac&0x400 == 0 {
			frac <<= 1
			exp--
		}
		exp++
		frac &= 0x3ff
		return math.Float32frombits((sign << 31) | ((exp + 127 - 15) << 23) | (frac << 13))
	case 31:
		return math.Float32frombits((sign << 31) | (0xff << 23) | (frac << 13))
	default:
		return math.Float32frombits((sign << 31) | ((exp + 127 - 15) << 23) | (frac << 13))
	}
}

func newDepthStencilState(compare uint, write bool) objc.ID {
	d := objc.ID(Class("MTLDepthStencilDescriptor")).Send(selNew)
	defer d.Send(selRelease)
	d.Send(sel("setDepthCompareFunction:"), compare)
	d.Send(sel("setDepthWriteEnabled:"), write)
	return mtlDevice.Send(sel("newDepthStencilStateWithDescriptor:"), d)
}

func mtlPixelFormat(f driver.GPUTextureFormat) uint {
	switch f {
	case driver.GPUFormatRGBA8Unorm:
		return mtlPixelFormatRGBA8Unorm
	case driver.GPUFormatRGBA8UnormSRGB:
		return mtlPixelFormatRGBA8UnormSRGB
	case driver.GPUFormatBGRA8Unorm:
		return mtlPixelFormatBGRA8Unorm
	case driver.GPUFormatRG8Unorm:
		return mtlPixelFormatRG8Unorm
	case driver.GPUFormatR8Unorm:
		return mtlPixelFormatR8Unorm
	case driver.GPUFormatRGBA16Float:
		return mtlPixelFormatRGBA16Float
	case driver.GPUFormatDepth24Plus, driver.GPUFormatDepth32Float:
		return mtlPixelFormatDepth32Float
	case driver.GPUFormatDepth24PlusStencil8:
		return mtlPixelFormatDepth32FloatStencil8
	default:
		return 0
	}
}

func mtlVertexFormat(f driver.GPUVertexFormat) uint {
	switch f {
	case driver.GPUVertexFloat32:
		return mtlVertexFormatFloat
	case driver.GPUVertexFloat32x2:
		return mtlVertexFormatFloat2
	case driver.GPUVertexFloat32x3:
		return mtlVertexFormatFloat3
	case driver.GPUVertexFloat32x4:
		return mtlVertexFormatFloat4
	case driver.GPUVertexSint32:
		return mtlVertexFormatInt
	case driver.GPUVertexSint32x2:
		return mtlVertexFormatInt2
	case driver.GPUVertexSint32x3:
		return mtlVertexFormatInt3
	case driver.GPUVertexSint32x4:
		return mtlVertexFormatInt4
	case driver.GPUVertexUint32:
		return mtlVertexFormatUInt
	case driver.GPUVertexUint32x2:
		return mtlVertexFormatUInt2
	case driver.GPUVertexUint32x3:
		return mtlVertexFormatUInt3
	case driver.GPUVertexUint32x4:
		return mtlVertexFormatUInt4
	case driver.GPUVertexUnorm8x4:
		return mtlVertexFormatUChar4Normalized
	default:
		return mtlVertexFormatFloat4
	}
}

func mtlCompareFunc(f driver.GPUCompareFunc) uint {
	switch f {
	case driver.GPUCompareNever:
		return mtlCompareNever
	case driver.GPUCompareLess:
		return mtlCompareLess
	case driver.GPUCompareEqual:
		return mtlCompareEqual
	case driver.GPUCompareLessEqual:
		return mtlCompareLessEqual
	case driver.GPUCompareGreater:
		return mtlCompareGreater
	case driver.GPUCompareNotEqual:
		return mtlCompareNotEqual
	case driver.GPUCompareGreaterEqual:
		return mtlCompareGreaterEqual
	case driver.GPUCompareAlways:
		return mtlCompareAlways
	default:
		return mtlCompareLess
	}
}

func mtlPrimitiveType(t driver.GPUPrimitiveTopology) uint {
	switch t {
	case driver.GPUTopologyTriangleList:
		return mtlPrimitiveTypeTriangle
	case driver.GPUTopologyTriangleStrip:
		return mtlPrimitiveTypeTriangleStrip
	case driver.GPUTopologyLineList:
		return mtlPrimitiveTypeLine
	case driver.GPUTopologyLineStrip:
		return mtlPrimitiveTypeLineStrip
	case driver.GPUTopologyPointList:
		return mtlPrimitiveTypePoint
	default:
		return mtlPrimitiveTypeTriangle
	}
}

func mtlIndexType(f driver.GPUIndexFormat) (uint, uint64) {
	if f == driver.GPUIndexUint32 {
		return mtlIndexTypeUInt32, 4
	}
	return mtlIndexTypeUInt16, 2
}

func mtlWinding(f driver.GPUFrontFace) uint {
	if f == driver.GPUFrontCW {
		return mtlWindingClockwise
	}
	return mtlWindingCounterClockwise
}

func mtlCullMode(m driver.GPUCullMode) uint {
	switch m {
	case driver.GPUCullFront:
		return mtlCullModeFront
	case driver.GPUCullBack:
		return mtlCullModeBack
	default:
		return mtlCullModeNone
	}
}

func mtlBlendFactor(f driver.GPUBlendFactor) uint {
	switch f {
	case driver.GPUBlendZero:
		return mtlBlendFactorZero
	case driver.GPUBlendOne:
		return mtlBlendFactorOne
	case driver.GPUBlendSrc:
		return mtlBlendFactorSourceColor
	case driver.GPUBlendOneMinusSrc:
		return mtlBlendFactorOneMinusSourceColor
	case driver.GPUBlendSrcAlpha:
		return mtlBlendFactorSourceAlpha
	case driver.GPUBlendOneMinusSrcAlpha:
		return mtlBlendFactorOneMinusSourceAlpha
	case driver.GPUBlendDst:
		return mtlBlendFactorDestinationColor
	case driver.GPUBlendOneMinusDst:
		return mtlBlendFactorOneMinusDestinationColor
	case driver.GPUBlendDstAlpha:
		return mtlBlendFactorDestinationAlpha
	case driver.GPUBlendOneMinusDstAlpha:
		return mtlBlendFactorOneMinusDestinationAlpha
	case driver.GPUBlendConstant:
		return mtlBlendFactorBlendColor
	case driver.GPUBlendOneMinusConstant:
		return mtlBlendFactorOneMinusBlendColor
	default:
		return mtlBlendFactorOne
	}
}

func mtlBlendOp(op driver.GPUBlendOperation) uint {
	switch op {
	case driver.GPUBlendSubtract:
		return mtlBlendOpSubtract
	case driver.GPUBlendReverseSubtract:
		return mtlBlendOpReverseSubtract
	case driver.GPUBlendMin:
		return mtlBlendOpMin
	case driver.GPUBlendMax:
		return mtlBlendOpMax
	default:
		return mtlBlendOpAdd
	}
}

func mtlColorWriteMask(m driver.GPUColorWriteMask) uint {
	if m == 0 || m == driver.GPUColorWriteAll {
		return 0xf
	}
	var out uint
	if m&driver.GPUColorWriteRed != 0 {
		out |= 1 << 3
	}
	if m&driver.GPUColorWriteGreen != 0 {
		out |= 1 << 2
	}
	if m&driver.GPUColorWriteBlue != 0 {
		out |= 1 << 1
	}
	if m&driver.GPUColorWriteAlpha != 0 {
		out |= 1 << 0
	}
	return out
}

func mtlLoadAction(op driver.GPULoadOp) uint {
	if op == driver.GPULoadClear {
		return mtlLoadActionClear
	}
	return mtlLoadActionLoad
}

func mtlStoreAction(op driver.GPUStoreOp) uint {
	if op == driver.GPUStoreDiscard {
		return mtlStoreActionDontCare
	}
	return mtlStoreActionStore
}

func mtlMinMagFilter(m driver.GPUFilterMode) uint {
	if m == driver.GPUFilterLinear {
		return mtlSamplerMinMagLinear
	}
	return mtlSamplerMinMagNearest
}

func mtlMipFilter(m driver.GPUFilterMode) uint {
	if m == driver.GPUFilterLinear {
		return mtlSamplerMipLinear
	}
	return mtlSamplerMipNearest
}

func mtlAddressMode(m driver.GPUAddressMode) uint {
	switch m {
	case driver.GPUAddressRepeat:
		return mtlSamplerAddressRepeat
	case driver.GPUAddressMirrorRepeat:
		return mtlSamplerAddressMirrorRepeat
	default:
		return mtlSamplerAddressClampToEdge
	}
}
