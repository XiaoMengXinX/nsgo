//go:build darwin

package apple

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/shader/wgsl"
)

const testColor3DWGSL = `
struct Uniforms {
	mvp: mat4x4<f32>,
}

@group(0) @binding(0) var<uniform> u: Uniforms;

struct VertexIn {
	@location(0) pos: vec3<f32>,
	@location(1) color: vec4<f32>,
}

struct VertexOut {
	@builtin(position) pos: vec4<f32>,
	@location(0) color: vec4<f32>,
}

@vertex
fn vs_main(in: VertexIn) -> VertexOut {
	var out: VertexOut;
	out.pos = u.mvp * vec4<f32>(in.pos, 1.0);
	out.color = in.color;
	return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
	return in.color;
}
`

const testTexturedLit3DWGSL = `
struct SceneUniforms {
	mvp: mat4x4<f32>,
	lightDir: vec4<f32>,
}

@group(0) @binding(0) var<uniform> scene: SceneUniforms;
@group(0) @binding(1) var tex: texture_2d<f32>;
@group(0) @binding(2) var samp: sampler;

struct VertexIn {
	@location(0) pos: vec3<f32>,
	@location(1) normal: vec3<f32>,
	@location(2) uv: vec2<f32>,
}

struct VertexOut {
	@builtin(position) pos: vec4<f32>,
	@location(0) normal: vec3<f32>,
	@location(1) uv: vec2<f32>,
}

@vertex
fn vs_main(in: VertexIn) -> VertexOut {
	var out: VertexOut;
	out.pos = scene.mvp * vec4<f32>(in.pos, 1.0);
	out.normal = in.normal;
	out.uv = in.uv;
	return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
	let n = normalize(in.normal);
	let l = normalize(scene.lightDir.xyz);
	let ndotl = max(dot(n, l), 0.2);
	let albedo = textureSample(tex, samp, in.uv);
	return vec4<f32>(albedo.rgb * ndotl, albedo.a);
}
`

const testInstancedWGSL = `
struct VertexIn {
	@location(0) pos: vec2<f32>,
	@location(1) offset: vec2<f32>,
	@location(2) color: vec4<f32>,
}

struct VertexOut {
	@builtin(position) pos: vec4<f32>,
	@location(0) color: vec4<f32>,
}

@vertex
fn vs_main(in: VertexIn) -> VertexOut {
	var out: VertexOut;
	out.pos = vec4<f32>(in.pos + in.offset, 0.5, 1.0);
	out.color = in.color;
	return out;
}

@fragment
fn fs_main(in: VertexOut) -> @location(0) vec4<f32> {
	return in.color;
}
`

