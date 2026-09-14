package main

// console_windows.go — the terminal width, read from the console rather than
// from stdout.
//
// A hook's stdout is a pipe: Claude Code reads it. So the usual
// "measure fd 1" trick reports nothing, and the width has to come from the
// console device itself. CONOUT$ is the documented handle for exactly this —
// the active screen buffer of the console this process is attached to,
// reachable even when all three standard handles have been redirected.

import (
	"golang.org/x/sys/windows"
)

// platformConsoleColumns returns the console's visible width in columns, or 0
// when there is no console (a genuinely headless run) or it cannot be read.
//
// The width comes from the WINDOW rectangle, not from the screen buffer size:
// a console with scrollback has a buffer taller and sometimes wider than what
// is on screen, and splitting against the buffer would size the pane for
// space the user cannot see.
func platformConsoleColumns() int {
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONOUT$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h) //nolint:errcheck // nothing to do with a close failure here

	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return 0
	}
	cols := int(info.Window.Right) - int(info.Window.Left) + 1
	if cols <= 0 {
		return 0
	}
	return cols
}
