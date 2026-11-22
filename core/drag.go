package core

import (
	"slices"
	"strconv"
)

// DefaultDragThreshold is how many points the pointer must move from press-down
// before a DragSource activates a drag, leaving shorter movements as normal
// presses.
const DefaultDragThreshold Points = 5

// dragPreviewOffset places the floating drag preview slightly down and right of
// the pointer so the pointer stays visible over drop targets.
const dragPreviewOffset Points = 12

// DragPayload is what an internal drag carries from a DragSource to a
// DropTarget within the same surface.
type DragPayload struct {
	// Value is the app-defined Go value carried by the drag: a single item, a
	// slice for a multi-item drag, or any typed value.
	Value any
	// Label names what is being dragged in the default floating preview when no
	// custom Preview is set.
	Label string
	// Count is the number of items dragged together; values <= 0 default to 1.
	// When Count > 1, the default floating preview shows a numeric count badge.
	Count int
	// Preview optionally builds a custom floating preview node for this drag,
	// overriding DragSource.Preview.
	Preview func(cx *Scope) Node
}

// DragSource makes a View draggable within its surface. Once the pointer moves
// past Threshold (DefaultDragThreshold when <= 0), a drag activates carrying
// Value (or what Start returns) with a floating preview following the pointer.
// A shorter movement still presses the view. Pair DragSource with a keyboard
// shortcut, context menu, or AccessibilityActions so keyboard and assistive
// technology users can perform the same move.
type DragSource struct {
	// Value is the Go value carried by the drag when Start is nil. A nil Value
	// with a nil Start declines starting a drag.
	Value any
	// Label names the dragged item in the default floating preview when Preview
	// is nil.
	Label string
	// Count is the number of items carried when Start is nil (defaults to 1).
	Count int
	// Preview builds a custom floating preview node following the pointer in a
	// non-interactive overlay. When nil and DragPayload.Preview is nil, a
	// compact floating badge shows Label and (when Count > 1) Count.
	Preview func(cx *Scope, payload DragPayload) Node
	// Start, when set, runs as the drag crosses Threshold to build the
	// DragPayload dynamically, such as bundling all currently selected items
	// into one drag. Returning a DragPayload with a nil Value declines starting
	// the drag. Non-zero fields override Value, Label, and Count.
	Start func() DragPayload
	// Disabled reports whether dragging from this view is currently disabled. A
	// View whose own Disabled returns true also cannot start a drag.
	Disabled func() bool
	// Threshold is how many points the pointer must move before the drag
	// starts; values <= 0 use DefaultDragThreshold (5 points).
	Threshold Points
	// OnStart runs when the drag activates.
	OnStart func(DragEvent)
	// OnEnd runs when the drag finishes, whether dropped on a target, released
	// outside any accepting target, or cancelled.
	OnEnd func(DragEndEvent)
}

// DropTarget makes a View accept internal drags from DragSource views in the
// same surface. When nested drop targets overlap, the innermost visible,
// enabled target under the pointer whose Accept returns true receives the drag.
type DropTarget struct {
	// Accept reports whether this target accepts payload. When nil, every
	// internal drag payload is accepted.
	Accept func(DragPayload) bool
	// OnEnter runs when an accepted drag enters this target.
	OnEnter func(DropEvent)
	// OnOver runs when an accepted drag enters or moves within this target.
	OnOver func(DropEvent)
	// OnLeave runs when an accepted drag leaves this target, finishes dropping
	// on it, or cancels while over it.
	OnLeave func(DropEvent)
	// OnDrop runs when an accepted drag is released over this target.
	OnDrop func(DropEvent)
	// Disabled reports whether this target currently refuses drops. A View
	// whose own Disabled returns true also refuses drops.
	Disabled func() bool
}

// DragEvent reports an internal drag activating on its DragSource.
type DragEvent struct {
	Payload DragPayload
	// Position is in the drag source view's coordinates.
	Position Point
	// PagePosition is in the surface's coordinates.
	PagePosition Point
}

// DragEndEvent reports an internal drag ending on its DragSource.
type DragEndEvent struct {
	Payload DragPayload
	// PagePosition is where the pointer was when the drag ended.
	PagePosition Point
	// Dropped is true when an accepting DropTarget received the drop.
	Dropped bool
	// Cancelled is true when the drag was cancelled (Escape, back, pointer
	// cancellation, or source unmount) rather than released normally.
	Cancelled bool
}

// DropEvent reports an internal drag entering, hovering over, leaving, or
// dropping on a DropTarget.
type DropEvent struct {
	Payload DragPayload
	// Position is in the drop target view's coordinates.
	Position Point
	// PagePosition is in the surface's coordinates.
	PagePosition Point
}