func packF32(vals ...float32) []byte {
	out := make([]byte, len(vals)*4)
	for i, v := range vals {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

func packU16(vals ...uint16) []byte {
	out := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(out[i*2:], v)
	}
	return out
}

func packU32(vals ...uint32) []byte {
	out := make([]byte, len(vals)*4)
	for i, v := range vals {
		binary.LittleEndian.PutUint32(out[i*4:], v)
	}
	return out
}

func testPerspectiveRH(fovYRad, aspect, near, far float32) [16]float32 {
	f := float32(1.0 / math.Tan(float64(fovYRad)/2.0))
	return [16]float32{
		f / aspect, 0, 0, 0,
		0, f, 0, 0,
		0, 0, far / (near - far), -1,
		0, 0, (far * near) / (near - far), 0,
	}
}

func testTranslate(tx, ty, tz float32) [16]float32 {
	return [16]float32{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		tx, ty, tz, 1,
	}
}

func testMulMat4(a, b [16]float32) [16]float32 {
	var out [16]float32
	for c := 0; c < 4; c++ {
		for r := 0; r < 4; r++ {
			var sum float32
			for k := 0; k < 4; k++ {
				sum += a[k*4+r] * b[c*4+k]
			}
			out[c*4+r] = sum
		}
	}
	return out
}

func TestMetalDepthTested3DCubeMatchesSoftware(t *testing.T) {
	if !GPUAvailable() {
		t.Skip("no Metal device")
	}
	prog, err := wgsl.CompilePipeline(testColor3DWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	ir := prog.IR

	const w, h = 120, 120
	cfg := driver.GPUViewConfig{
		ColorFormat: driver.GPUFormatBGRA8Unorm,
		DepthFormat: driver.GPUFormatDepth24Plus,
		SampleCount: 1,
	}

	// Near red quad at z=+0.5 drawn FIRST; larger far blue quad at z=-0.5 drawn SECOND.
	verts := packF32(
		-0.5, -0.5, 0.5, 1, 0, 0, 1,
		0.5, -0.5, 0.5, 1, 0, 0, 1,
		0.5, 0.5, 0.5, 1, 0, 0, 1,
		-0.5, 0.5, 0.5, 1, 0, 0, 1,
		-1.0, -1.0, -0.5, 0, 0, 1, 1,
		1.0, -1.0, -0.5, 0, 0, 1, 1,
		1.0, 1.0, -0.5, 0, 0, 1, 1,
		-1.0, 1.0, -0.5, 0, 0, 1, 1,
	)
	indices := packU16(0, 1, 2, 0, 2, 3, 4, 5, 6, 4, 6, 7)
	mvp := testMulMat4(testPerspectiveRH(math.Pi/3, 1.0, 0.1, 20.0), testTranslate(0, 0, -3.0))

	setupFrame := driver.GPUFrame{
		Ops: []driver.GPUResourceOp{
			{Kind: driver.GPUOpCreateBuffer, ID: 1, Buffer: &driver.GPUBufferDesc{Size: uint64(len(verts)), Usage: driver.GPUBufferVertex, Data: verts}},
			{Kind: driver.GPUOpCreateBuffer, ID: 2, Buffer: &driver.GPUBufferDesc{Size: uint64(len(indices)), Usage: driver.GPUBufferIndex, Data: indices}},
			{Kind: driver.GPUOpCreateBuffer, ID: 3, Buffer: &driver.GPUBufferDesc{Size: 64, Usage: driver.GPUBufferUniform | driver.GPUBufferCopyDst, Data: packF32(mvp[:]...)}},
			{Kind: driver.GPUOpCreateBindGroupLayout, ID: 4, BindGroupLayout: &driver.GPUBindGroupLayoutDesc{
				Entries: []driver.GPUBindGroupLayoutEntry{{Binding: 0, Visibility: driver.GPUStageVertex, Type: driver.GPUBindingUniform}},
			}},
			{Kind: driver.GPUOpCreateBindGroup, ID: 5, BindGroup: &driver.GPUBindGroupDesc{
				Layout:  4,
				Entries: []driver.GPUBindGroupEntry{{Binding: 0, Buffer: 3, Size: 64}},
			}},
			{Kind: driver.GPUOpCreatePipeline, ID: 6, Pipeline: &driver.GPUPipelineDesc{
				Vertex:   driver.GPUShaderStage{IR: ir, EntryPoint: "vs_main"},
				Fragment: driver.GPUShaderStage{IR: ir, EntryPoint: "fs_main"},
				Layouts:  []driver.GPUID{4},
				VertexBuffers: []driver.GPUVertexBufferLayout{{
					Stride: 7 * 4,
					Attributes: []driver.GPUVertexAttribute{
						{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x3},
						{Location: 1, Offset: 3 * 4, Format: driver.GPUVertexFloat32x4},
					},
				}},
				Primitive: driver.GPUPrimitiveState{
					Topology:  driver.GPUTopologyTriangleList,
					FrontFace: driver.GPUFrontCCW,
					CullMode:  driver.GPUCullBack,
				},
				DepthStencil: &driver.GPUDepthStencilState{
					Format:       driver.GPUFormatDepth24Plus,
					DepthWrite:   true,
					DepthCompare: driver.GPUCompareLess,
				},
				Targets:     []driver.GPUColorTargetState{{Format: driver.GPUFormatBGRA8Unorm, WriteMask: driver.GPUColorWriteAll}},
				SampleCount: 1,
			}},
		},
		Passes: []driver.GPUPass{{
			ColorAttachments: []driver.GPUColorAttachment{{
				LoadOp:     driver.GPULoadClear,
				StoreOp:    driver.GPUStoreStore,
				ClearColor: driver.RGB(0x10141c),
			}},
			DepthStencil: &driver.GPUDepthStencilAttachment{
				DepthLoadOp:  driver.GPULoadClear,
				DepthStoreOp: driver.GPUStoreStore,
				ClearDepth:   1.0,
			},
			Commands: []driver.GPUCommand{
				{Kind: driver.GPUCmdSetPipeline, ID: 6},
				{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 5},
				{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 1},
				{Kind: driver.GPUCmdSetIndexBuffer, ID: 2, IndexFormat: driver.GPUIndexUint16},
				{Kind: driver.GPUCmdDrawIndexed, U0: 12},
			},
		}},
	}
	readFrame := driver.GPUFrame{
		Ops: []driver.GPUResourceOp{{
			Kind:            driver.GPUOpReadTexture,
			ReadbackID:      1,
			ReadbackSurface: true,
		}},
	}

	// Render with Metal on a locked OS thread with an autorelease pool.
	var metalPix []byte
	withPool(func() {
		ctx, err := newGPUContext(w, h, cfg)
		if err != nil {
			t.Fatalf("newGPUContext: %v", err)
		}
		defer func() {
			ctx.close()
			b, tx, sm, pp := ctx.resourceCounts()
			if b != 0 || tx != 0 || sm != 0 || pp != 0 {
				t.Errorf("Metal resources leaked after close: buffers=%d textures=%d samplers=%d pipelines=%d", b, tx, sm, pp)
			}
		}()

		if _, code, err := ctx.submit(setupFrame, 0); err != nil {
			t.Fatalf("Metal submit setup: code=%d err=%v", code, err)
		}
		rbs, code, err := ctx.submit(readFrame, 0)
		if err != nil {
			t.Fatalf("Metal submit read: code=%d err=%v", code, err)
		}
		if len(rbs) != 1 || rbs[0].errMsg != "" {
			t.Fatalf("Metal readback failed: %+v", rbs)
		}
		metalPix = rbs[0].pixels
	})

	// Render with software reference.
	soft := driver.NewGPUSoftwareDevice(w, h, cfg)
	if _, err := soft.Submit(setupFrame); err != nil {
		t.Fatalf("software submit setup: %v", err)
	}
	softRBs, err := soft.Submit(readFrame)
	if err != nil || len(softRBs) != 1 {
		t.Fatalf("software submit read: %v (%+v)", err, softRBs)
	}
	softPix := softRBs[0].Pixels

	if len(metalPix) != w*h*4 || len(softPix) != w*h*4 {
		t.Fatalf("unexpected pixel lengths: metal=%d soft=%d", len(metalPix), len(softPix))
	}

	at := func(pix []byte, x, y int) [4]byte {
		i := (y*w + x) * 4
		return [4]byte{pix[i], pix[i+1], pix[i+2], pix[i+3]}
	}

	// Corner clear color (16, 20, 28, 255).
	if got := at(metalPix, 2, 2); got != [4]byte{16, 20, 28, 255} {
		t.Errorf("Metal corner (2,2) = %v, want [16 20 28 255]", got)
	}
	// Center pixel must be red (depth test rejects far blue quad).
	if got := at(metalPix, 60, 60); got[0] < 250 || got[1] > 5 || got[2] > 5 || got[3] != 255 {
		t.Errorf("Metal center (60,60) = %v, want red", got)
	}
	// Outer rim pixel must be blue from the larger far quad behind the red quad.
	if got := at(metalPix, 36, 60); got[2] < 250 || got[0] > 5 || got[1] > 5 || got[3] != 255 {
		t.Errorf("Metal rim (36,60) = %v, want blue", got)
	}

	// Compare Metal vs software rasterizer across the full 120x120 image.
	// Allow tiny rasterization edge differences along quad boundaries (<2% of pixels).
	var mismatch int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mp, sp := at(metalPix, x, y), at(softPix, x, y)
			if absDiff(mp[0], sp[0]) > 4 || absDiff(mp[1], sp[1]) > 4 || absDiff(mp[2], sp[2]) > 4 || absDiff(mp[3], sp[3]) > 4 {
				mismatch++
			}
		}
	}
	if maxAllowed := (w * h) / 50; mismatch > maxAllowed {
		t.Errorf("Metal vs software mismatches = %d / %d pixels (max allowed %d)", mismatch, w*h, maxAllowed)
	}
}

