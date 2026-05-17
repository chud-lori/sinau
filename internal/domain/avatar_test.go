package domain

import "testing"

func TestInitials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"two words", "Tommy Lee", "TL"},
		{"three words drops middle", "Mary Jane Watson", "MW"},
		{"single word falls back to first two letters", "Nur", "NU"},
		{"extra whitespace", "  john   doe  ", "JD"},
		{"unicode preserved", "日本人 太郎", "日太"},
		{"lowercase upcased", "alice bob", "AB"},
		{"empty falls back to ?", "", "?"},
		{"whitespace only falls back to ?", "   ", "?"},
		{"single letter", "a", "A"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Initials(c.in); got != c.want {
				t.Fatalf("Initials(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestAvatarBucketStable: same input always lands in the same slot
// (chips would otherwise flicker between page loads).
func TestAvatarBucketStable(t *testing.T) {
	id := "01H...something"
	first := AvatarBucket(id)
	for i := 0; i < 100; i++ {
		if got := AvatarBucket(id); got != first {
			t.Fatalf("AvatarBucket(%q) flapped: %d != %d", id, got, first)
		}
	}
}

func TestAvatarBucketRange(t *testing.T) {
	for _, id := range []string{"", "a", "abc", "01HXYZ", "very-long-user-identifier-string"} {
		b := AvatarBucket(id)
		if b < 0 || b >= AvatarBuckets {
			t.Fatalf("AvatarBucket(%q) = %d, out of range [0,%d)", id, b, AvatarBuckets)
		}
	}
}

// TestAvatarBucketSpread is a sanity check: hashing a handful of
// distinct IDs should land in more than one bucket. Not a strict
// uniformity guarantee — just guarding against a typo that collapses
// the hash to a constant.
func TestAvatarBucketSpread(t *testing.T) {
	seen := map[int]bool{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		seen[AvatarBucket(id)] = true
	}
	if len(seen) < 3 {
		t.Fatalf("hash collapsed to %d distinct buckets out of 12 IDs", len(seen))
	}
}
