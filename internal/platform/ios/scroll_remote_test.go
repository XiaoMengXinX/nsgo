//go:build ios && nsgotest

package ios

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/internal/driver"
	"github.com/nsgo-dev/nsgo/internal/platform/apple"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestTVRemoteSurfaceScrollsUnfocusableContent(t *testing.T) { testTVRemoteSurfaceScroll(t, false) }
func TestTVRemoteSurfaceScrollsHorizontalContent(t *testing.T)  { testTVRemoteSurfaceScroll(t, true) }

func testTVRemoteSurfaceScroll(t *testing.T, horizontal bool) {
	t.Helper()
	if os.Getenv("NSGO_TEST_TARGET") != "tvos_simulator" {
		t.Skip("requires tvOS simulator indirect input")
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		children := make([]core.Node, 60)
		for i := range children {
			style := core.Style{Height: core.Pt(45)}
			if horizontal {
				style.Width, style.Height = core.Pt(100), core.Pt(180)
			}
			children[i] = core.Text{Content: fmt.Sprintf("Long reading content %d", i), Style: style}
		}
		return core.Scroll{Horizontal: horizontal, TestID: "reading", Style: core.Style{Width: core.Pt(600), Height: core.Pt(320)}, Children: children}
	})
	id := app.Find("id:reading").ID
	var native, pan, window objc.ID
	var touchTypes []int
	onMainSync(func() {
		native = runner.views[id].native
		pan, window = native.Send(selPanRecognizer), native.Send(sel("window"))
		apple.ForEach(pan.Send(sel("allowedTouchTypes")), func(n objc.ID) { touchTypes = append(touchTypes, objc.Send[int](n, sel("integerValue"))) })
	})
	indirect := false
	for _, typ := range touchTypes {
		indirect = indirect || typ == 1
	}
	if !indirect {
		t.Fatalf("native UIScrollView pan does not accept remote indirect touches: allowedTouchTypes=%v", touchTypes)
	}
	if !onTV() || !runningOnSimulator() {
		t.Fatal("native target is not a tvOS simulator")
	}
	seen := make(map[int]int)
	var previous func(objc.ID, objc.ID)
	onMainSync(func() {
		previous = onWindowEvent
		onWindowEvent = func(w, event objc.ID) {
			if previous != nil {
				previous(w, event)
			}
			if w == window && event != 0 && objc.Send[int](event, sel("type")) == 0 {
				apple.ForEach(event.Send(sel("touchesForWindow:"), w), func(touch objc.ID) { seen[objc.Send[int](touch, sel("type"))]++ })
			}
		}
	})
	defer onMainSync(func() { onWindowEvent = previous })
	var start, end apple.CGPoint
	onMainSync(func() { start = objc.Send[apple.CGPoint](native, selContentOffset) })
	// Deliver the remote surface gesture as native indirect input; this is the same path used by testSurface.Scroll.
	if err := loadHID(); err != nil {
		t.Fatal(err)
	}
	path := []driver.Point{{X: 300, Y: 270}, {X: 300, Y: 90}}
	if horizontal {
		path = []driver.Point{{X: 500, Y: 160}, {X: 100, Y: 160}}
	}
	fingerAlongInput(window, path, true)
	time.Sleep(50 * time.Millisecond)
	var indirectEvents int
	onMainSync(func() { indirectEvents = seen[1] })
	if indirectEvents == 0 {
		t.Fatalf("digitizer input did not produce remote UITouchTypeIndirect: observed=%v", seen)
	}
	app.WaitUntil(3*time.Second, func() bool {
		onMainSync(func() { end = objc.Send[apple.CGPoint](native, selContentOffset) })
		if horizontal {
			return end.X > start.X+20
		}
		return end.Y > start.Y+20
	})
}

func TestTVRemoteSurfaceKeepsButtonFocusNavigation(t *testing.T) {
	if os.Getenv("NSGO_TEST_TARGET") != "tvos_simulator" {
		t.Skip("requires tvOS simulator indirect input")
	}
	var first core.Ref
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		children := make([]core.Node, 20)
		for i := range children {
			var ref *core.Ref
			if i == 0 {
				ref = &first
			}
			children[i] = core.View{Ref: ref, TestID: fmt.Sprintf("row%d", i), OnPress: func(core.PressEvent) {}, Style: core.Style{Width: core.Pt(600), Height: core.Pt(90)}, Children: []core.Node{core.Text{Content: fmt.Sprintf("Row %d", i)}}}
		}
		return core.Scroll{TestID: "buttons", Style: core.Style{Width: core.Pt(600), Height: core.Pt(400)}, Children: children}
	})
	app.Post(first.Focus)
	app.WaitUntil(3*time.Second, func() bool { return app.Find("id:row0").Focused })
	if err := loadHID(); err != nil {
		t.Fatal(err)
	}
	fingerAlongInput(runner.window, []driver.Point{{X: 300, Y: 80}, {X: 300, Y: 260}}, true)
	app.WaitUntil(3*time.Second, func() bool {
		for i := 1; i < 20; i++ {
			if app.Find(fmt.Sprintf("id:row%d", i)).Focused {
				return true
			}
		}
		return false
	})
}