func TestMetalTexturedLit3DMeshAndOffscreenTarget(t *testing.T) {
	if !GPUAvailable() {
		t.Skip("no Metal device")
	}
	litProg, err := wgsl.CompilePipeline(testTexturedLit3DWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline lit: %v", err)
	}
	litIR := litProg.IR
	colorProg, err := wgsl.CompilePipeline(testColor3DWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline color: %v", err)
	}
	colorIR := colorProg.IR

	const w, h = 64, 64
	cfg := driver.GPUViewConfig{
		ColorFormat: driver.GPUFormatBGRA8Unorm,
		DepthFormat: driver.GPUFormatDepth32Float,
		SampleCount: 1,
	}
	withPool(func() {
		ctx, err := newGPUContext(w, h, cfg)
		if err != nil {
			t.Fatalf("newGPUContext: %v", err)
		}
		defer ctx.close()

		// 2x2 RGBA texture: left column green (0,255,0), right column yellow (255,255,0).
		texData := []byte{
			0, 255, 0, 255, 255, 255, 0, 255,
			0, 255, 0, 255, 255, 255, 0, 255,
		}
		ident := [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
		// Light pointing straight from +Z at 50% angle -> normal=(0,0,1), lightDir=(0,0,1) => ndotl=1.0,
		// or tilted lightDir=(0, 0.8, 0.6) => ndotl = 0.6.
		sceneUniform := append(packF32(ident[:]...), packF32(0, 0.8, 0.6, 0)...)

		// Fullscreen quad with pos(3) + normal(3) + uv(2) = 8 float32 = 32 bytes/vertex.
		quadVerts := packF32(
			-1, -1, 0.5, 0, 0, 1, 0, 1,
			1, -1, 0.5, 0, 0, 1, 1, 1,
			1, 1, 0.5, 0, 0, 1, 1, 0,
			-1, -1, 0.5, 0, 0, 1, 0, 1,
			1, 1, 0.5, 0, 0, 1, 1, 0,
			-1, 1, 0.5, 0, 0, 1, 0, 0,
		)

		// Staging uniform buffer copied via GPUCopyBufferToBuffer to verify blit copy!
		setup := driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpCreateBuffer, ID: 10, Buffer: &driver.GPUBufferDesc{Size: uint64(len(quadVerts)), Usage: driver.GPUBufferVertex, Data: quadVerts}},
				{Kind: driver.GPUOpCreateBuffer, ID: 11, Buffer: &driver.GPUBufferDesc{Size: uint64(len(sceneUniform)), Usage: driver.GPUBufferCopySrc, Data: sceneUniform}},
				{Kind: driver.GPUOpCreateBuffer, ID: 12, Buffer: &driver.GPUBufferDesc{Size: uint64(len(sceneUniform)), Usage: driver.GPUBufferUniform | driver.GPUBufferCopyDst}},
				// Texture source + copy target to exercise CopyTextureToTexture
				{Kind: driver.GPUOpCreateTexture, ID: 20, Texture: &driver.GPUTextureDesc{
					Width: 2, Height: 2, Layers: 1, MipLevels: 1, SampleCount: 1,
					Format: driver.GPUFormatRGBA8Unorm, Usage: driver.GPUTextureSampled | driver.GPUTextureCopySrc | driver.GPUTextureCopyDst,
				}},
				{Kind: driver.GPUOpWriteTexture, ID: 20, Data: texData, TextureDst: driver.GPUTextureCopy{Texture: 20}, DataLayout: driver.GPUDataLayout{BytesPerRow: 8, RowsPerImage: 2}},
				{Kind: driver.GPUOpCreateTexture, ID: 21, Texture: &driver.GPUTextureDesc{
					Width: 2, Height: 2, Layers: 1, MipLevels: 1, SampleCount: 1,
					Format: driver.GPUFormatRGBA8Unorm, Usage: driver.GPUTextureSampled | driver.GPUTextureCopyDst,
				}},
				// Offscreen render target (32x32 RGBA8Unorm)
				{Kind: driver.GPUOpCreateTexture, ID: 22, Texture: &driver.GPUTextureDesc{
					Width: 32, Height: 32, Layers: 1, MipLevels: 1, SampleCount: 1,
					Format: driver.GPUFormatRGBA8Unorm, Usage: driver.GPUTextureRenderAttachment | driver.GPUTextureSampled | driver.GPUTextureCopySrc,
				}},
				{Kind: driver.GPUOpCreateSampler, ID: 30, Sampler: &driver.GPUSamplerDesc{
					MinFilter: driver.GPUFilterNearest, MagFilter: driver.GPUFilterNearest,
					AddressU: driver.GPUAddressClampToEdge, AddressV: driver.GPUAddressClampToEdge,
				}},
				{Kind: driver.GPUOpCreateBindGroupLayout, ID: 40, BindGroupLayout: &driver.GPUBindGroupLayoutDesc{
					Entries: []driver.GPUBindGroupLayoutEntry{
						{Binding: 0, Visibility: driver.GPUStageAll, Type: driver.GPUBindingUniform},
						{Binding: 1, Visibility: driver.GPUStageFragment, Type: driver.GPUBindingTexture},
						{Binding: 2, Visibility: driver.GPUStageFragment, Type: driver.GPUBindingSampler},
					},
				}},
				{Kind: driver.GPUOpCreateBindGroup, ID: 41, BindGroup: &driver.GPUBindGroupDesc{
					Layout: 40,
					Entries: []driver.GPUBindGroupEntry{
						{Binding: 0, Buffer: 12, Size: uint64(len(sceneUniform))},
						{Binding: 1, Texture: 21},
						{Binding: 2, Sampler: 30},
					},
				}},
				{Kind: driver.GPUOpCreatePipeline, ID: 50, Pipeline: &driver.GPUPipelineDesc{
					Vertex:   driver.GPUShaderStage{IR: litIR, EntryPoint: "vs_main"},
					Fragment: driver.GPUShaderStage{IR: litIR, EntryPoint: "fs_main"},
					Layouts:  []driver.GPUID{40},
					VertexBuffers: []driver.GPUVertexBufferLayout{{
						Stride: 8 * 4,
						Attributes: []driver.GPUVertexAttribute{
							{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x3},
							{Location: 1, Offset: 3 * 4, Format: driver.GPUVertexFloat32x3},
							{Location: 2, Offset: 6 * 4, Format: driver.GPUVertexFloat32x2},
						},
					}},
					Primitive: driver.GPUPrimitiveState{Topology: driver.GPUTopologyTriangleList, FrontFace: driver.GPUFrontCCW, CullMode: driver.GPUCullNone},
					Targets:   []driver.GPUColorTargetState{{Format: driver.GPUFormatRGBA8Unorm, WriteMask: driver.GPUColorWriteAll}},
				}},
				// Alpha-blended pipeline for pass 2 over surface (testing viewport/scissor + blending).
				{Kind: driver.GPUOpCreateBuffer, ID: 13, Buffer: &driver.GPUBufferDesc{
					Size: 64, Usage: driver.GPUBufferUniform, Data: packF32(ident[:]...),
				}},
				{Kind: driver.GPUOpCreateBuffer, ID: 14, Buffer: &driver.GPUBufferDesc{
					Usage: driver.GPUBufferVertex,
					Data: packF32(
						-1, -1, 0.2, 0, 0, 1, 0.5,
						1, -1, 0.2, 0, 0, 1, 0.5,
						1, 1, 0.2, 0, 0, 1, 0.5,
						-1, -1, 0.2, 0, 0, 1, 0.5,
						1, 1, 0.2, 0, 0, 1, 0.5,
						-1, 1, 0.2, 0, 0, 1, 0.5,
					),
				}},
				{Kind: driver.GPUOpCreateBindGroupLayout, ID: 42, BindGroupLayout: &driver.GPUBindGroupLayoutDesc{
					Entries: []driver.GPUBindGroupLayoutEntry{{Binding: 0, Visibility: driver.GPUStageVertex, Type: driver.GPUBindingUniform}},
				}},
				{Kind: driver.GPUOpCreateBindGroup, ID: 43, BindGroup: &driver.GPUBindGroupDesc{
					Layout: 42, Entries: []driver.GPUBindGroupEntry{{Binding: 0, Buffer: 13, Size: 64}},
				}},
				{Kind: driver.GPUOpCreatePipeline, ID: 51, Pipeline: &driver.GPUPipelineDesc{
					Vertex:   driver.GPUShaderStage{IR: colorIR, EntryPoint: "vs_main"},
					Fragment: driver.GPUShaderStage{IR: colorIR, EntryPoint: "fs_main"},
					Layouts:  []driver.GPUID{42},
					VertexBuffers: []driver.GPUVertexBufferLayout{{
						Stride: 7 * 4,
						Attributes: []driver.GPUVertexAttribute{
							{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x3},
							{Location: 1, Offset: 3 * 4, Format: driver.GPUVertexFloat32x4},
						},
					}},
					Targets: []driver.GPUColorTargetState{{
						Format:    driver.GPUFormatBGRA8Unorm,
						WriteMask: driver.GPUColorWriteAll,
						Blend: &driver.GPUBlendState{
							Color: driver.GPUBlendComponent{SrcFactor: driver.GPUBlendSrcAlpha, DstFactor: driver.GPUBlendOneMinusSrcAlpha, Operation: driver.GPUBlendAdd},
							Alpha: driver.GPUBlendComponent{SrcFactor: driver.GPUBlendOne, DstFactor: driver.GPUBlendOneMinusSrcAlpha, Operation: driver.GPUBlendAdd},
						},
					}},
				}},
			},
			Copies: []driver.GPUCopyOp{
				{Kind: driver.GPUCopyBufferToBuffer, SrcID: 11, DstID: 12, Size: uint64(len(sceneUniform))},
				{Kind: driver.GPUCopyTextureToTexture, SrcTex: driver.GPUTextureCopy{Texture: 20}, DstTex: driver.GPUTextureCopy{Texture: 21}, Width: 2, Height: 2, Layers: 1},
			},
			Passes: []driver.GPUPass{
				// Pass 1: Render lit textured mesh into offscreen 32x32 target (ID 22).
				{
					ColorAttachments: []driver.GPUColorAttachment{{
						Texture:    22,
						LoadOp:     driver.GPULoadClear,
						StoreOp:    driver.GPUStoreStore,
						ClearColor: driver.RGB(0x000000),
					}},
					Commands: []driver.GPUCommand{
						{Kind: driver.GPUCmdSetPipeline, ID: 50},
						{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 41},
						{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 10},
						{Kind: driver.GPUCmdDraw, U0: 6},
					},
				},
				// Pass 2: Clear surface to red, then alpha-blend 50% blue only inside scissor [16..48, 16..48].
				{
					ColorAttachments: []driver.GPUColorAttachment{{
						LoadOp:     driver.GPULoadClear,
						StoreOp:    driver.GPUStoreStore,
						ClearColor: driver.RGB(0xff0000),
					}},
					Commands: []driver.GPUCommand{
						{Kind: driver.GPUCmdSetViewport, F0: 0, F1: 0, F2: 64, F3: 64, F4: 0, F5: 1},
						{Kind: driver.GPUCmdSetScissor, I0: 16, I1: 16, I2: 32, I3: 32},
						{Kind: driver.GPUCmdSetPipeline, ID: 51},
						{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 43},
						{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 14},
						{Kind: driver.GPUCmdDraw, U0: 6},
					},
				},
			},
		}

		if _, code, err := ctx.submit(setup, 0); err != nil {
			t.Fatalf("Metal submit: code=%d err=%v", code, err)
		}

		// Read back offscreen render target (ID 22) and surface.
		rbs, code, err := ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpReadTexture, ID: 22, ReadbackID: 1, ReadbackRegion: driver.GPUTextureRegion{Width: 32, Height: 32}},
				{Kind: driver.GPUOpReadTexture, ReadbackID: 2, ReadbackSurface: true},
			},
		}, 0)
		if err != nil {
			t.Fatalf("Metal readback submit: code=%d err=%v", code, err)
		}
		if len(rbs) != 2 || rbs[0].errMsg != "" || rbs[1].errMsg != "" {
			t.Fatalf("unexpected readbacks: %+v", rbs)
		}

		// Offscreen target: left half lit green (0, ~153, 0, 255), right half lit yellow (~153, ~153, 0, 255).
		rtPix := rbs[0].pixels
		left := rtPix[(16*32+8)*4:]
		if left[0] > 5 || left[1] < 145 || left[1] > 161 || left[2] > 5 || left[3] != 255 {
			t.Errorf("offscreen left lit green = %v, want ~(0, 153, 0, 255)", left[:4])
		}
		right := rtPix[(16*32+24)*4:]
		if right[0] < 145 || right[0] > 161 || right[1] < 145 || right[1] > 161 || right[2] > 5 || right[3] != 255 {
			t.Errorf("offscreen right lit yellow = %v, want ~(153, 153, 0, 255)", right[:4])
		}

		// Surface: outside scissor (4,4) must stay pure red (255,0,0,255); inside scissor (32,32) must be blended (~128, 0, ~128, 255).
		surfPix := rbs[1].pixels
		outScissor := surfPix[(4*w+4)*4:]
		if outScissor[0] < 250 || outScissor[1] > 5 || outScissor[2] > 5 {
			t.Errorf("outside scissor pixel = %v, want red", outScissor[:4])
		}
		inScissor := surfPix[(32*w+32)*4:]
		if inScissor[0] < 120 || inScissor[0] > 136 || inScissor[1] > 5 || inScissor[2] < 120 || inScissor[2] > 136 {
			t.Errorf("inside scissor blended pixel = %v, want ~(128, 0, 128, 255)", inScissor[:4])
		}
	})
}