// ActiveDrag returns the payload of the internal drag currently active on cx's
// surface and true, or a zero DragPayload and false when no drag is active. It
// is a tracked read, so styles and views reading it update when a drag starts
// or ends.
func ActiveDrag(cx *Scope) (DragPayload, bool) {
	t := cx.tree()
	if t == nil {
		return DragPayload{}, false
	}
	snap := t.activeDragSignal().Get()
	if snap.id == 0 {
		return DragPayload{}, false
	}
	return snap.payload, true
}

// activeDragSnapshot pairs a drag payload with its session ID so the signal
// compares session IDs instead of interface values (which may hold slices or
// other uncomparable Go types).
type activeDragSnapshot struct {
	id      uint64
	payload DragPayload
}

type dragState struct {
	pending        *pendingDrag
	active         *activeDrag
	nextID         uint64
	activeSignal   *Signal[activeDragSnapshot]
	suppressPress  bool
	suppressSource *node
}

type pendingDrag struct {
	source    *node
	fingerID  int
	start     Point
	threshold Points
}

type activeDrag struct {
	id            uint64
	source        *node
	fingerID      int
	payload       DragPayload
	pos           Point
	posSignal     *Signal[Point]
	target        *node
	previewRegion *node
	previewScope  *Scope
}

func (t *Tree) activeDragSignal() *Signal[activeDragSnapshot] {
	if t.drag.activeSignal == nil {
		var cur activeDragSnapshot
		if d := t.drag.active; d != nil {
			cur = activeDragSnapshot{id: d.id, payload: d.payload}
		}
		t.drag.activeSignal = NewSignalFunc(t.root, cur, func(a, b activeDragSnapshot) bool { return a.id == b.id })
	}
	return t.drag.activeSignal
}

func (t *Tree) setActiveDragSignal(id uint64, payload DragPayload) {
	if t.drag.activeSignal != nil {
		t.drag.activeSignal.Set(activeDragSnapshot{id: id, payload: payload})
	}
}

func (t *Tree) clearDragSuppress() {
	t.drag.suppressPress = false
	t.drag.suppressSource = nil
}

// suppressDragPress reports whether a non-keyboard press on n should be
// swallowed because an internal drag just took the gesture.
func (t *Tree) suppressDragPress(n *node) bool {
	if t.drag.active != nil {
		return true
	}
	if !t.drag.suppressPress {
		return false
	}
	src := t.drag.suppressSource
	t.drag.suppressPress = false
	t.drag.suppressSource = nil
	if src == nil || src.deleted {
		return true
	}
	return n == src || n.within(src) || src.within(n)
}

func (t *Tree) dragSourceEnabled(n *node) bool {
	if n == nil || !n.alive() || n.drag == nil || n.labelProps().Disabled {
		return false
	}
	if n.drag.Disabled != nil {
		disabled := false
		t.within(n.owner, func() { disabled = n.drag.Disabled() })
		if disabled {
			return false
		}
	}
	return true
}

func (t *Tree) dropTargetEnabled(n *node) bool {
	if n == nil || !n.alive() || n.drop == nil || n.labelProps().Disabled {
		return false
	}
	if n.drop.Disabled != nil {
		disabled := false
		t.within(n.owner, func() { disabled = n.drop.Disabled() })
		if disabled {
			return false
		}
	}
	return true
}

// dragTouch advances pending and active internal drags from EventTouch.
func (t *Tree) dragTouch(e *TouchEvent, path []*node, about Touch) {
	switch e.Phase {
	case TouchStart:
		if t.drag.active != nil {
			return
		}
		// If a touch responder already claimed on start, leave the gesture to it.
		if t.touches.responder != nil {
			t.drag.pending = nil
			return
		}
		for _, n := range path {
			if n.drag == nil {
				continue
			}
			if !t.dragSourceEnabled(n) {
				continue
			}
			threshold := n.drag.Threshold
			if threshold <= 0 {
				threshold = DefaultDragThreshold
			}
			t.drag.pending = &pendingDrag{
				source:    n,
				fingerID:  about.ID,
				start:     about.PagePosition,
				threshold: threshold,
			}
			return
		}
	case TouchMove:
		if d := t.drag.active; d != nil {
			if about.ID != d.fingerID {
				return
			}
			t.moveDrag(about.PagePosition)
			return
		}
		p := t.drag.pending
		if p == nil || about.ID != p.fingerID {
			return
		}
		if t.touches.responder != nil || !p.source.alive() {
			t.drag.pending = nil
			return
		}
		dx := about.PagePosition.X - p.start.X
		dy := about.PagePosition.Y - p.start.Y
		if dx*dx+dy*dy < p.threshold*p.threshold {
			return
		}
		t.drag.pending = nil
		t.startDrag(p.source, p.fingerID, about.PagePosition)
	case TouchEnd:
		if p := t.drag.pending; p != nil && about.ID == p.fingerID {
			t.drag.pending = nil
		}
		if d := t.drag.active; d != nil && about.ID == d.fingerID {
			t.finishDrag(about.PagePosition)
		}
	case TouchCancel:
		if p := t.drag.pending; p != nil && about.ID == p.fingerID {
			t.drag.pending = nil
		}
		if d := t.drag.active; d != nil && about.ID == d.fingerID {
			t.cancelDrag()
		}
	}
}

