//go:build linux && !android

package linux

// initDisplay checks the display as well as GTK's initialization result.
// GTK 4.14 marks itself initialized before opening the display, so a retry
// after a failed connection can return true without a usable display.
func initDisplay() bool {
	return gtkInitCheck() != 0 && gdkDisplayGetDefault() != 0
}