func TestMetalStress100kTrianglesAndInstancing(t *testing.T) {
	if !GPUAvailable() {
		t.Skip("no Metal device")
	}
	colorProg, err := wgsl.CompilePipeline(testColor3DWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline color: %v", err)
	}
	colorIR := colorProg.IR
	instProg, err := wgsl.CompilePipeline(testInstancedWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline instanced: %v", err)
	}
	instIR := instProg.IR

	const w, h = 128, 128
	cfg := driver.GPUViewConfig{
		ColorFormat: driver.GPUFormatBGRA8Unorm,
		DepthFormat: driver.GPUFormatDepth32Float,
		SampleCount: 1,
	}
	withPool(func() {
		ctx, err := newGPUContext(w, h, cfg)
		if err != nil {
			t.Fatalf("newGPUContext: %v", err)
		}
		defer ctx.close()

		// Build a 100,000-triangle indexed mesh (250x200 grid = 50,000 quads = 100,000 triangles!).
		const gx, gy = 250, 200
		const triCount = gx * gy * 2
		verts := make([]byte, (gx+1)*(gy+1)*7*4)
		off := 0
		for j := 0; j <= gy; j++ {
			y := -0.8 + 1.6*float32(j)/float32(gy)
			for i := 0; i <= gx; i++ {
				x := -0.8 + 1.6*float32(i)/float32(gx)
				binary.LittleEndian.PutUint32(verts[off+0:], math.Float32bits(x))
				binary.LittleEndian.PutUint32(verts[off+4:], math.Float32bits(y))
				binary.LittleEndian.PutUint32(verts[off+8:], math.Float32bits(0.5))
				binary.LittleEndian.PutUint32(verts[off+12:], math.Float32bits(0.0))
				binary.LittleEndian.PutUint32(verts[off+16:], math.Float32bits(1.0))
				binary.LittleEndian.PutUint32(verts[off+20:], math.Float32bits(0.2))
				binary.LittleEndian.PutUint32(verts[off+24:], math.Float32bits(1.0))
				off += 28
			}
		}
		indices := make([]uint32, 0, triCount*3)
		row := uint32(gx + 1)
		for j := uint32(0); j < gy; j++ {
			for i := uint32(0); i < gx; i++ {
				v00 := j*row + i
				v10 := v00 + 1
				v01 := v00 + row
				v11 := v01 + 1
				indices = append(indices, v00, v10, v11, v00, v11, v01)
			}
		}
		idxBytes := packU32(indices...)
		ident := [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

		start := time.Now()
		_, code, err := ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpCreateBuffer, ID: 1, Buffer: &driver.GPUBufferDesc{Size: uint64(len(verts)), Usage: driver.GPUBufferVertex, Data: verts}},
				{Kind: driver.GPUOpCreateBuffer, ID: 2, Buffer: &driver.GPUBufferDesc{Size: uint64(len(idxBytes)), Usage: driver.GPUBufferIndex, Data: idxBytes}},
				{Kind: driver.GPUOpCreateBuffer, ID: 3, Buffer: &driver.GPUBufferDesc{Size: 64, Usage: driver.GPUBufferUniform, Data: packF32(ident[:]...)}},
				{Kind: driver.GPUOpCreateBindGroupLayout, ID: 4, BindGroupLayout: &driver.GPUBindGroupLayoutDesc{
					Entries: []driver.GPUBindGroupLayoutEntry{{Binding: 0, Visibility: driver.GPUStageVertex, Type: driver.GPUBindingUniform}},
				}},
				{Kind: driver.GPUOpCreateBindGroup, ID: 5, BindGroup: &driver.GPUBindGroupDesc{
					Layout: 4, Entries: []driver.GPUBindGroupEntry{{Binding: 0, Buffer: 3, Size: 64}},
				}},
				{Kind: driver.GPUOpCreatePipeline, ID: 6, Pipeline: &driver.GPUPipelineDesc{
					Vertex:   driver.GPUShaderStage{IR: colorIR, EntryPoint: "vs_main"},
					Fragment: driver.GPUShaderStage{IR: colorIR, EntryPoint: "fs_main"},
					Layouts:  []driver.GPUID{4},
					VertexBuffers: []driver.GPUVertexBufferLayout{{
						Stride: 7 * 4,
						Attributes: []driver.GPUVertexAttribute{
							{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x3},
							{Location: 1, Offset: 3 * 4, Format: driver.GPUVertexFloat32x4},
						},
					}},
					Primitive: driver.GPUPrimitiveState{Topology: driver.GPUTopologyTriangleList, FrontFace: driver.GPUFrontCCW, CullMode: driver.GPUCullBack},
					DepthStencil: &driver.GPUDepthStencilState{
						Format: driver.GPUFormatDepth32Float, DepthWrite: true, DepthCompare: driver.GPUCompareLess,
					},
					Targets: []driver.GPUColorTargetState{{Format: driver.GPUFormatBGRA8Unorm, WriteMask: driver.GPUColorWriteAll}},
				}},
			},
			Passes: []driver.GPUPass{{
				ColorAttachments: []driver.GPUColorAttachment{{LoadOp: driver.GPULoadClear, StoreOp: driver.GPUStoreStore, ClearColor: driver.RGB(0x000000)}},
				DepthStencil:     &driver.GPUDepthStencilAttachment{DepthLoadOp: driver.GPULoadClear, DepthStoreOp: driver.GPUStoreStore, ClearDepth: 1.0},
				Commands: []driver.GPUCommand{
					{Kind: driver.GPUCmdSetPipeline, ID: 6},
					{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 5},
					{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 1},
					{Kind: driver.GPUCmdSetIndexBuffer, ID: 2, IndexFormat: driver.GPUIndexUint32},
					{Kind: driver.GPUCmdDrawIndexed, U0: uint32(len(indices))},
				},
			}},
		}, 0)
		if err != nil {
			t.Fatalf("100k-tri Metal submit: code=%d err=%v", code, err)
		}
		rbs, _, err := ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 1, ReadbackSurface: true}},
		}, 0)
		if err != nil || len(rbs) != 1 || rbs[0].errMsg != "" {
			t.Fatalf("100k-tri readback failed: err=%v rbs=%+v", err, rbs)
		}
		t.Logf("Metal rendered %d triangles (%d indices) + readback in %v", triCount, len(indices), time.Since(start))

		center := rbs[0].pixels[(64*w+64)*4:]
		if center[0] > 5 || center[1] < 250 || center[2] < 45 || center[2] > 56 {
			t.Errorf("100k-tri center pixel = %v, want ~(0, 255, 51, 255)", center[:4])
		}

		// Also verify instanced draw with per-instance vertex buffer step mode.
		unitQuad := packF32(-0.2, -0.2, 0.2, -0.2, 0.2, 0.2, -0.2, 0.2)
		quadIdx := packU16(0, 1, 2, 0, 2, 3)
		// Instance 0 on left (-0.5, 0) red; Instance 1 on right (+0.5, 0) blue.
		instData := packF32(
			-0.5, 0.0, 1, 0, 0, 1,
			0.5, 0.0, 0, 0, 1, 1,
		)
		_, code, err = ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpCreateBuffer, ID: 20, Buffer: &driver.GPUBufferDesc{Usage: driver.GPUBufferVertex, Data: unitQuad}},
				{Kind: driver.GPUOpCreateBuffer, ID: 21, Buffer: &driver.GPUBufferDesc{Usage: driver.GPUBufferVertex, Data: instData}},
				{Kind: driver.GPUOpCreateBuffer, ID: 22, Buffer: &driver.GPUBufferDesc{Usage: driver.GPUBufferIndex, Data: quadIdx}},
				{Kind: driver.GPUOpCreatePipeline, ID: 23, Pipeline: &driver.GPUPipelineDesc{
					Vertex:   driver.GPUShaderStage{IR: instIR, EntryPoint: "vs_main"},
					Fragment: driver.GPUShaderStage{IR: instIR, EntryPoint: "fs_main"},
					VertexBuffers: []driver.GPUVertexBufferLayout{
						{
							Stride:     2 * 4,
							StepMode:   driver.GPUStepVertex,
							Attributes: []driver.GPUVertexAttribute{{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x2}},
						},
						{
							Stride:   6 * 4,
							StepMode: driver.GPUStepInstance,
							Attributes: []driver.GPUVertexAttribute{
								{Location: 1, Offset: 0, Format: driver.GPUVertexFloat32x2},
								{Location: 2, Offset: 2 * 4, Format: driver.GPUVertexFloat32x4},
							},
						},
					},
					Targets: []driver.GPUColorTargetState{{Format: driver.GPUFormatBGRA8Unorm, WriteMask: driver.GPUColorWriteAll}},
				}},
			},
			Passes: []driver.GPUPass{{
				ColorAttachments: []driver.GPUColorAttachment{{LoadOp: driver.GPULoadClear, StoreOp: driver.GPUStoreStore, ClearColor: driver.RGB(0x000000)}},
				Commands: []driver.GPUCommand{
					{Kind: driver.GPUCmdSetPipeline, ID: 23},
					{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 20},
					{Kind: driver.GPUCmdSetVertexBuffer, Slot: 1, ID: 21},
					{Kind: driver.GPUCmdSetIndexBuffer, ID: 22, IndexFormat: driver.GPUIndexUint16},
					{Kind: driver.GPUCmdDrawIndexedInstanced, U0: 6, U1: 2},
				},
			}},
		}, 0)
		if err != nil {
			t.Fatalf("instanced Metal submit: code=%d err=%v", code, err)
		}
		rbs, _, err = ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 2, ReadbackSurface: true}},
		}, 0)
		if err != nil || len(rbs) != 1 || rbs[0].errMsg != "" {
			t.Fatalf("instanced readback failed: err=%v rbs=%+v", err, rbs)
		}
		leftInst := rbs[0].pixels[(64*w+32)*4:]
		rightInst := rbs[0].pixels[(64*w+96)*4:]
		if leftInst[0] < 250 || leftInst[2] > 5 {
			t.Errorf("instance 0 pixel (32,64) = %v, want red", leftInst[:4])
		}
		if rightInst[2] < 250 || rightInst[0] > 5 {
			t.Errorf("instance 1 pixel (96,64) = %v, want blue", rightInst[:4])
		}
	})
}

