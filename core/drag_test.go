package core

import (
	"fmt"
	"slices"
	"testing"

	"github.com/nsgo-dev/nsgo/internal/driver"
)

func TestDragThresholdLeavesNormalPressIntact(t *testing.T) {
	var presses, starts, ends int
	var lastEnd DragEndEvent
	h := newHost(t, Size{Width: 200, Height: 200}, func(cx *Scope) Node {
		return View{
			TestID: "item",
			Style:  Style{Width: Pt(100), Height: Pt(40)},
			OnPress: func(PressEvent) {
				presses++
			},
			Drag: &DragSource{
				Value:   "card-1",
				Label:   "Card 1",
				OnStart: func(DragEvent) { starts++ },
				OnEnd:   func(e DragEndEvent) { ends++; lastEnd = e },
			},
		}
	})
	h.commit()
	item := h.find("item")
	if !item.events().Has(driver.EventTouch) {
		t.Fatal("a draggable view does not listen to EventTouch")
	}

	// Movement under DefaultDragThreshold (5pt) does not start a drag and leaves OnPress intact.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 20, Y: 20}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 23, Y: 20}, 16))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 23, Y: 20}, 32))
	h.tree.Dispatch(driver.Event{Kind: driver.EventPress, ID: item.id})
	if starts != 0 || ends != 0 || presses != 1 {
		t.Fatalf("short move: starts=%d ends=%d presses=%d, want 0,0,1", starts, ends, presses)
	}

	// Movement at or past threshold activates the drag and suppresses the trailing pointer press.
	h.tree.Dispatch(driver.Event{Kind: driver.EventPressDown, ID: item.id})
	h.tree.Dispatch(finger(GestureBegan, Point{X: 20, Y: 20}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 26, Y: 20}, 56))
	if starts != 1 {
		t.Fatalf("after 6pt move: starts=%d, want 1", starts)
	}
	if item.states&statePressed != 0 {
		t.Error("source stayed pressed after drag activated")
	}
	h.tree.Dispatch(finger(GestureEnded, Point{X: 26, Y: 20}, 72))
	h.tree.Dispatch(driver.Event{Kind: driver.EventPress, ID: item.id})
	if ends != 1 || lastEnd.Dropped || lastEnd.Cancelled || presses != 1 {
		t.Fatalf("after drag lift: ends=%d lastEnd=%+v presses=%d, want 1,{Dropped:false Cancelled:false},1", ends, lastEnd, presses)
	}

	// A subsequent normal click presses again.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 20, Y: 20}, 80))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 20, Y: 20}, 96))
	h.tree.Dispatch(driver.Event{Kind: driver.EventPress, ID: item.id})
	if presses != 2 {
		t.Fatalf("subsequent click: presses=%d, want 2", presses)
	}

	// Keyboard press is never suppressed even right after a drag.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 20, Y: 20}, 100))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 30, Y: 20}, 116))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 30, Y: 20}, 132))
	h.tree.Dispatch(driver.Event{Kind: driver.EventInputModality, Count: driver.ModalityKeyboard})
	h.tree.Dispatch(driver.Event{Kind: driver.EventPress, ID: item.id})
	if presses != 3 {
		t.Fatalf("keyboard press after drag: presses=%d, want 3", presses)
	}
}

