// Package conv holds small, safe numeric conversions used at storage and
// external-output boundaries.
package conv

import "math"

// Int32 narrows an int to int32, clamping out-of-range values to the int32
// bounds instead of silently wrapping (which Go's plain int32(n) does). Use it
// wherever a value derived from request input or external command output is
// converted to a 32-bit column/field — e.g. a paging offset computed from an
// unbounded page number, or a resource count parsed from tofu plan output —
// so a hostile or absurd value degrades gracefully (an empty page, a saturated
// count) instead of wrapping to a negative or wrong number.
func Int32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}
