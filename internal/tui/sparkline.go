package tui

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline renders vals (oldest first) as a one-line block chart clipped to
// the last `width` points.
func sparkline(vals []float64, width int) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	out := make([]rune, len(vals))
	for i, v := range vals {
		if span == 0 {
			out[i] = sparkBlocks[0]
			continue
		}
		idx := int((v - lo) / span * float64(len(sparkBlocks)-1))
		out[i] = sparkBlocks[idx]
	}
	return string(out)
}