func TestDragLifecycleEnterOverLeaveReenterDropAndActiveDrag(t *testing.T) {
	var events []string
	var activeSeen []string
	h := newHost(t, Size{Width: 300, Height: 200}, func(cx *Scope) Node {
		cx.Effect(func() {
			p, ok := ActiveDrag(cx)
			if !ok {
				activeSeen = append(activeSeen, "none")
				return
			}
			ids, _ := p.Value.([]string)
			activeSeen = append(activeSeen, fmt.Sprintf("%v:%d", ids, p.Count))
		})
		return View{
			Style: Style{Direction: Row, Gap: 20, Padding: All(10)},
			Children: []Node{
				View{
					TestID: "source",
					Style:  Style{Width: Pt(100), Height: Pt(80)},
					Drag: &DragSource{
						Start: func() DragPayload {
							return DragPayload{
								Value: []string{"a", "b"},
								Label: "Selected",
								Count: 2,
							}
						},
						OnStart: func(e DragEvent) {
							events = append(events, fmt.Sprintf("start:%d", e.Payload.Count))
						},
						OnEnd: func(e DragEndEvent) {
							events = append(events, fmt.Sprintf("end:dropped=%v,cancelled=%v", e.Dropped, e.Cancelled))
						},
					},
				},
				View{
					TestID: "target",
					Style:  Style{Width: Pt(120), Height: Pt(80)},
					Drop: &DropTarget{
						Accept: func(p DragPayload) bool {
							_, ok := p.Value.([]string)
							return ok
						},
						OnEnter: func(e DropEvent) {
							events = append(events, fmt.Sprintf("enter:%v@%v", e.Position, e.PagePosition))
						},
						OnOver: func(e DropEvent) {
							events = append(events, fmt.Sprintf("over:%v", e.Position))
						},
						OnLeave: func(e DropEvent) {
							events = append(events, "leave")
						},
						OnDrop: func(e DropEvent) {
							ids := e.Payload.Value.([]string)
							events = append(events, fmt.Sprintf("drop:%v@%v", ids, e.Position))
						},
					},
				},
			},
		}
	})
	h.commit()

	// source is at (10,10)-(110,90); target is at (130,10)-(250,90).
	h.tree.Dispatch(finger(GestureBegan, Point{X: 50, Y: 40}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 60, Y: 40}, 16))   // activates outside target
	h.tree.Dispatch(finger(GestureChanged, Point{X: 150, Y: 30}, 32))  // enters target (local 20,20)
	h.tree.Dispatch(finger(GestureChanged, Point{X: 170, Y: 50}, 48))  // moves inside target (local 40,40)
	h.tree.Dispatch(finger(GestureChanged, Point{X: 150, Y: 150}, 64)) // leaves target
	h.tree.Dispatch(finger(GestureChanged, Point{X: 140, Y: 25}, 80))  // re-enters target (local 10,15)
	h.tree.Dispatch(finger(GestureEnded, Point{X: 140, Y: 25}, 96))    // drops

	want := []string{
		"start:2",
		"enter:{20 20}@{150 30}",
		"over:{20 20}",
		"over:{40 40}",
		"leave",
		"enter:{10 15}@{140 25}",
		"over:{10 15}",
		"drop:[a b]@{10 15}",
		"leave",
		"end:dropped=true,cancelled=false",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events:\n got  %q\n want %q", events, want)
	}
	wantActive := []string{"none", "[a b]:2", "none"}
	if !slices.Equal(activeSeen, wantActive) {
		t.Fatalf("ActiveDrag transitions:\n got  %q\n want %q", activeSeen, wantActive)
	}
}

func TestDragRejectsIncompatiblePayload(t *testing.T) {
	var targetHit bool
	var end DragEndEvent
	h := newHost(t, Size{Width: 300, Height: 100}, func(cx *Scope) Node {
		return View{
			Style: Style{Direction: Row, Gap: 20},
			Children: []Node{
				View{
					TestID: "source",
					Style:  Style{Width: Pt(80), Height: Pt(80)},
					Drag: &DragSource{
						Value: 42,
						OnEnd: func(e DragEndEvent) { end = e },
					},
				},
				View{
					TestID: "target",
					Style:  Style{Width: Pt(80), Height: Pt(80)},
					Drop: &DropTarget{
						Accept:  func(p DragPayload) bool { _, ok := p.Value.(string); return ok },
						OnEnter: func(DropEvent) { targetHit = true },
						OnOver:  func(DropEvent) { targetHit = true },
						OnLeave: func(DropEvent) { targetHit = true },
						OnDrop:  func(DropEvent) { targetHit = true },
					},
				},
			},
		}
	})
	h.commit()
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 40}, 16))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 130, Y: 40}, 32))
	if targetHit {
		t.Fatal("target rejected payload yet received callbacks")
	}
	if end.Dropped || end.Cancelled {
		t.Fatalf("end = %+v, want Dropped=false Cancelled=false", end)
	}
}