func (t *Tree) startDrag(source *node, fingerID int, pagePos Point) {
	if !t.dragSourceEnabled(source) {
		return
	}
	src := source.drag
	var payload DragPayload
	if src.Start != nil {
		t.within(source.owner, func() { payload = src.Start() })
		if payload.Value == nil {
			return
		}
		if payload.Label == "" {
			payload.Label = src.Label
		}
		if payload.Count <= 0 {
			payload.Count = src.Count
		}
	} else {
		if src.Value == nil {
			return
		}
		payload = DragPayload{
			Value: src.Value,
			Label: src.Label,
			Count: src.Count,
		}
	}
	if payload.Count <= 0 {
		payload.Count = 1
	}
	if !source.alive() || source.drag != src {
		return
	}

	// Clear any pressed state on the drag source and its pressable ancestors
	// now that the gesture has become a drag.
	for cur := source; cur != nil; cur = cur.nativeParent() {
		if cur.states&statePressed != 0 {
			t.setState(cur, statePressed, false)
		}
	}

	t.drag.nextID++
	d := &activeDrag{
		id:       t.drag.nextID,
		source:   source,
		fingerID: fingerID,
		payload:  payload,
		pos:      pagePos,
	}
	t.drag.active = d
	t.blockNative(source)
	t.mountDragPreview(d)
	t.setActiveDragSignal(d.id, d.payload)

	if src.OnStart != nil && source.alive() {
		ev := DragEvent{
			Payload:      d.payload,
			Position:     source.surfacePoint(pagePos),
			PagePosition: pagePos,
		}
		t.within(source.owner, func() { src.OnStart(ev) })
	}
	if t.drag.active == d {
		t.updateDragTarget(pagePos)
	}
}

func (t *Tree) moveDrag(pagePos Point) {
	d := t.drag.active
	if d == nil {
		return
	}
	d.pos = pagePos
	if d.posSignal != nil {
		d.posSignal.Set(pagePos)
	}
	t.updateDragTarget(pagePos)
}

func (t *Tree) updateDragTarget(pagePos Point) {
	d := t.drag.active
	if d == nil {
		return
	}
	next, local := t.findDropTarget(pagePos, d.source, d.payload)
	if t.drag.active != d {
		return
	}
	prev := d.target
	if prev != next {
		d.target = next
		if prev != nil && prev.alive() && prev.drop != nil && prev.drop.OnLeave != nil {
			ev := DropEvent{
				Payload:      d.payload,
				Position:     prev.surfacePoint(pagePos),
				PagePosition: pagePos,
			}
			t.within(prev.owner, func() { prev.drop.OnLeave(ev) })
			if t.drag.active != d {
				return
			}
		}
		if next != nil && next.alive() && next.drop != nil {
			ev := DropEvent{
				Payload:      d.payload,
				Position:     local,
				PagePosition: pagePos,
			}
			if next.drop.OnEnter != nil {
				t.within(next.owner, func() { next.drop.OnEnter(ev) })
				if t.drag.active != d {
					return
				}
			}
			if next.alive() && next.drop != nil && next.drop.OnOver != nil {
				t.within(next.owner, func() { next.drop.OnOver(ev) })
			}
		}
		return
	}
	if next != nil && next.alive() && next.drop != nil && next.drop.OnOver != nil {
		ev := DropEvent{
			Payload:      d.payload,
			Position:     local,
			PagePosition: pagePos,
		}
		t.within(next.owner, func() { next.drop.OnOver(ev) })
	}
}

