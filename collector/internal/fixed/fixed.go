// Package fixed implements exact fixed-point decimal arithmetic (scale 1e8)
// so that price/volume comparisons are exact, with no float rounding.
package fixed

import (
	"fmt"
	"strings"
)

// Scale is 1e8: enough for the 8 decimals used by Binance spot CSVs.
const Scale = 100_000_000

// Parse converts a decimal string ("64504.1", "0.15314000") to int64 * 1e8.
// It errors on more than 8 decimals instead of rounding silently.
func Parse(s string) (int64, error) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if len(fracPart) > 8 {
		return 0, fmt.Errorf("fixed: %q has more than 8 decimals", s)
	}
	fracPart += strings.Repeat("0", 8-len(fracPart))
	var v int64
	for _, c := range intPart + fracPart {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("fixed: invalid decimal %q", s)
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, nil
}

// Format renders an int64*1e8 back to a canonical decimal string,
// trimming trailing zeros in the fraction.
func Format(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%d.%08d", v/Scale, v%Scale)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if neg {
		s = "-" + s
	}
	return s
}
