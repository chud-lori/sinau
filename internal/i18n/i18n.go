// Package i18n is Sinau's tiny localisation layer: two locales (en, id),
// flat string keys, English as the fallback. Locales are baked into the
// binary as Go maps — no file loading, no template parsing of locale files,
// no external dependency.
//
// Lookup precedence at the HTTP boundary is:
//
//  1. The user's saved preference (users.language).
//  2. The sinau_lang cookie (anonymous visitors and pre-login choice).
//  3. The Accept-Language header.
//  4. English.
//
// Templates call {{t "key"}}, which is wired in web.Server.render via a
// per-request FuncMap.
package i18n

import (
	"fmt"
	"strings"
)

// Lang is a BCP-47-ish language tag. We only support two values.
type Lang string

const (
	LangEN  Lang = "en"
	LangID  Lang = "id"
	Default Lang = LangEN
)

// Supported lists locales that have first-class translations.
var Supported = []Lang{LangEN, LangID}

// IsValid reports whether l is one of the supported locales.
func IsValid(l Lang) bool {
	switch l {
	case LangEN, LangID:
		return true
	}
	return false
}

// Normalize maps an arbitrary input string to a supported Lang. Unknown
// values fall back to the default locale.
func Normalize(s string) Lang {
	l := Lang(strings.ToLower(strings.TrimSpace(s)))
	if IsValid(l) {
		return l
	}
	return Default
}

// translations is filled by en.go / id.go at init time.
var translations = map[Lang]map[string]string{}

// register is called from per-locale files to add their map. Keeps each
// locale file independent and the package import graph trivial.
func register(l Lang, m map[string]string) {
	translations[l] = m
}

// T returns the translated string for key in the target language. Missing
// keys fall back to English; keys missing in English too return the key
// itself so the gap is visible in the rendered page instead of silently
// emitting an empty string.
func T(lang Lang, key string) string {
	if !IsValid(lang) {
		lang = Default
	}
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if lang != Default {
		if m, ok := translations[Default]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
	}
	return key
}

// Tf is T with fmt.Sprintf-style substitution applied to the resolved
// string. Useful for "due in %s" style messages.
func Tf(lang Lang, key string, args ...any) string {
	return fmt.Sprintf(T(lang, key), args...)
}

// Detect picks the best language from the available signals. Pass the
// empty string for any signal that is absent. Returns the default locale
// when nothing matches.
func Detect(userLang, cookieLang, acceptLanguage string) Lang {
	if l := Lang(strings.ToLower(strings.TrimSpace(userLang))); IsValid(l) {
		return l
	}
	if l := Lang(strings.ToLower(strings.TrimSpace(cookieLang))); IsValid(l) {
		return l
	}
	if l := parseAcceptLanguage(acceptLanguage); l != "" {
		return l
	}
	return Default
}

// parseAcceptLanguage finds the highest-quality supported language in an
// Accept-Language header. It is intentionally lax: we only support en and
// id, so all we need to do is spot which one wins.
//
// Example headers we handle:
//
//	"id"            → id
//	"id-ID,id;q=0.9,en;q=0.8" → id
//	"en-US,en;q=0.9" → en
//	"fr,de;q=0.8"   → "" (no supported match; caller falls back to default)
func parseAcceptLanguage(header string) Lang {
	if header == "" {
		return ""
	}
	type candidate struct {
		lang Lang
		q    float64
	}
	var best candidate
	for _, raw := range strings.Split(header, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			tag = strings.TrimSpace(part[:i])
			rest := part[i+1:]
			if j := strings.Index(rest, "q="); j >= 0 {
				_, _ = fmt.Sscanf(rest[j+2:], "%f", &q)
			}
		}
		// Match on the primary subtag so "id-ID" and "en-US" still resolve.
		primary := strings.ToLower(tag)
		if k := strings.Index(primary, "-"); k > 0 {
			primary = primary[:k]
		}
		var l Lang
		switch primary {
		case "id":
			l = LangID
		case "en":
			l = LangEN
		default:
			continue
		}
		if l != "" && q > best.q-0.0001 && (best.lang == "" || q > best.q) {
			best = candidate{l, q}
		}
	}
	return best.lang
}

// Label returns the human-readable display name of l, in l's own language.
// Used by the picker so it always shows native names.
func Label(l Lang) string {
	switch l {
	case LangEN:
		return "English"
	case LangID:
		return "Bahasa Indonesia"
	}
	return string(l)
}

// RoleLabel returns the user-facing label for a (mode, role) pair in the
// target language. Centralising this here means templates never branch on
// raw role/mode strings to pick a translation.
func RoleLabel(lang Lang, mode, role string) string {
	classroom := mode == "classroom"
	switch role {
	case "mentor":
		if classroom {
			return T(lang, "role.teacher")
		}
		return T(lang, "role.mentor")
	case "mentee":
		if classroom {
			return T(lang, "role.student")
		}
		return T(lang, "role.mentee")
	}
	return role
}

// ModeLabel returns the room mode label in the target language.
func ModeLabel(lang Lang, mode string) string {
	switch mode {
	case "classroom":
		return T(lang, "mode.classroom")
	case "mentorship":
		return T(lang, "mode.mentorship")
	}
	return mode
}