// findDropTarget finds the innermost visible, enabled DropTarget under pagePos
// in the same modal and confinement scope as source that accepts payload.
func (t *Tree) findDropTarget(pagePos Point, source *node, payload DragPayload) (*node, Point) {
	if source == nil || t.view == nil {
		return nil, Point{}
	}
	host := t.modalHost(source)
	conf := confinement(source.owner)
	id, _ := hitWith(t.view, pagePos, func(n *node) bool { return !n.inDragPreview() })
	for cur := t.node(id); cur != nil; cur = cur.nativeParent() {
		if t.modalHost(cur) != host {
			break
		}
		if conf != nil && !owns(conf, cur.owner) {
			continue
		}
		if !t.dropTargetEnabled(cur) {
			continue
		}
		accepted := true
		if cur.drop.Accept != nil {
			t.within(cur.owner, func() { accepted = cur.drop.Accept(payload) })
		}
		if accepted && cur.alive() && cur.drop != nil {
			return cur, cur.surfacePoint(pagePos)
		}
	}
	return nil, Point{}
}

func (t *Tree) finishDrag(pagePos Point) {
	d := t.drag.active
	if d == nil {
		return
	}
	if pagePos != d.pos {
		d.pos = pagePos
		t.updateDragTarget(pagePos)
		if t.drag.active != d {
			return
		}
	}
	t.drag.active = nil
	t.drag.suppressPress = true
	t.drag.suppressSource = d.source
	if t.touches.responder == nil {
		t.blockNative(nil)
	}
	t.teardownDragPreview(d)
	t.setActiveDragSignal(0, DragPayload{})

	dropped := false
	target := d.target
	if target != nil && target.alive() && target.drop != nil {
		ev := DropEvent{
			Payload:      d.payload,
			Position:     target.surfacePoint(pagePos),
			PagePosition: pagePos,
		}
		if t.dropTargetEnabled(target) {
			dropped = true
			if target.drop.OnDrop != nil {
				t.within(target.owner, func() { target.drop.OnDrop(ev) })
			}
		}
		if target.alive() && target.drop != nil && target.drop.OnLeave != nil {
			t.within(target.owner, func() { target.drop.OnLeave(ev) })
		}
	}

	if d.source != nil && d.source.alive() && d.source.drag != nil && d.source.drag.OnEnd != nil {
		ev := DragEndEvent{
			Payload:      d.payload,
			PagePosition: pagePos,
			Dropped:      dropped,
			Cancelled:    false,
		}
		t.within(d.source.owner, func() { d.source.drag.OnEnd(ev) })
	}
}

// cancelDrag cancels an active internal drag, reporting whether one was active.
func (t *Tree) cancelDrag() bool {
	d := t.drag.active
	if d == nil {
		return false
	}
	t.drag.active = nil
	t.drag.pending = nil
	t.drag.suppressPress = true
	t.drag.suppressSource = d.source
	if t.touches.responder == nil {
		t.blockNative(nil)
	}
	t.teardownDragPreview(d)
	t.setActiveDragSignal(0, DragPayload{})

	if target := d.target; target != nil && target.alive() && target.drop != nil && target.drop.OnLeave != nil {
		ev := DropEvent{
			Payload:      d.payload,
			Position:     target.surfacePoint(d.pos),
			PagePosition: d.pos,
		}
		t.within(target.owner, func() { target.drop.OnLeave(ev) })
	}
	if d.source != nil && d.source.alive() && d.source.drag != nil && d.source.drag.OnEnd != nil {
		ev := DragEndEvent{
			Payload:      d.payload,
			PagePosition: d.pos,
			Dropped:      false,
			Cancelled:    true,
		}
		t.within(d.source.owner, func() { d.source.drag.OnEnd(ev) })
	}
	return true
}

// dragNodeDiscarded cleans up drag state when a pending/active source or
// hovered drop target is removed from the tree.
func (t *Tree) dragNodeDiscarded(n *node) {
	if t.drag.pending != nil && t.drag.pending.source == n {
		t.drag.pending = nil
	}
	if t.drag.suppressSource == n {
		t.drag.suppressSource = nil
	}
	d := t.drag.active
	if d == nil {
		return
	}
	if d.target == n {
		d.target = nil
	}
	if d.source == n {
		t.drag.active = nil
		t.drag.suppressPress = true
		t.drag.suppressSource = nil
		if t.touches.responder == nil {
			t.blockNative(nil)
		}
		t.after(t.view, func() { t.finishDiscardedDrag(d) })
	}
}