func TestDragPreviewDefaultAndCustomAndContext(t *testing.T) {
	themeKey := NewKey("drag-theme", "default")
	var previewDisposed bool
	h := newHost(t, Size{Width: 300, Height: 200}, func(cx *Scope) Node {
		themeKey.Provide(cx, "midnight")
		return View{
			Style: Style{Direction: Row, Gap: 20},
			Children: []Node{
				View{
					TestID: "default-src",
					Style:  Style{Width: Pt(80), Height: Pt(60)},
					Drag: &DragSource{
						Value: []int{1, 2, 3},
						Label: "Photos",
						Count: 3,
					},
				},
				View{
					TestID: "custom-src",
					Style:  Style{Width: Pt(80), Height: Pt(60)},
					Drag: &DragSource{
						Value: "doc",
						Preview: func(cx *Scope, p DragPayload) Node {
							cx.Cleanup(func() { previewDisposed = true })
							return View{
								TestID:   "custom-preview",
								Children: []Node{Text{Content: themeKey.Use(cx) + ":" + p.Value.(string)}},
							}
						},
					},
				},
				View{
					TestID: "declined-src",
					Style:  Style{Width: Pt(80), Height: Pt(60)},
					Drag: &DragSource{
						Start: func() DragPayload { return DragPayload{} },
					},
				},
			},
		}
	})
	h.commit()

	// Default preview mounts with label and count badge, hidden from accessibility, and cleans up on release.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 30}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 50, Y: 30}, 16))
	h.commit()
	if len(h.tree.view.hosted) != 1 {
		t.Fatalf("hosted overlays during default drag = %d, want 1", len(h.tree.view.hosted))
	}
	overlay := h.tree.view.hosted[0]
	if overlay.props.PointerEvents != PointerEventsNone {
		t.Error("drag preview overlay must have PointerEventsNone")
	}
	h.tree.Dispatch(finger(GestureEnded, Point{X: 50, Y: 30}, 32))
	h.commit()
	if len(h.tree.view.hosted) != 0 {
		t.Fatalf("hosted overlays after drag end = %d, want 0", len(h.tree.view.hosted))
	}

	// Custom preview inherits context from the source's scope and disposes cleanly on end.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 140, Y: 30}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 150, Y: 30}, 56))
	h.commit()
	prev := h.find("custom-preview")
	if prev == nil {
		t.Fatal("custom preview did not mount")
	}
	if got := prev.children[0].text.content; got != "midnight:doc" {
		t.Fatalf("custom preview text = %q, want midnight:doc", got)
	}
	h.tree.Dispatch(finger(GestureEnded, Point{X: 150, Y: 30}, 72))
	h.commit()
	if !previewDisposed {
		t.Error("custom preview scope was not disposed on drag end")
	}
	for _, n := range h.tree.nodes {
		if n != nil && !n.deleted && n.props.TestID == "custom-preview" {
			t.Error("custom preview remained mounted after drag end")
		}
	}

	// Declined drag (nil Value from Start) never activates.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 240, Y: 30}, 80))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 260, Y: 30}, 96))
	h.commit()
	if len(h.tree.view.hosted) != 0 || h.tree.drag.active != nil {
		t.Fatal("declined drag should not activate")
	}
	h.tree.Dispatch(finger(GestureEnded, Point{X: 260, Y: 30}, 112))
}

func TestDragCancelByEscapeTouchCancelAndUnmount(t *testing.T) {
	var events []string
	var showSource *Signal[bool]
	h := newHost(t, Size{Width: 300, Height: 120}, func(cx *Scope) Node {
		showSource = NewSignal(cx, true)
		return View{
			Style: Style{Direction: Row, Gap: 20},
			Children: []Node{
				Show{
					When: showSource.Get,
					Then: func(cx *Scope) Node {
						return View{
							TestID: "source",
							Style:  Style{Width: Pt(80), Height: Pt(80)},
							Drag: &DragSource{
								Value: "item",
								Label: "Item",
								OnEnd: func(e DragEndEvent) {
									events = append(events, fmt.Sprintf("end:%v:%v", e.Dropped, e.Cancelled))
								},
							},
						}
					},
				},
				View{
					TestID: "target",
					Style:  Style{Width: Pt(80), Height: Pt(80)},
					Drop: &DropTarget{
						OnEnter: func(DropEvent) { events = append(events, "enter") },
						OnLeave: func(DropEvent) { events = append(events, "leave") },
						OnDrop:  func(DropEvent) { events = append(events, "drop") },
					},
				},
			},
		}
	})
	h.commit()

	// Escape / EventBack cancels active drag and fires OnLeave + OnEnd(Cancelled:true).
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 40}, 16))
	h.commit()
	h.tree.Dispatch(driver.Event{Kind: driver.EventBack})
	h.commit()
	h.tree.Dispatch(finger(GestureEnded, Point{X: 130, Y: 40}, 32))
	want := []string{"enter", "leave", "end:false:true"}
	if !slices.Equal(events, want) {
		t.Fatalf("escape cancel events = %q, want %q", events, want)
	}
	if len(h.tree.view.hosted) != 0 {
		t.Fatal("preview overlay lingered after Escape cancel")
	}

	// TouchCancel cancels active drag cleanly.
	events = nil
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 40}, 56))
	h.tree.Dispatch(finger(GestureCancelled, Point{X: 130, Y: 40}, 72))
	h.commit()
	if !slices.Equal(events, want) {
		t.Fatalf("touch cancel events = %q, want %q", events, want)
	}

	// Unmounting the drag source mid-drag cancels and fires OnLeave on hovered target.
	events = nil
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 80))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 40}, 96))
	h.commit()
	showSource.Set(false)
	h.commit()
	h.commit()
	if !slices.Equal(events, []string{"enter", "leave"}) {
		t.Fatalf("unmount cancel events = %q, want [enter leave]", events)
	}
	if h.tree.drag.active != nil || len(h.tree.view.hosted) != 0 {
		t.Fatal("active drag or preview remained after source unmounted")
	}
}

