package nsgotest

import (
	"fmt"
	"slices"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
)

func TestDragToDropsPayloadAndSuppressesPress(t *testing.T) {
	var events []string
	app := Start(t, func(cx *core.Scope) core.Node {
		hovered := core.NewSignal(cx, false)
		return core.View{
			Style: core.Style{Direction: core.Row, Gap: 20, Padding: core.All(10)},
			Children: []core.Node{
				core.View{
					TestID: "item",
					Style:  core.Style{Width: core.Pt(80), Height: core.Pt(40)},
					OnPress: func(core.PressEvent) {
						events = append(events, "press")
					},
					Drag: &core.DragSource{
						Value:   "invoice-42",
						Label:   "Invoice.pdf",
						OnStart: func(core.DragEvent) { events = append(events, "start") },
						OnEnd: func(e core.DragEndEvent) {
							events = append(events, fmt.Sprintf("end:%v:%v", e.Dropped, e.Cancelled))
						},
					},
				},
				core.View{
					TestID: "folder",
					Style:  core.Style{Width: core.Pt(100), Height: core.Pt(60)},
					StyleFunc: func() core.Style {
						if hovered.Get() {
							return core.Style{Background: core.RGB(0x2f80ed)}
						}
						return core.Style{}
					},
					Drop: &core.DropTarget{
						Accept: func(p core.DragPayload) bool {
							_, ok := p.Value.(string)
							return ok
						},
						OnEnter: func(core.DropEvent) {
							hovered.Set(true)
							events = append(events, "enter")
						},
						OnLeave: func(core.DropEvent) {
							hovered.Set(false)
							events = append(events, "leave")
						},
						OnDrop: func(e core.DropEvent) {
							events = append(events, "drop:"+e.Payload.Value.(string))
						},
					},
				},
			},
		}
	}, Size(300, 120))

	app.DragTo("id:item", "id:folder")
	want := []string{"start", "enter", "drop:invoice-42", "leave", "end:true:false"}
	if !slices.Equal(events, want) {
		t.Fatalf("DragTo events:\n got  %q\n want %q", events, want)
	}
	if app.Find("id:folder").Style.Background != 0 {
		t.Error("drop target stayed highlighted after drop")
	}

	// Normal press still works right after dragging.
	app.Press("id:item")
	if events[len(events)-1] != "press" {
		t.Fatalf("after Press: events = %q, want trailing press", events)
	}
}

func TestDragThresholdAndShortMoveBehavior(t *testing.T) {
	var presses, drags int
	app := Start(t, func(cx *core.Scope) core.Node {
		return core.View{
			TestID:  "item",
			Style:   core.Style{Width: core.Pt(120), Height: core.Pt(40)},
			OnPress: func(core.PressEvent) { presses++ },
			Drag: &core.DragSource{
				Value:   "item",
				OnStart: func(core.DragEvent) { drags++ },
			},
		}
	}, Size(200, 100))

	// A 3pt jitter stays below DefaultDragThreshold (5pt) and still presses.
	app.Drag("id:item", core.Point{X: 3, Y: 0})
	if drags != 0 || presses != 1 {
		t.Fatalf("3pt move: drags=%d presses=%d, want 0,1", drags, presses)
	}

	// A 6pt move crosses DefaultDragThreshold (5pt) even though it stays below pressSlop (8pt):
	// the drag starts and OnPress is suppressed.
	app.Drag("id:item", core.Point{X: 6, Y: 0})
	if drags != 1 || presses != 1 {
		t.Fatalf("6pt move: drags=%d presses=%d, want 1,1", drags, presses)
	}
}

