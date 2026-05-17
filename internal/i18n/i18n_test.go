package i18n

import "testing"

func TestTFallbacks(t *testing.T) {
	if got := T(LangEN, "nav.help"); got != "Help" {
		t.Errorf("EN nav.help = %q, want %q", got, "Help")
	}
	if got := T(LangID, "nav.help"); got != "Bantuan" {
		t.Errorf("ID nav.help = %q, want %q", got, "Bantuan")
	}
	// Missing key in ID falls back to EN, then to the key itself.
	if got := T(LangID, "site.name"); got != "Sinau" {
		t.Errorf("ID site.name fallback = %q", got)
	}
	if got := T(LangID, "no.such.key"); got != "no.such.key" {
		t.Errorf("missing key should return key, got %q", got)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name, user, cookie, accept string
		want                       Lang
	}{
		{"empty falls to default", "", "", "", Default},
		{"user wins over cookie+accept", "id", "en", "en", LangID},
		{"cookie wins over accept", "", "id", "en", LangID},
		{"accept id-ID", "", "", "id-ID,id;q=0.9,en;q=0.8", LangID},
		{"accept en-US", "", "", "en-US,en;q=0.9", LangEN},
		{"accept unsupported falls through", "", "", "fr-FR,fr;q=0.9", Default},
		{"weighted en > id picks en", "", "", "id;q=0.1,en;q=0.9", LangEN},
		{"weighted id > en picks id", "", "", "en;q=0.3,id;q=0.9", LangID},
		{"invalid user falls through", "xx", "", "id", LangID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.user, tc.cookie, tc.accept); got != tc.want {
				t.Errorf("Detect(%q,%q,%q) = %q, want %q", tc.user, tc.cookie, tc.accept, got, tc.want)
			}
		})
	}
}

func TestRoleAndModeLabel(t *testing.T) {
	cases := []struct {
		lang       Lang
		mode, role string
		want       string
	}{
		{LangEN, "mentorship", "mentor", "Mentor"},
		{LangEN, "mentorship", "learner", "Learner"},
		{LangEN, "classroom", "mentor", "Teacher"},
		{LangEN, "classroom", "learner", "Student"},
		{LangID, "mentorship", "mentor", "Mentor"},
		{LangID, "mentorship", "learner", "Murid"},
		{LangID, "classroom", "mentor", "Guru"},
		{LangID, "classroom", "learner", "Siswa"},
	}
	for _, tc := range cases {
		if got := RoleLabel(tc.lang, tc.mode, tc.role); got != tc.want {
			t.Errorf("RoleLabel(%s,%s,%s) = %q, want %q", tc.lang, tc.mode, tc.role, got, tc.want)
		}
	}
	if got := ModeLabel(LangID, "classroom"); got != "Kelas" {
		t.Errorf("ID classroom = %q", got)
	}
	if got := ModeLabel(LangEN, "mentorship"); got != "Mentorship" {
		t.Errorf("EN mentorship = %q", got)
	}
}
