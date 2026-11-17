package webkeys_test

import (
	"testing"

	_ "github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

func TestTabMovesFocusThroughFieldsAndPressable(t *testing.T) {
	if nsgotest.Target() != "web" {
		t.Skip("checks browser keyboard focus")
	}
	app := nsgotest.Start(t, func(*core.Scope) core.Node {
		return core.View{Children: []core.Node{
			core.Input{TestID: "first", Style: core.Style{Width: core.Pt(120), Height: core.Pt(30)}},
			core.View{},
			core.Input{TestID: "second", Style: core.Style{Width: core.Pt(120), Height: core.Pt(30)}},
			core.View{TestID: "button", OnPress: func(core.PressEvent) {}, Style: core.Style{Width: core.Pt(120), Height: core.Pt(30)}},
		}}
	}, nsgotest.Size(240, 150))
	app.Focus("id:first")
	app.Key("Tab")
	if !app.Find("id:second").Focused || app.Find("id:first").Focused {
		t.Fatalf("Tab focused first=%v second=%v", app.Find("id:first").Focused, app.Find("id:second").Focused)
	}
	app.Key("Tab")
	if !app.Find("id:button").Focused || app.Find("id:second").Focused {
		t.Fatalf("second Tab focused second=%v button=%v", app.Find("id:second").Focused, app.Find("id:button").Focused)
	}
	app.Key("shift+Tab")
	if !app.Find("id:second").Focused || app.Find("id:button").Focused {
		t.Fatalf("Shift+Tab focused button=%v second=%v", app.Find("id:button").Focused, app.Find("id:second").Focused)
	}
}