func TestDragStepByStepPreviewEnterLeaveAndEscapeCancel(t *testing.T) {
	var events []string
	app := Start(t, func(cx *core.Scope) core.Node {
		overA := core.NewSignal(cx, false)
		overB := core.NewSignal(cx, false)
		return core.View{
			Style: core.Style{Direction: core.Row, Gap: 16, Padding: core.All(10)},
			Children: []core.Node{
				core.View{
					TestID:  "item",
					Style:   core.Style{Width: core.Pt(70), Height: core.Pt(40)},
					OnPress: func(core.PressEvent) { events = append(events, "press") },
					Drag: &core.DragSource{
						Start: func() core.DragPayload {
							return core.DragPayload{
								Value: []string{"doc1", "doc2", "doc3"},
								Label: "Invoice.pdf",
								Count: 3,
							}
						},
						OnEnd: func(e core.DragEndEvent) {
							events = append(events, fmt.Sprintf("end:%v:%v", e.Dropped, e.Cancelled))
						},
					},
				},
				core.View{
					TestID: "folder-a",
					Style:  core.Style{Width: core.Pt(70), Height: core.Pt(40)},
					StyleFunc: func() core.Style {
						if overA.Get() {
							return core.Style{Background: core.RGB(0x112233)}
						}
						return core.Style{}
					},
					Drop: &core.DropTarget{
						OnEnter: func(core.DropEvent) { overA.Set(true); events = append(events, "enter-a") },
						OnLeave: func(core.DropEvent) { overA.Set(false); events = append(events, "leave-a") },
						OnDrop:  func(core.DropEvent) { events = append(events, "drop-a") },
					},
				},
				core.View{
					TestID: "folder-b",
					Style:  core.Style{Width: core.Pt(70), Height: core.Pt(40)},
					StyleFunc: func() core.Style {
						if overB.Get() {
							return core.Style{Background: core.RGB(0x445566)}
						}
						return core.Style{}
					},
					Drop: &core.DropTarget{
						OnEnter: func(core.DropEvent) { overB.Set(true); events = append(events, "enter-b") },
						OnLeave: func(core.DropEvent) { overB.Set(false); events = append(events, "leave-b") },
						OnDrop:  func(core.DropEvent) { events = append(events, "drop-b") },
					},
				},
			},
		}
	}, Size(300, 100))

	f := app.Touch("id:item", core.Point{X: 10, Y: 10})
	f.MoveTo("id:folder-a")
	if !app.Exists("text:Invoice.pdf") || !app.Exists("text:3") {
		t.Fatalf("floating preview with label and count badge missing during drag\n%s", app.Dump())
	}
	if app.Find("id:folder-a").Style.Background != core.RGB(0x112233) {
		t.Error("folder-a not highlighted while drag is over it")
	}

	f.MoveTo("id:folder-b")
	if app.Find("id:folder-a").Style.Background != 0 {
		t.Error("folder-a stayed highlighted after drag left")
	}
	if app.Find("id:folder-b").Style.Background != core.RGB(0x445566) {
		t.Error("folder-b not highlighted while drag is over it")
	}

	app.Key("escape")
	if app.Exists("text:Invoice.pdf") || app.Exists("text:3") {
		t.Fatal("floating preview stayed mounted after Escape cancel")
	}
	if app.Find("id:folder-b").Style.Background != 0 {
		t.Error("folder-b stayed highlighted after Escape cancel")
	}
	f.Lift()

	want := []string{"enter-a", "leave-a", "enter-b", "leave-b", "end:false:true"}
	if !slices.Equal(events, want) {
		t.Fatalf("events:\n got  %q\n want %q", events, want)
	}
}