func TestMetalGPULayerLifecycleResizeAndTeardown(t *testing.T) {
	if !GPUAvailable() {
		t.Skip("no Metal device")
	}
	events := make(chan driver.GPUEvent, 16)
	gl := NewGPULayer(2.0, func(ev driver.GPUEvent) {
		events <- ev
	})
	if gl == nil {
		t.Fatal("NewGPULayer returned nil on Metal host")
	}

	gl.Configure(driver.GPUViewConfig{
		ColorFormat: driver.GPUFormatBGRA8Unorm,
		DepthFormat: driver.GPUFormatDepth32Float,
		SampleCount: 1,
	})
	gl.Activate(32, 24, 2.0)

	waitEvent := func(want driver.GPUEventKind) driver.GPUEvent {
		t.Helper()
		select {
		case ev := <-events:
			if ev.Kind != want {
				t.Fatalf("got GPUEvent kind %d (%+v), want %d", ev.Kind, ev, want)
			}
			return ev
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for GPUEvent kind %d", want)
			return driver.GPUEvent{}
		}
	}

	ready := waitEvent(driver.GPUEventDeviceReady)
	if ready.Width != 64 || ready.Height != 48 || ready.Generation != 1 || ready.SurfaceFormat != driver.GPUFormatBGRA8Unorm {
		t.Fatalf("unexpected DeviceReady event: %+v", ready)
	}
	if !ready.Features.Depth32Float || !ready.Features.StorageBuffers {
		t.Errorf("expected Depth32Float and StorageBuffers features on Metal, got %+v", ready.Features)
	}

	// Off-commit submission from a separate goroutine (no UI commit).
	gl.Submit(driver.GPUFrame{
		Generation: ready.Generation,
		Ops: []driver.GPUResourceOp{
			{Kind: driver.GPUOpCreateBuffer, ID: 1, Buffer: &driver.GPUBufferDesc{Size: 64, Usage: driver.GPUBufferUniform}},
			{Kind: driver.GPUOpCreateSampler, ID: 2, Sampler: &driver.GPUSamplerDesc{MinFilter: driver.GPUFilterLinear, MagFilter: driver.GPUFilterLinear}},
		},
		Passes: []driver.GPUPass{{
			ColorAttachments: []driver.GPUColorAttachment{{
				LoadOp:     driver.GPULoadClear,
				StoreOp:    driver.GPUStoreStore,
				ClearColor: driver.RGB(0x224466),
			}},
		}},
	})
	gl.Submit(driver.GPUFrame{
		Generation: ready.Generation,
		Ops:        []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 7, ReadbackSurface: true}},
	})

	rb := waitEvent(driver.GPUEventReadback)
	if rb.ReadbackID != 7 || rb.Message != "" || len(rb.Pixels) != 64*48*4 {
		t.Fatalf("unexpected initial readback: id=%d err=%q bytes=%d", rb.ReadbackID, rb.Message, len(rb.Pixels))
	}
	if rb.Pixels[0] != 0x22 || rb.Pixels[1] != 0x44 || rb.Pixels[2] != 0x66 || rb.Pixels[3] != 0xff {
		t.Errorf("initial surface pixel = %x, want 224466ff", rb.Pixels[:4])
	}

	// Resize layer and verify GPUEventResize + resized render target readback.
	gl.Resize(40, 30, 2.0)
	resEv := waitEvent(driver.GPUEventResize)
	if resEv.Width != 80 || resEv.Height != 60 || resEv.Generation != ready.Generation {
		t.Fatalf("unexpected Resize event: %+v", resEv)
	}
	gl.Submit(driver.GPUFrame{
		Generation: ready.Generation,
		Passes: []driver.GPUPass{{
			ColorAttachments: []driver.GPUColorAttachment{{
				LoadOp:     driver.GPULoadClear,
				StoreOp:    driver.GPUStoreStore,
				ClearColor: driver.RGB(0x11aa33),
			}},
		}},
	})
	gl.Submit(driver.GPUFrame{
		Generation: ready.Generation,
		Ops:        []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 8, ReadbackSurface: true}},
	})
	rb2 := waitEvent(driver.GPUEventReadback)
	if rb2.ReadbackID != 8 || rb2.Message != "" || len(rb2.Pixels) != 80*60*4 {
		t.Fatalf("unexpected resized readback: id=%d err=%q bytes=%d", rb2.ReadbackID, rb2.Message, len(rb2.Pixels))
	}
	if rb2.Pixels[0] != 0x11 || rb2.Pixels[1] != 0xaa || rb2.Pixels[2] != 0x33 || rb2.Pixels[3] != 0xff {
		t.Errorf("resized surface pixel = %x, want 11aa33ff", rb2.Pixels[:4])
	}

	// Verify resources exist before Release and are completely released on teardown.
	gl.ctxMu.Lock()
	ctx := gl.ctx
	gl.ctxMu.Unlock()
	if ctx == nil {
		t.Fatal("expected active gpuContext before Release")
	}

	gl.Release()
	gl.waitClosed()

	if gl.Layer != 0 {
		t.Error("expected gl.Layer == 0 after Release")
	}
	gl.ctxMu.Lock()
	afterCtx := gl.ctx
	gl.ctxMu.Unlock()
	if afterCtx != nil {
		t.Error("expected gl.ctx == nil after teardown")
	}
	b, tx, sm, pp := ctx.resourceCounts()
	if b != 0 || tx != 0 || sm != 0 || pp != 0 {
		t.Errorf("resources leaked after GPULayer.Release: buffers=%d textures=%d samplers=%d pipelines=%d", b, tx, sm, pp)
	}
}

