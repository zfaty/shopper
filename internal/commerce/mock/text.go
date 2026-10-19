package mock

import (
	"strings"
	"unicode"
)

// normalize lowercases text and folds common Arabic spelling variants so
// "أقراص", "اقراص" and "اَقراص" match, and Arabic-Indic digits match ASCII.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 0x064B && r <= 0x0652, r == 0x0640: // harakat, tatweel
			continue
		case r == 'أ', r == 'إ', r == 'آ', r == 'ٱ':
			r = 'ا'
		case r == 'ة':
			r = 'ه'
		case r == 'ى':
			r = 'ي'
		case r >= '٠' && r <= '٩':
			r = '0' + (r - '٠')
		case r >= '۰' && r <= '۹':
			r = '0' + (r - '۰')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tokenize splits normalised text into words. "Vitamin D3, 1000 IU" →
// [vitamin d3 1000 iu]. "pain-relief" → [pain relief].
func tokenize(s string) []string {
	return strings.FieldsFunc(normalize(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// matchesAny reports whether any token starts with q, so "paracet" finds
// "paracetamol" and "headache" finds "headaches". One-letter queries must
// match a whole token to avoid matching everything.
func matchesAny(tokens []string, q string) bool {
	for _, t := range tokens {
		if t == q || (len([]rune(q)) >= 2 && strings.HasPrefix(t, q)) {
			return true
		}
	}
	return false
}