func TestDragMultiItemNestedTargetsAndAccessibilityAlternative(t *testing.T) {
	type noteBatch struct{ IDs []string }
	var dropped []string
	var a11yMoves int

	app := Start(t, func(cx *core.Scope) core.Node {
		selected := core.NewSignalFunc(cx, []string{"n1", "n2"}, slices.Equal[[]string])
		row := func(id string) core.Node {
			return core.View{
				TestID:               "row-" + id,
				Style:                core.Style{Width: core.Pt(80), Height: core.Pt(30)},
				Role:                 core.RoleButton,
				Label:                id,
				AccessibilityActions: []core.AccessibilityAction{{Name: "move", Label: "Move"}},
				OnAccessibilityAction: func(name string) {
					if name == "move" {
						a11yMoves++
					}
				},
				Drag: &core.DragSource{
					Start: func() core.DragPayload {
						ids := selected.GetUntracked()
						if slices.Contains(ids, id) {
							return core.DragPayload{
								Value: noteBatch{IDs: slices.Clone(ids)},
								Label: id,
								Count: len(ids),
							}
						}
						return core.DragPayload{Value: id, Label: id, Count: 1}
					},
				},
			}
		}
		return core.View{
			Style: core.Style{Direction: core.Row, Gap: 20, Padding: core.All(10)},
			Children: []core.Node{
				core.View{
					Style:    core.Style{Gap: 8},
					Children: []core.Node{row("n1"), row("n2"), row("n3")},
				},
				core.View{
					TestID: "outer-folder",
					Style:  core.Style{Width: core.Pt(120), Height: core.Pt(100), Padding: core.All(15)},
					Drop: &core.DropTarget{
						Accept: func(p core.DragPayload) bool {
							_, isBatch := p.Value.(noteBatch)
							_, isSingle := p.Value.(string)
							return isBatch || isSingle
						},
						OnDrop: func(e core.DropEvent) {
							switch v := e.Payload.Value.(type) {
							case noteBatch:
								dropped = append(dropped, fmt.Sprintf("outer:%v", v.IDs))
							case string:
								dropped = append(dropped, "outer:"+v)
							}
						},
					},
					Children: []core.Node{
						core.View{
							TestID: "inner-archive",
							Style:  core.Style{Width: core.Pt(80), Height: core.Pt(50)},
							Drop: &core.DropTarget{
								Accept: func(p core.DragPayload) bool {
									_, isBatch := p.Value.(noteBatch)
									return isBatch
								},
								OnDrop: func(e core.DropEvent) {
									v := e.Payload.Value.(noteBatch)
									dropped = append(dropped, fmt.Sprintf("inner:%v", v.IDs))
								},
							},
						},
					},
				},
			},
		}
	}, Size(300, 150))

	// Dragging selected row n1 carries both selected items and drops on inner-archive.
	app.DragTo("id:row-n1", "id:inner-archive")
	// Dragging unselected row n3 carries a single string, rejected by inner-archive, so outer-folder receives it.
	app.DragTo("id:row-n3", "id:inner-archive")
	// Assistive technology can still invoke the accessible alternative action.
	app.AccessibilityAction("id:row-n1", "move")

	want := []string{"inner:[n1 n2]", "outer:n3"}
	if !slices.Equal(dropped, want) {
		t.Fatalf("drops = %q, want %q", dropped, want)
	}
	if a11yMoves != 1 {
		t.Fatalf("a11yMoves = %d, want 1", a11yMoves)
	}
}

func TestDragInsideScrollViewHandsOffToDropTarget(t *testing.T) {
	var dropped string
	app := Start(t, func(cx *core.Scope) core.Node {
		rows := make([]core.Node, 12)
		for i := range rows {
			id := fmt.Sprintf("row-%d", i)
			rows[i] = core.View{
				TestID: id,
				Style:  core.Style{Height: core.Pt(30)},
				Drag:   &core.DragSource{Value: id, Label: id},
			}
		}
		return core.View{
			Style: core.Style{Direction: core.Row, Gap: 16, Padding: core.All(8)},
			Children: []core.Node{
				core.Scroll{
					TestID:   "list",
					Style:    core.Style{Width: core.Pt(100), Height: core.Pt(120)},
					Children: rows,
				},
				core.View{
					TestID: "bin",
					Style:  core.Style{Width: core.Pt(100), Height: core.Pt(120)},
					Drop: &core.DropTarget{
						OnDrop: func(e core.DropEvent) {
							dropped = e.Payload.Value.(string)
						},
					},
				},
			},
		}
	}, Size(260, 150))

	app.DragTo("id:row-1", "id:bin")
	if dropped != "row-1" {
		t.Fatalf("dropped = %q, want row-1", dropped)
	}

	// Scrolling the list first still hit-tests the scrolled row accurately.
	app.ScrollTo("id:list", core.Point{Y: 90})
	dropped = ""
	app.DragTo("id:row-4", "id:bin")
	if dropped != "row-4" {
		t.Fatalf("after scroll: dropped = %q, want row-4", dropped)
	}

	// Finger.Cancel cancels an active drag cleanly without dropping.
	dropped = ""
	f := app.Touch("id:row-5", core.Point{X: 10, Y: 10})
	f.MoveTo("id:bin")
	f.Cancel()
	if dropped != "" {
		t.Fatalf("after Finger.Cancel: dropped = %q, want empty", dropped)
	}
}
