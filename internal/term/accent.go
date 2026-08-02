package term

import (
	"fmt"
	"math"
)

// AccentWeightOnly is the AccentPick result meaning "no probed candidate is
// distinguishable from the default foreground": render the live role as dim
// (SGR 2) against normal-weight primary text instead of using a color index
// (§3.10.2a).
const AccentWeightOnly = -1

// AccentDefault is the accent used when the terminal never answers the
// palette query: ANSI 6 (cyan), safe against the common white/grey-on-dark
// and green-on-black schemes alike (§3.10.3).
const AccentDefault = 6

// DistinguishThreshold is the minimum weighted-RGB distance (Distance, range
// 0..1) at which two colors count as perceptually distinguishable. 0.12 is
// roughly the gap between a normal and bright rendition of the same ANSI hue
// — comfortably above quantisation noise, comfortably below any two distinct
// hues — chosen so bright-green-on-black body text still counts as separable
// from ANSI 2 normal green.
const DistinguishThreshold = 0.12

// Distance is a simple luminance-weighted RGB distance in [0, 1]:
// sqrt(0.30·ΔR² + 0.59·ΔG² + 0.11·ΔB²) over channels normalised to [0, 1].
// The weights are the classic NTSC luma coefficients — crude next to a Lab
// ΔE, but monotonic enough for a one-bit distinguishable/indistinguishable
// decision at DistinguishThreshold.
func Distance(a, b RGB) float64 {
	dr := (float64(a.R) - float64(b.R)) / 0xFFFF
	dg := (float64(a.G) - float64(b.G)) / 0xFFFF
	db := (float64(a.B) - float64(b.B)) / 0xFFFF
	return math.Sqrt(0.30*dr*dr + 0.59*dg*dg + 0.11*db*db)
}

// accentFallback is the §3.10.3 rev 3.1 preference order: stay inside the
// user's hue (2) whenever it reads, else cyan (6), else blue (4), else give
// up on hue and signal live by weight alone.
var accentFallback = []int{2, 6, 4}

var accentNames = map[int]string{2: "green", 4: "blue", 6: "cyan"}

// AccentPick chooses the ANSI index for the live role per §3.10.3 rev 3.1:
// prefer index 2 whenever it is perceptually distinguishable from the default
// foreground (a monochrome pane is the goal, §3.10.2a), then 6, then 4, then
// AccentWeightOnly. The needs-you role never gets a hue — it is bright +
// reverse video + the ⚑ glyph (§3.10.2a) — so no index is picked for it.
// The reasoning string is what `agentpane doctor` prints.
func AccentPick(caps Caps) (accent int, reasoning string) {
	if !caps.SupportsPaletteQuery {
		return AccentDefault, fmt.Sprintf(
			"palette query unanswered (%s timeout); defaulting to accent %d (cyan) — safe on grey-on-dark and green-on-black alike",
			ProbeTimeout, AccentDefault)
	}
	if !caps.HasFG {
		return AccentDefault, fmt.Sprintf(
			"terminal answered OSC 4 but not OSC 10 (no default fg to compare against); defaulting to accent %d (cyan)",
			AccentDefault)
	}
	skipped := ""
	for _, idx := range accentFallback {
		c, ok := caps.Palette[idx]
		if !ok {
			skipped += fmt.Sprintf("index %d unanswered; ", idx)
			continue
		}
		d := Distance(caps.FG, c)
		if d >= DistinguishThreshold {
			why := fmt.Sprintf("accent %d (%s): Δ%.2f from default fg (threshold %.2f)",
				idx, accentNames[idx], d, DistinguishThreshold)
			if idx == 2 {
				why += " — staying inside the user's hue (§3.10.2a)"
			}
			return idx, skipped + why
		}
		skipped += fmt.Sprintf("index %d (%s) indistinguishable from default fg (Δ%.2f); ", idx, accentNames[idx], d)
	}
	return AccentWeightOnly, skipped +
		"no distinguishable accent — live renders dim (SGR 2) against normal-weight primary (§3.10.2a)"
}
