package pick_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/nsgo-dev/nsgo"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

// pickApp is a button that asks for a photo and shows how many files came back.
func pickApp(cx *core.Scope) core.Node {
	got := core.NewSignal(cx, "none")
	return core.View{Children: []core.Node{
		core.View{Role: core.RoleButton, Label: "Pick", TestID: "pick", Style: core.Style{Width: core.Pt(80), Height: core.Pt(30)},
			OnPress: func(core.PressEvent) {
				cx.PickImages(core.PickOptions{Limit: 1}, func(files []core.PickedFile) {
					got.Set(strings.TrimSpace(fmt.Sprintf("picked %d %s", len(files), names(files))))
				})
			}},
		core.Text{ContentFunc: got.Get},
	}}
}

func names(files []core.PickedFile) string {
	s := ""
	for _, f := range files {
		s += f.Name
	}
	return s
}

func skipOtherTargets(t *testing.T) {
	switch nsgotest.Target() {
	case "", "macos", "windows", "linux":
	default:
		t.Skip("PickImages answers headless and on the desktops; the other targets show the system's picker")
	}
}

// TestPickImagesAnswersTheApp checks that App.PickImages, set after Start as a test sets it, answers the app's pick in place of the system's picker, headless and on a desktop target alike (#866).
func TestPickImagesAnswersTheApp(t *testing.T) {
	skipOtherTargets(t)
	app := nsgotest.Start(t, pickApp)
	called := 0
	app.PickImages = func(core.PickRequest) []core.PickedFile {
		called++
		return []core.PickedFile{{Name: "a.png", MIME: "image/png", Data: []byte("png")}}
	}
	app.Press("id:pick")
	app.WaitFor("text:picked 1 a.png", 5*time.Second)
	if called != 1 || len(app.Picks) != 1 || app.Picks[0].Limit != 1 {
		t.Fatalf("PickImages was asked %d times for %+v, want once for a limit of 1", called, app.Picks)
	}
}

// TestPickImagesWithoutAnAnswerCancels checks that without App.PickImages the user cancels, headless and on a desktop target alike, rather than the system's picker opening over the test.
func TestPickImagesWithoutAnAnswerCancels(t *testing.T) {
	skipOtherTargets(t)
	app := nsgotest.Start(t, pickApp)
	app.Press("id:pick")
	app.WaitFor("text:picked 0", 5*time.Second)
}

// photoApp is a button that asks to take a photo and shows what came back.
func photoApp(cx *core.Scope) core.Node {
	got := core.NewSignal(cx, "none")
	return core.View{Children: []core.Node{
		core.View{Role: core.RoleButton, Label: "Photo", TestID: "photo", Style: core.Style{Width: core.Pt(80), Height: core.Pt(30)},
			OnPress: func(core.PressEvent) {
				cx.TakePhoto(core.PhotoOptions{}, func(photo *core.PickedFile, err error) {
					switch {
					case err != nil:
						got.Set("failed " + err.Error())
					case photo == nil:
						got.Set("cancelled")
					default:
						got.Set("took " + photo.Name)
					}
				})
			}},
		core.Text{ContentFunc: got.Get},
	}}
}

// startPhotoApp starts photoApp where the environment says the device has a camera, which a target takes from the test too (#948).
func startPhotoApp(t *testing.T) *nsgotest.App {
	t.Helper()
	return nsgotest.Start(t, photoApp, nsgotest.Environment(core.Environment{Camera: true}))
}

// TestTakePhotoAnswersTheApp checks that App.TakePhoto, set after Start as a test sets it, answers the app's photo in place of the camera (#897).
func TestTakePhotoAnswersTheApp(t *testing.T) {
	app := startPhotoApp(t)
	called := 0
	app.TakePhoto = func(core.PickRequest) (*core.PickedFile, error) {
		called++
		return &core.PickedFile{Name: "IMG_0001.jpg", MIME: "image/jpeg", Data: []byte("jpeg")}, nil
	}
	app.Press("id:photo")
	app.WaitFor("text:took IMG_0001.jpg", 5*time.Second)
	if called != 1 || len(app.Photos) != 1 {
		t.Fatalf("TakePhoto was asked %d times, %d photos recorded, want once each", called, len(app.Photos))
	}
	app.TakePhoto = func(core.PickRequest) (*core.PickedFile, error) { return nil, errors.New("camera refused") }
	app.Press("id:photo")
	app.WaitFor("text:failed camera refused", 5*time.Second)
}

// TestTakePhotoWithoutAnAnswerCancels checks that without App.TakePhoto the user cancels rather than the camera opening over the test.
func TestTakePhotoWithoutAnAnswerCancels(t *testing.T) {
	app := startPhotoApp(t)
	app.Press("id:photo")
	app.WaitFor("text:cancelled", 5*time.Second)
}
