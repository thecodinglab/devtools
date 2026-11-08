package hub

import (
	"strings"
	"unicode"
)

// match reports whether every space separated term of query occurs in s as
// a subsequence, ignoring case, the way fzf matches. The score favours runs
// of consecutive letters and letters that start a word; positions are the
// rune indices of s that matched.
func match(query, s string) (int, []int, bool) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 0, nil, true
	}
	text := []rune(strings.ToLower(s))
	total := 0
	var positions []int
	for _, term := range terms {
		score, pos, ok := matchTerm([]rune(term), text)
		if !ok {
			return 0, nil, false
		}
		total += score
		positions = append(positions, pos...)
	}
	return total, dedupe(positions), true
}

// matchTerm tries every place the term's first letter occurs and keeps the
// best scoring greedy match from there.
func matchTerm(term, text []rune) (int, []int, bool) {
	best, bestPositions, found := 0, []int(nil), false
	for start := range text {
		if text[start] != term[0] {
			continue
		}
		score, positions, ok := greedy(term, text, start)
		if ok && (!found || score > best) {
			best, bestPositions, found = score, positions, true
		}
	}
	return best, bestPositions, found
}

func greedy(term, text []rune, start int) (int, []int, bool) {
	positions := make([]int, 0, len(term))
	score := 0
	i := start
	for _, r := range term {
		for i < len(text) && text[i] != r {
			i++
		}
		if i == len(text) {
			return 0, nil, false
		}
		score += 1
		if i == 0 || isBoundary(text[i-1]) {
			score += 8
		}
		if n := len(positions); n > 0 {
			if positions[n-1] == i-1 {
				score += 6
			} else {
				score -= min(i-positions[n-1]-1, 4)
			}
		}
		positions = append(positions, i)
		i++
	}
	return score, positions, true
}

func isBoundary(r rune) bool {
	return r == '/' || r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)
}

func dedupe(positions []int) []int {
	seen := make(map[int]bool, len(positions))
	out := positions[:0]
	for _, p := range positions {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