func TestDragNestedTargetsAndModalBoundary(t *testing.T) {
	var dropped []string
	var innerDisabled *Signal[bool]
	h := newHost(t, Size{Width: 300, Height: 200}, func(cx *Scope) Node {
		innerDisabled = NewSignal(cx, false)
		return View{
			Style: Style{Direction: Row, Gap: 20},
			Children: []Node{
				View{
					Style: Style{Gap: 10},
					Children: []Node{
						View{TestID: "src-a", Style: Style{Width: Pt(60), Height: Pt(40)}, Drag: &DragSource{Value: "a"}},
						View{TestID: "src-b", Style: Style{Width: Pt(60), Height: Pt(40)}, Drag: &DragSource{Value: "b"}},
					},
				},
				View{
					TestID: "outer",
					Style:  Style{Width: Pt(140), Height: Pt(120), Padding: All(20)},
					Drop: &DropTarget{
						Accept: func(p DragPayload) bool { return p.Value == "a" || p.Value == "b" },
						OnDrop: func(e DropEvent) { dropped = append(dropped, "outer:"+e.Payload.Value.(string)) },
					},
					Children: []Node{
						View{
							TestID:   "inner",
							Style:    Style{Width: Pt(80), Height: Pt(60)},
							Disabled: innerDisabled.Get,
							Drop: &DropTarget{
								Accept: func(p DragPayload) bool { return p.Value == "b" },
								OnDrop: func(e DropEvent) { dropped = append(dropped, "inner:"+e.Payload.Value.(string)) },
							},
						},
					},
				},
			},
		}
	})
	h.commit()

	// Inner accepts "b": dragging "b" over inner hits inner.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 30, Y: 70}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 50}, 16))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 130, Y: 50}, 32))

	// Inner rejects "a": dragging "a" over inner falls back to outer.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 30, Y: 20}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 50}, 56))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 130, Y: 50}, 72))

	// When inner is disabled, dragging "b" over inner falls back to outer.
	innerDisabled.Set(true)
	h.commit()
	h.tree.Dispatch(finger(GestureBegan, Point{X: 30, Y: 70}, 80))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 130, Y: 50}, 96))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 130, Y: 50}, 112))

	want := []string{"inner:b", "outer:a", "outer:b"}
	if !slices.Equal(dropped, want) {
		t.Fatalf("nested drops = %q, want %q", dropped, want)
	}
}

