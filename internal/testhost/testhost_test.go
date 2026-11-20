package testhost

import (
	"testing"

	"github.com/nsgo-dev/nsgo/internal/driver"
)

func TestPinSetsTheTestsDisplaySettings(t *testing.T) {
	platform := driver.Environment{Dark: true, ReduceMotion: true, IncreaseContrast: true, LaunchedAtLogin: true, FrontmostApp: driver.FrontmostAppInfo{ID: "com.apple.Terminal", PID: 7}, CanReadFrontmostApp: true, OS: driver.OSMacOS, Locale: "en-US", FontScale: 1, DeviceName: "Ada's Mac", DeviceModel: "Mac15,7"}
	got := Pin(platform, &driver.Environment{IncreaseContrast: false})
	if got.Dark || got.ReduceMotion || got.IncreaseContrast || got.LaunchedAtLogin || got.FrontmostApp != (driver.FrontmostAppInfo{}) || got.CanReadFrontmostApp || got.OS != driver.OSMacOS || got.Locale != "en-US" || got.FontScale != 1 || got.DeviceName != "Ada's Mac" || got.DeviceModel != "Mac15,7" {
		t.Errorf("pinned off: %+v", got)
	}
	got = Pin(driver.Environment{OS: driver.OSMacOS}, &driver.Environment{Dark: true, ReduceMotion: true, IncreaseContrast: true, LaunchedAtLogin: true, FrontmostApp: driver.FrontmostAppInfo{ID: "editor", FullScreen: true}, CanReadFrontmostApp: true, DeviceName: "Test phone", DeviceModel: "Pixel 8"})
	if !got.Dark || !got.ReduceMotion || !got.IncreaseContrast || !got.LaunchedAtLogin || got.FrontmostApp != (driver.FrontmostAppInfo{ID: "editor", FullScreen: true}) || !got.CanReadFrontmostApp || got.DeviceName != "Test phone" || got.DeviceModel != "Pixel 8" {
		t.Errorf("pinned on: %+v", got)
	}
}

// A test's camera stands over a platform without one, and a test that sets none keeps the platform's, as its photos then come from the platform's camera rules (#948).
func TestPinGivesTheTestsCamera(t *testing.T) {
	if got := Pin(driver.Environment{OS: driver.OSMacOS}, &driver.Environment{Camera: true}); !got.Camera {
		t.Errorf("a test's camera was not pinned: %+v", got)
	}
	if got := Pin(driver.Environment{OS: driver.OSIOS, Camera: true}, &driver.Environment{}); !got.Camera {
		t.Errorf("a phone's own camera was taken away: %+v", got)
	}
}
