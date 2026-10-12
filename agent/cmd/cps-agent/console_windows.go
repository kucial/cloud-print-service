//go:build windows

package main

import "golang.org/x/sys/windows"

// disableQuickEdit turns off QuickEdit for the agent's console window. With it on, a
// single click starts a text selection, and Windows pauses every write to the console
// until it ends — which stalled the agent mid-heartbeat on a real packing PC.
func disableQuickEdit() {
	in, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if windows.GetConsoleMode(in, &mode) != nil {
		return // not a console (redirected, or a service)
	}
	_ = windows.SetConsoleMode(in, (mode&^windows.ENABLE_QUICK_EDIT_MODE)|windows.ENABLE_EXTENDED_FLAGS)
}
