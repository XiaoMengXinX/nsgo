//go:build linux && !android && !nsgoautomation

package linux

func registerLiveWindow(uint32, *window) {}
func unregisterLiveWindow(*window)       {}