func TestMetalMSAA4xUniformUpdateAndExplicitDestroy(t *testing.T) {
	if !GPUAvailable() {
		t.Skip("no Metal device")
	}
	prog, err := wgsl.CompilePipeline(testColor3DWGSL)
	if err != nil {
		t.Fatalf("CompilePipeline: %v", err)
	}
	ir := prog.IR

	const w, h = 64, 64
	cfg := driver.GPUViewConfig{
		ColorFormat: driver.GPUFormatBGRA8Unorm,
		DepthFormat: driver.GPUFormatDepth32Float,
		SampleCount: 4,
	}

	withPool(func() {
		ctx, err := newGPUContext(w, h, cfg)
		if err != nil {
			t.Fatalf("newGPUContext 4x MSAA: %v", err)
		}
		defer ctx.close()

		// Fullscreen green triangle, initially scaled by identity, then moved offscreen via WriteBuffer.
		verts := packF32(
			-1, -1, 0.5, 0, 1, 0, 1,
			1, -1, 0.5, 0, 1, 0, 1,
			0, 1, 0.5, 0, 1, 0, 1,
		)
		ident := [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
		shifted := testTranslate(10, 0, 0) // moves triangle completely offscreen

		_, code, err := ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpCreateBuffer, ID: 1, Buffer: &driver.GPUBufferDesc{Usage: driver.GPUBufferVertex, Data: verts}},
				{Kind: driver.GPUOpCreateBuffer, ID: 2, Buffer: &driver.GPUBufferDesc{Size: 64, Usage: driver.GPUBufferUniform | driver.GPUBufferCopyDst, Data: packF32(ident[:]...)}},
				{Kind: driver.GPUOpCreateBindGroupLayout, ID: 3, BindGroupLayout: &driver.GPUBindGroupLayoutDesc{
					Entries: []driver.GPUBindGroupLayoutEntry{{Binding: 0, Visibility: driver.GPUStageVertex, Type: driver.GPUBindingUniform}},
				}},
				{Kind: driver.GPUOpCreateBindGroup, ID: 4, BindGroup: &driver.GPUBindGroupDesc{
					Layout: 3, Entries: []driver.GPUBindGroupEntry{{Binding: 0, Buffer: 2, Size: 64}},
				}},
				{Kind: driver.GPUOpCreatePipeline, ID: 5, Pipeline: &driver.GPUPipelineDesc{
					Vertex:   driver.GPUShaderStage{IR: ir, EntryPoint: "vs_main"},
					Fragment: driver.GPUShaderStage{IR: ir, EntryPoint: "fs_main"},
					Layouts:  []driver.GPUID{3},
					VertexBuffers: []driver.GPUVertexBufferLayout{{
						Stride: 7 * 4,
						Attributes: []driver.GPUVertexAttribute{
							{Location: 0, Offset: 0, Format: driver.GPUVertexFloat32x3},
							{Location: 1, Offset: 3 * 4, Format: driver.GPUVertexFloat32x4},
						},
					}},
					DepthStencil: &driver.GPUDepthStencilState{Format: driver.GPUFormatDepth32Float, DepthWrite: true, DepthCompare: driver.GPUCompareLess},
					Targets:      []driver.GPUColorTargetState{{Format: driver.GPUFormatBGRA8Unorm, WriteMask: driver.GPUColorWriteAll}},
					SampleCount:  4,
				}},
			},
			Passes: []driver.GPUPass{{
				ColorAttachments: []driver.GPUColorAttachment{{LoadOp: driver.GPULoadClear, StoreOp: driver.GPUStoreStore, ClearColor: driver.RGB(0x000000)}},
				DepthStencil:     &driver.GPUDepthStencilAttachment{DepthLoadOp: driver.GPULoadClear, DepthStoreOp: driver.GPUStoreDiscard, ClearDepth: 1.0},
				Commands: []driver.GPUCommand{
					{Kind: driver.GPUCmdSetPipeline, ID: 5},
					{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 4},
					{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 1},
					{Kind: driver.GPUCmdDraw, U0: 3},
				},
			}},
		}, 0)
		if err != nil {
			t.Fatalf("MSAA frame 1 failed: code=%d err=%v", code, err)
		}
		rbs, _, err := ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 1, ReadbackSurface: true}},
		}, 0)
		if err != nil || len(rbs) != 1 || rbs[0].errMsg != "" {
			t.Fatalf("MSAA readback 1 failed: err=%v rbs=%+v", err, rbs)
		}
		if c := rbs[0].pixels[(40*w+32)*4:]; c[1] < 250 || c[0] > 5 {
			t.Errorf("MSAA frame 1 center = %v, want green", c[:4])
		}

		// Update uniform via WriteBuffer (waitLastCB + copy), re-render, and verify triangle moved offscreen.
		_, code, err = ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpWriteBuffer, ID: 2, Offset: 0, Data: packF32(shifted[:]...)},
			},
			Passes: []driver.GPUPass{{
				ColorAttachments: []driver.GPUColorAttachment{{LoadOp: driver.GPULoadClear, StoreOp: driver.GPUStoreStore, ClearColor: driver.RGB(0x050a14)}},
				DepthStencil:     &driver.GPUDepthStencilAttachment{DepthLoadOp: driver.GPULoadClear, DepthStoreOp: driver.GPUStoreDiscard, ClearDepth: 1.0},
				Commands: []driver.GPUCommand{
					{Kind: driver.GPUCmdSetPipeline, ID: 5},
					{Kind: driver.GPUCmdSetBindGroup, Slot: 0, ID: 4},
					{Kind: driver.GPUCmdSetVertexBuffer, Slot: 0, ID: 1},
					{Kind: driver.GPUCmdDraw, U0: 3},
				},
			}},
		}, 0)
		if err != nil {
			t.Fatalf("MSAA frame 2 failed: code=%d err=%v", code, err)
		}
		rbs, _, err = ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{{Kind: driver.GPUOpReadTexture, ReadbackID: 2, ReadbackSurface: true}},
		}, 0)
		if err != nil || len(rbs) != 1 || rbs[0].errMsg != "" {
			t.Fatalf("MSAA readback 2 failed: err=%v rbs=%+v", err, rbs)
		}
		if c := rbs[0].pixels[(40*w+32)*4:]; c[0] != 5 || c[1] != 10 || c[2] != 20 || c[3] != 255 {
			t.Errorf("MSAA frame 2 center = %v, want clear color [5 10 20 255]", c[:4])
		}

		// Explicitly destroy all resources and verify counts drop to zero.
		_, code, err = ctx.submit(driver.GPUFrame{
			Ops: []driver.GPUResourceOp{
				{Kind: driver.GPUOpDestroyPipeline, ID: 5},
				{Kind: driver.GPUOpDestroyBindGroup, ID: 4},
				{Kind: driver.GPUOpDestroyBindGroupLayout, ID: 3},
				{Kind: driver.GPUOpDestroyBuffer, ID: 2},
				{Kind: driver.GPUOpDestroyBuffer, ID: 1},
			},
		}, 0)
		if err != nil {
			t.Fatalf("explicit destroy failed: code=%d err=%v", code, err)
		}
		b, tx, sm, pp := ctx.resourceCounts()
		if b != 0 || tx != 0 || sm != 0 || pp != 0 {
			t.Errorf("expected 0 resources after explicit destroy, got buffers=%d textures=%d samplers=%d pipelines=%d", b, tx, sm, pp)
		}
	})
}

func absDiff(a, b byte) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
