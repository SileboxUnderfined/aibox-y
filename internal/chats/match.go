package chats

import (
	"strings"
	"unicode"
)

// Normalize lowercases, folds ё and drops punctuation, the way ASR text varies.
func Normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е"))
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var filler = map[string]bool{"про": true, "о": true, "об": true, "чат": true, "на": true, "по": true, "с": true, "в": true, "мой": true, "наш": true}

func words(s string) []string {
	var out []string
	for _, w := range strings.Fields(Normalize(s)) {
		if !filler[w] {
			out = append(out, w)
		}
	}
	return out
}

// Similarity scores 0..1 how well a spoken query names a chat. It tolerates
// word endings and small recognition errors.
func Similarity(query, name string) float64 {
	q, n := words(query), words(name)
	if len(q) == 0 || len(n) == 0 {
		return 0
	}
	qs, ns := strings.Join(q, " "), strings.Join(n, " ")
	if qs == ns {
		return 1
	}
	whole := ratio(qs, ns)
	total := 0.0
	for _, qw := range q {
		best := 0.0
		for _, nw := range n {
			if r := wordRatio(qw, nw); r > best {
				best = r
			}
		}
		total += best
	}
	perWord := total / float64(len(q))
	// A query covering only part of a long name is weaker evidence.
	if len(n) > len(q) {
		perWord *= 0.85 + 0.15*float64(len(q))/float64(len(n))
	}
	if whole > perWord {
		return whole
	}
	return perWord
}

// wordRatio treats a shared stem as a match: "космосе" names "космос".
func wordRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	common := 0
	for common < len(ra) && common < len(rb) && ra[common] == rb[common] {
		common++
	}
	shorter := min(len(ra), len(rb))
	if shorter >= 4 && common >= shorter-2 && common >= 4 {
		return 0.95
	}
	return ratio(a, b)
}

func ratio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	longest := max(len(ra), len(rb))
	if longest == 0 {
		return 1
	}
	return 1 - float64(levenshtein(ra, rb))/float64(longest)
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// MatchThreshold is the lowest Similarity accepted as naming a chat.
const MatchThreshold = 0.7
