package utils

import (
	"strings"
	"unicode"
)

// Tunable weights for score
const (
	containmentBase = 0.50 // Floor for any substring containment
	containmentSpan = 0.40 // Added across coverage in (0,1]
	wholeTokenBonus = 0.07 // Containment whose match lands on token boundaries both sides
	midWordPenalty  = 0.15 // Containment buried inside a larger word on both sides
	containmentCap  = 0.99 // Keep containment strictly below an exact match

	tokenBase = 0.50 // Floor when at least one whole candidate token is found
	tokenSpan = 0.45 // Added across the fraction of candidate tokens found

	editWeight = 0.60 // Edit-distance similarity scaled below other bands
)

// Scored candidate returned by Best / BestAbove
type Match struct {
	Value string  // The candidate string
	Index int     // Its position in the input slice (-1 when no match)
	Score float64 // Similarity to the query, in [0,1]
}

// Measures how closely candidate matches query, 0 to 1
func Score(query, candidate string) float64 {
	q := normalize(query)
	c := normalize(candidate)
	if q == "" || c == "" {
		return 0
	}
	if q == c {
		return 1.0
	}

	best := containmentScore(q, c)
	if s := tokenScore(q, c); s > best {
		best = s
	}
	if s := editScore(q, c); s > best {
		best = s
	}
	return best
}

// Gets highest scoring candidate for query
func Best(query string, candidates []string) (Match, bool) {
	best := Match{Index: -1}
	found := false
	for i, c := range candidates {
		s := Score(query, c)
		if !found || s > best.Score {
			best = Match{Value: c, Index: i, Score: s}
			found = true
		}
	}
	return best, found
}

// Scores substring containment weighted by coverage and token boundary fit
func containmentScore(q, c string) float64 {
	short, long := q, c
	if len(long) < len(short) {
		short, long = long, short
	}
	idx := bestContainmentIndex(long, short)
	if idx < 0 {
		return 0
	}

	coverage := float64(len(short)) / float64(len(long))
	score := containmentBase + containmentSpan*coverage

	leftBoundary := idx == 0 || !isWordByte(long[idx-1])
	rightBoundary := idx+len(short) == len(long) || !isWordByte(long[idx+len(short)])
	switch {
	case leftBoundary && rightBoundary:
		// Matched a complete token, e.g. "neoforge" within "neoforge-21.1.228"
		score += wholeTokenBonus
	case !leftBoundary && !rightBoundary:
		// Buried inside a larger word, likely spurious
		score -= midWordPenalty
	}
	return min(max(score, 0), containmentCap)
}

// Finds index of short within long with most boundary matches
func bestContainmentIndex(long, short string) int {
	best, bestRank := -1, -1
	for start := 0; ; {
		i := strings.Index(long[start:], short)
		if i < 0 {
			break
		}
		idx := start + i
		rank := 0
		if idx == 0 || !isWordByte(long[idx-1]) {
			rank++
		}
		if idx+len(short) == len(long) || !isWordByte(long[idx+len(short)]) {
			rank++
		}
		if rank > bestRank {
			best, bestRank = idx, rank
			if rank == 2 {
				break
			}
		}
		start = idx + 1
	}
	return best
}

// Rewards candidates whose words all appear in query
func tokenScore(q, c string) float64 {
	candidateTokens := tokens(c)
	if len(candidateTokens) == 0 {
		return 0
	}
	queryTokens := tokenSet(q)
	matched := 0
	for _, t := range candidateTokens {
		if queryTokens[t] {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	frac := float64(matched) / float64(len(candidateTokens))
	return tokenBase + tokenSpan*frac
}

// Scores edit-distance similarity capped by editWeight
func editScore(q, c string) float64 {
	maxLen := max(len(q), len(c))
	if maxLen == 0 {
		return 0
	}
	sim := 1.0 - float64(levenshtein(q, c))/float64(maxLen)
	if sim <= 0 {
		return 0
	}
	return editWeight * sim
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Splits s into runs of letters and digits, discarding separators
func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, t := range tokens(s) {
		set[t] = true
	}
	return set
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Computes rune-aware edit distance between a and b
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func DeduplicateStrings(strings []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, s := range strings {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
