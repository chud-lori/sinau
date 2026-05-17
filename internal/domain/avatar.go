package domain

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// AvatarBuckets is the number of distinct color slots a chip can land
// in. Templates pair "avatar-c0" through "avatar-c{AvatarBuckets-1}"
// with the colour palette defined in static/app.css.
//
// Eight is a sweet spot: enough variety that a single room rarely shows
// two chips the same colour, few enough that the palette stays
// hand-tunable.
const AvatarBuckets = 8

// Initials builds a 1-2 letter monogram from a display name for the
// initials-chip avatar. Whitespace-only or empty input falls back to
// "?", so the chip always renders something.
//
// Examples:
//
//	"Tommy Lee"          -> "TL"
//	"Mary Jane Watson"   -> "MW"   (first + last word — middle is dropped)
//	"Nur"                -> "NU"   (single word — first two letters)
//	"  john   doe  "     -> "JD"
//	"日本人 太郎"          -> "日太"  (unicode survives intact)
//	""                   -> "?"
func Initials(name string) string {
	fields := strings.FieldsFunc(name, unicode.IsSpace)
	switch len(fields) {
	case 0:
		return "?"
	case 1:
		return firstTwoUpper(fields[0])
	default:
		return firstUpper(fields[0]) + firstUpper(fields[len(fields)-1])
	}
}

func firstUpper(s string) string {
	for _, r := range s {
		return string(unicode.ToUpper(r))
	}
	return ""
}

func firstTwoUpper(s string) string {
	var out []rune
	for _, r := range s {
		out = append(out, unicode.ToUpper(r))
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

// AvatarBucket maps a stable identifier (typically users.id) to a
// color slot in [0, AvatarBuckets). FNV-1a 32-bit is plenty for a UI
// hash — not cryptographic, just decent distribution.
//
// Empty input falls back to bucket 0 so the chip still renders.
func AvatarBucket(id string) int {
	if id == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % uint32(AvatarBuckets))
}
