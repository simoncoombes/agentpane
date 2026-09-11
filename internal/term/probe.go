package term

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"time"

	xterm "github.com/charmbracelet/x/term"
)

// ProbeTimeout bounds the whole palette query round-trip (§3.10.3).
const ProbeTimeout = 200 * time.Millisecond

// PaletteIndices are the ANSI indices queried via OSC 4. §3.10.3 names
// 1, 2, 3, 5, 6; index 4 is probed as well because the accent fallback chain
// (2 → 6 → 4, §3.10.3 rev 3.1) needs its value to judge distinguishability.
var PaletteIndices = []int{1, 2, 3, 4, 5, 6}

// RGB is a 16-bit-per-channel color, the native resolution of an OSC
// rgb:RRRR/GGGG/BBBB reply. Shorter replies are scaled up.
type RGB struct {
	R, G, B uint16
}

// Caps is the result of one terminal probe.
type Caps struct {
	// SupportsPaletteQuery is true when the terminal answered at least one
	// OSC 4 or OSC 10 query before the timeout.
	SupportsPaletteQuery bool
	// FG is the default foreground (OSC 10 reply); valid only when HasFG.
	FG    RGB
	HasFG bool
	// Palette holds the ANSI indices that answered, keyed by index.
	Palette map[int]RGB
}

// Probe queries the terminal for its default foreground (OSC 10) and the
// palette entries in PaletteIndices (OSC 4), waiting at most ProbeTimeout.
// tty must be the controlling terminal opened read-write; the probe puts it
// in raw mode for the duration. Any failure — not a tty, raw mode refused,
// no reply — returns zero Caps: the caller falls back per §3.10.3.
func Probe(tty *os.File) Caps {
	var caps Caps
	if tty == nil {
		return caps
	}
	fd := tty.Fd()
	oldState, err := xterm.MakeRaw(fd)
	if err != nil {
		return caps
	}
	defer xterm.Restore(fd, oldState) //nolint:errcheck // best effort

	var q bytes.Buffer
	for _, idx := range PaletteIndices {
		fmt.Fprintf(&q, "\x1b]4;%d;?\a", idx)
	}
	q.WriteString("\x1b]10;?\a")
	// DA1 terminates the read early: every terminal answers it, and its reply
	// is ordered after the OSC replies (or after their absence).
	q.WriteString("\x1b[c")
	if _, err := tty.Write(q.Bytes()); err != nil {
		return caps
	}

	buf := readUntil(tty, ProbeTimeout)
	fg, hasFG, palette := parseReplies(buf)
	caps.FG = fg
	caps.HasFG = hasFG
	caps.Palette = palette
	caps.SupportsPaletteQuery = hasFG || len(palette) > 0
	return caps
}

// readUntil reads from tty until the DA1 reply (CSI ... c) arrives or the
// deadline passes, whichever is first.
func readUntil(tty *os.File, timeout time.Duration) []byte {
	deadline := time.Now().Add(timeout)
	if err := tty.SetReadDeadline(deadline); err != nil {
		// No deadline support on this handle: do one bounded read and stop
		// rather than risk blocking a render.
		return nil
	}
	defer tty.SetReadDeadline(time.Time{}) //nolint:errcheck

	var out []byte
	chunk := make([]byte, 256)
	for {
		n, err := tty.Read(chunk)
		out = append(out, chunk[:n]...)
		if err != nil || hasDA1Reply(out) {
			return out
		}
	}
}

// hasDA1Reply reports whether buf contains a primary device attributes reply
// (ESC [ ? ... c), which the probe uses as an end-of-answers marker.
func hasDA1Reply(buf []byte) bool {
	for i := 0; i+2 < len(buf); i++ {
		if buf[i] != 0x1b || buf[i+1] != '[' || buf[i+2] != '?' {
			continue
		}
		for j := i + 3; j < len(buf); j++ {
			c := buf[j]
			if c == 'c' {
				return true
			}
			if !(c >= '0' && c <= '9') && c != ';' {
				break
			}
		}
	}
	return false
}

// parseReplies extracts OSC 10 and OSC 4 answers from a raw reply buffer.
// Malformed or truncated sequences are skipped, never fatal (C9).
func parseReplies(buf []byte) (fg RGB, hasFG bool, palette map[int]RGB) {
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] != 0x1b || buf[i+1] != ']' {
			continue
		}
		body, next := oscBody(buf, i+2)
		if body == "" {
			continue
		}
		if rest, ok := cutPrefix(body, "10;"); ok {
			if c, ok := parseColorSpec(rest); ok {
				fg, hasFG = c, true
			}
		} else if rest, ok := cutPrefix(body, "4;"); ok {
			semi := bytes.IndexByte([]byte(rest), ';')
			if semi > 0 {
				idx, err := strconv.Atoi(rest[:semi])
				if err == nil {
					if c, ok := parseColorSpec(rest[semi+1:]); ok {
						if palette == nil {
							palette = make(map[int]RGB)
						}
						palette[idx] = c
					}
				}
			}
		}
		i = next - 1
	}
	return fg, hasFG, palette
}

// oscBody returns the OSC payload starting at buf[start] up to its BEL or
// ESC \ terminator, and the offset just past the terminator. An unterminated
// sequence returns ("", len(buf)).
func oscBody(buf []byte, start int) (string, int) {
	for j := start; j < len(buf); j++ {
		switch {
		case buf[j] == 0x07:
			return string(buf[start:j]), j + 1
		case buf[j] == 0x1b && j+1 < len(buf) && buf[j+1] == '\\':
			return string(buf[start:j]), j + 2
		}
	}
	return "", len(buf)
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

// parseColorSpec parses an XParseColor-style rgb:RRRR/GGGG/BBBB spec with one
// to four hex digits per channel, scaling each channel to 16 bits. rgba:
// specs are accepted with the alpha channel ignored.
func parseColorSpec(s string) (RGB, bool) {
	rest, ok := cutPrefix(s, "rgb:")
	if !ok {
		rest, ok = cutPrefix(s, "rgba:")
		if !ok {
			return RGB{}, false
		}
	}
	parts := splitSlash(rest)
	if len(parts) < 3 {
		return RGB{}, false
	}
	var ch [3]uint16
	for i := 0; i < 3; i++ {
		v, ok := parseChannel(parts[i])
		if !ok {
			return RGB{}, false
		}
		ch[i] = v
	}
	return RGB{ch[0], ch[1], ch[2]}, true
}

func splitSlash(s string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return parts
}

// parseChannel scales an n-digit hex channel (1 ≤ n ≤ 4) to 16 bits:
// v * 0xFFFF / (16^n − 1), so "c0" → 0xC0C0 and "f" → 0xFFFF.
func parseChannel(s string) (uint16, bool) {
	if len(s) == 0 || len(s) > 4 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	max := uint64(1)<<(4*uint(len(s))) - 1
	return uint16(v * 0xFFFF / max), true
}