func (t *Tree) finishDiscardedDrag(d *activeDrag) {
	t.teardownDragPreview(d)
	if t.drag.active == nil {
		t.setActiveDragSignal(0, DragPayload{})
	}
	if target := d.target; target != nil && target.alive() && target.drop != nil && target.drop.OnLeave != nil {
		ev := DropEvent{
			Payload:      d.payload,
			Position:     target.surfacePoint(d.pos),
			PagePosition: d.pos,
		}
		t.within(target.owner, func() { target.drop.OnLeave(ev) })
	}
	if d.source != nil && !d.source.owner.disposed && d.source.drag != nil && d.source.drag.OnEnd != nil {
		ev := DragEndEvent{
			Payload:      d.payload,
			PagePosition: d.pos,
			Dropped:      false,
			Cancelled:    true,
		}
		t.within(d.source.owner, func() { d.source.drag.OnEnd(ev) })
	}
}

func (n *node) inDragPreview() bool {
	for p := n; p != nil; p = p.parent {
		if p.dragPreview {
			return true
		}
	}
	return false
}

func dragPreviewHidden() bool { return true }

func (t *Tree) mountDragPreview(d *activeDrag) {
	region := t.newRegion(d.source, d.source.owner)
	region.dragPreview = true
	region.props.ElementsHidden = true
	s := &Scope{}
	d.source.owner.adopt(s)
	d.previewRegion = region
	d.previewScope = s
	d.posSignal = NewSignal(s, d.pos)

	// Guard against scope disposal without going through Tree.discard.
	s.Cleanup(func() {
		if t.drag.active == d {
			t.drag.active = nil
			t.drag.suppressPress = true
			t.drag.suppressSource = nil
			if t.touches.responder == nil {
				t.blockNative(nil)
			}
			d.previewScope = nil
			t.after(t.view, func() { t.finishDiscardedDrag(d) })
		}
	})

	t.within(s, func() {
		s.OnKeyWith("escape", KeyOptions{When: KeyAlways, OverModals: true}, func() {
			t.cancelDrag()
		})
		var content Node
		switch {
		case d.payload.Preview != nil:
			content = d.payload.Preview(s)
		case d.source.drag != nil && d.source.drag.Preview != nil:
			content = d.source.drag.Preview(s, d.payload)
		case d.payload.Label != "" || d.payload.Count > 1:
			content = defaultDragPreview(d.payload)
		}
		if content == nil {
			return
		}
		overlay := Overlay{
			Topmost:       true,
			PointerEvents: PointerEventsNone,
			OnCancel:      func() { t.cancelDrag() },
			StyleFunc: func() Style {
				p := d.posSignal.Get()
				return Style{
					Position: Absolute,
					Left:     Pt(p.X + dragPreviewOffset),
					Top:      Pt(p.Y + dragPreviewOffset),
				}
			},
			Children: []Node{
				View{
					PointerEvents:               PointerEventsNone,
					AccessibilityElementsHidden: dragPreviewHidden,
					Children:                    []Node{content},
				},
			},
		}
		t.mount(s, region, overlay)
	})
}

func (t *Tree) teardownDragPreview(d *activeDrag) {
	if d.previewRegion != nil && !d.previewRegion.deleted {
		t.clear(d.previewRegion)
		if p := d.previewRegion.parent; p != nil {
			if i := slices.Index(p.children, d.previewRegion); i >= 0 {
				p.children = slices.Delete(p.children, i, i+1)
				t.childrenChanged(p)
			}
		}
		delete(t.regions, d.previewRegion)
		d.previewRegion.deleted = true
	}
	d.previewRegion = nil
	if d.previewScope != nil {
		s := d.previewScope
		d.previewScope = nil
		s.Dispose()
	}
}

func defaultDragPreview(p DragPayload) Node {
	var parts []Node
	if p.Label != "" {
		parts = append(parts, Text{
			Content:  p.Label,
			MaxLines: 1,
			Style: Style{
				Color:    RGB(0xffffff),
				FontSize: 13,
			},
		})
	}
	if p.Count > 1 {
		parts = append(parts, View{
			Style: Style{
				Background:     RGB(0x2f80ed),
				CornerRadius:   10,
				Padding:        Symmetric(1, 6),
				AlignItems:     AlignCenter,
				JustifyContent: JustifyCenter,
			},
			Children: []Node{
				Text{
					Content: strconv.Itoa(p.Count),
					Style: Style{
						Color:      RGB(0xffffff),
						FontSize:   12,
						FontWeight: FontWeightSemibold,
					},
				},
			},
		})
	}
	return View{
		Style: Style{
			Direction:    Row,
			AlignItems:   AlignCenter,
			Gap:          6,
			Padding:      Symmetric(6, 10),
			CornerRadius: 8,
			Background:   RGBA(0x1f2328, 0.92),
			Shadows: []BoxShadow{
				{Offset: Point{Y: 4}, Blur: 12, Color: RGBA(0x000000, 0.22)},
			},
		},
		Children: parts,
	}
}