func TestDragDoesNotEscapeModalAndPreservesAccessibility(t *testing.T) {
	var bgDropped, modalDropped bool
	var actions []string
	h := newHost(t, Size{Width: 300, Height: 200}, func(cx *Scope) Node {
		return View{
			TestID: "bg",
			Style:  Style{Width: Pt(300), Height: Pt(200)},
			Drop: &DropTarget{
				OnDrop: func(DropEvent) { bgDropped = true },
			},
			Children: []Node{
				Modal{
					Children: []Node{
						View{
							Style: Style{Direction: Row, Gap: 20},
							Children: []Node{
								View{
									TestID:               "modal-src",
									Style:                Style{Width: Pt(60), Height: Pt(40)},
									Role:                 RoleButton,
									Label:                "Task",
									AccessibilityActions: []AccessibilityAction{{Name: "move", Label: "Move"}},
									OnAccessibilityAction: func(name string) {
										actions = append(actions, name)
									},
									Drag: &DragSource{Value: "task"},
								},
								View{
									TestID: "modal-dst",
									Style:  Style{Width: Pt(60), Height: Pt(40)},
									Drop:   &DropTarget{OnDrop: func(DropEvent) { modalDropped = true }},
								},
							},
						},
					},
				},
			},
		}
	})
	h.commit()

	// Dragging inside the modal onto empty modal space must not hit the background target behind it.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 30, Y: 20}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 250, Y: 150}, 16))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 250, Y: 150}, 32))
	if bgDropped {
		t.Fatal("drag inside modal dropped onto target behind modal")
	}

	// Dragging to a target inside the same modal succeeds.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 30, Y: 20}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 110, Y: 20}, 56))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 110, Y: 20}, 72))
	if !modalDropped {
		t.Fatal("drag inside modal did not reach target in same modal")
	}

	// Accessibility action on the draggable view works unimpeded.
	src := h.find("modal-src")
	h.tree.Dispatch(driver.Event{Kind: driver.EventAccessibilityAction, ID: src.id, Text: "move"})
	if !slices.Equal(actions, []string{"move"}) {
		t.Fatalf("accessibility actions = %q, want [move]", actions)
	}
}

func TestDragDisabledSourcesAndTargetsAndEmptyPreviewCancel(t *testing.T) {
	var starts, drops, cancels int
	var srcDisabled, dstDisabled *Signal[bool]
	h := newHost(t, Size{Width: 300, Height: 120}, func(cx *Scope) Node {
		srcDisabled = NewSignal(cx, true)
		dstDisabled = NewSignal(cx, true)
		return View{
			Style: Style{Direction: Row, Gap: 20},
			Children: []Node{
				View{
					TestID: "src",
					Style:  Style{Width: Pt(80), Height: Pt(80)},
					Drag: &DragSource{
						Value:    "payload",
						Disabled: srcDisabled.Get,
						OnStart:  func(DragEvent) { starts++ },
						OnEnd: func(e DragEndEvent) {
							if e.Cancelled {
								cancels++
							}
						},
					},
				},
				View{
					TestID: "dst",
					Style:  Style{Width: Pt(80), Height: Pt(80)},
					Drop: &DropTarget{
						Disabled: dstDisabled.Get,
						OnDrop:   func(DropEvent) { drops++ },
					},
				},
			},
		}
	})
	h.commit()

	// Disabled drag source refuses to start.
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 0))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 140, Y: 40}, 16))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 140, Y: 40}, 32))
	if starts != 0 || drops != 0 {
		t.Fatalf("disabled source: starts=%d drops=%d, want 0,0", starts, drops)
	}

	// Enabled source dragging onto disabled target starts, mounts no visual overlay (empty label/preview),
	// and does not drop on the disabled target.
	srcDisabled.Set(false)
	h.commit()
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 40))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 140, Y: 40}, 56))
	h.commit()
	if len(h.tree.view.hosted) != 0 {
		t.Fatalf("empty preview mounted %d overlays, want 0", len(h.tree.view.hosted))
	}
	// Escape still cancels an active drag with no visual overlay.
	h.tree.Dispatch(driver.Event{Kind: driver.EventBack})
	h.tree.Dispatch(finger(GestureEnded, Point{X: 140, Y: 40}, 72))
	if starts != 1 || drops != 0 || cancels != 1 {
		t.Fatalf("disabled target + Escape: starts=%d drops=%d cancels=%d, want 1,0,1", starts, drops, cancels)
	}

	// Enabling target allows the drop.
	dstDisabled.Set(false)
	h.commit()
	h.tree.Dispatch(finger(GestureBegan, Point{X: 40, Y: 40}, 80))
	h.tree.Dispatch(finger(GestureChanged, Point{X: 140, Y: 40}, 96))
	h.tree.Dispatch(finger(GestureEnded, Point{X: 140, Y: 40}, 112))
	if starts != 2 || drops != 1 {
		t.Fatalf("enabled target: starts=%d drops=%d, want 2,1", starts, drops)
	}
}
