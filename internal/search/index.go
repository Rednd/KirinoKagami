// Package search implements the in-memory search index and result cache.
//
// The index holds a compact entry per beatmapset (lower-cased text, status,
// mode mask, favourites) plus an inverted index from token -> sets. Queries
// are answered with bitset intersections of the posting lists for each
// query term (prefix matching), then the matching IDs are hydrated from the
// database. The index is rebuilt from the database by a cron job and saved
// to disk so restarts are instant.
package search

import (
	mbits "math/bits"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entry is the search-relevant data of one beatmapset. Strings are lower-case.
type Entry struct {
	ID         int32
	Status     int8
	Modes      uint8
	Favourites int32
	Title      string
	Artist     string
	Creator    string
	Extra      string // unicode title/artist, source, tags
}

type Index struct {
	entries  []Entry  // ordered by ID descending
	tokens   []string // sorted, unique
	offsets  []uint32 // postings for tokens[i] are postings[offsets[i]:offsets[i+1]]
	postings []uint32 // entry indices, ascending
	builtAt  time.Time
}

// Build creates an index from entries. entries is taken over by the index.
func Build(entries []Entry, builtAt time.Time) *Index {
	if !slices.IsSortedFunc(entries, func(a, b Entry) int { return int(b.ID) - int(a.ID) }) {
		slices.SortFunc(entries, func(a, b Entry) int { return int(b.ID) - int(a.ID) })
	}
	lists := make(map[string][]uint32, len(entries)*2)
	add := func(i uint32) func(string) {
		return func(tok string) {
			l := lists[tok]
			if n := len(l); n > 0 && l[n-1] == i {
				return
			}
			lists[tok] = append(l, i)
		}
	}
	for i := range entries {
		e := &entries[i]
		fn := add(uint32(i))
		tokenize(e.Title, fn)
		tokenize(e.Artist, fn)
		tokenize(e.Creator, fn)
		tokenize(e.Extra, fn)
	}

	ix := &Index{entries: entries, builtAt: builtAt}
	ix.tokens = make([]string, 0, len(lists))
	total := 0
	for tok, l := range lists {
		ix.tokens = append(ix.tokens, tok)
		total += len(l)
	}
	sort.Strings(ix.tokens)
	ix.offsets = make([]uint32, len(ix.tokens)+1)
	ix.postings = make([]uint32, 0, total)
	for i, tok := range ix.tokens {
		ix.offsets[i] = uint32(len(ix.postings))
		ix.postings = append(ix.postings, lists[tok]...)
	}
	ix.offsets[len(ix.tokens)] = uint32(len(ix.postings))
	return ix
}

func (ix *Index) Len() int           { return len(ix.entries) }
func (ix *Index) Tokens() int        { return len(ix.tokens) }
func (ix *Index) BuiltAt() time.Time { return ix.builtAt }

// Params are the cheesegull /api/search parameters.
type Params struct {
	Query  string
	Status []int // empty = any
	Modes  []int // empty = any
	Amount int
	Offset int
}

type filter struct {
	status   [8]bool // index = status + 2
	anyState bool
	modeMask uint8
}

func newFilter(p Params) filter {
	f := filter{anyState: len(p.Status) == 0}
	for _, s := range p.Status {
		if s >= -2 && s <= 5 {
			f.status[s+2] = true
		}
	}
	for _, m := range p.Modes {
		if m >= 0 && m < 8 {
			f.modeMask |= 1 << uint(m)
		}
	}
	return f
}

func (f *filter) match(e *Entry) bool {
	if !f.anyState {
		s := int(e.Status) + 2
		if s < 0 || s >= len(f.status) || !f.status[s] {
			return false
		}
	}
	return f.modeMask == 0 || e.Modes&f.modeMask != 0
}

// Query returns the IDs for one page of results.
func (ix *Index) Query(p Params) []int {
	f := newFilter(p)
	terms := queryTerms(p.Query)

	if len(terms) == 0 {
		// No text: newest sets first, which is the entry order already.
		out := make([]int, 0, p.Amount)
		skip := p.Offset
		for i := range ix.entries {
			e := &ix.entries[i]
			if !f.match(e) {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, int(e.ID))
			if len(out) >= p.Amount {
				break
			}
		}
		return out
	}

	words := (len(ix.entries) + 63) / 64
	var acc, cur []uint64
	for _, term := range terms {
		if cur == nil {
			cur = make([]uint64, words)
		} else {
			clear(cur)
		}
		found := false
		for k := sort.SearchStrings(ix.tokens, term); k < len(ix.tokens) && strings.HasPrefix(ix.tokens[k], term); k++ {
			for _, e := range ix.postings[ix.offsets[k]:ix.offsets[k+1]] {
				cur[e>>6] |= 1 << (e & 63)
			}
			found = true
		}
		if !found {
			return []int{}
		}
		if acc == nil {
			acc, cur = cur, nil
		} else {
			for w := range acc {
				acc[w] &= cur[w]
			}
		}
	}

	type hit struct {
		idx   uint32
		score int32
	}
	full := strings.Join(terms, " ")
	var hits []hit
	for w, bits := range acc {
		for bits != 0 {
			b := uint32(mbits.TrailingZeros64(bits))
			bits &= bits - 1
			i := uint32(w)*64 + b
			e := &ix.entries[i]
			if !f.match(e) {
				continue
			}
			hits = append(hits, hit{i, score(e, terms, full)})
		}
	}
	if p.Offset >= len(hits) {
		return []int{}
	}
	slices.SortFunc(hits, func(a, b hit) int {
		if a.score != b.score {
			return int(b.score - a.score)
		}
		ea, eb := &ix.entries[a.idx], &ix.entries[b.idx]
		if ea.Favourites != eb.Favourites {
			return int(eb.Favourites - ea.Favourites)
		}
		return int(eb.ID - ea.ID)
	})
	end := min(p.Offset+p.Amount, len(hits))
	out := make([]int, 0, end-p.Offset)
	for _, h := range hits[p.Offset:end] {
		out = append(out, int(ix.entries[h.idx].ID))
	}
	return out
}

// score ranks a matching set: matches in the title weigh most, then artist,
// then creator, then tags/source.
func score(e *Entry, terms []string, full string) int32 {
	var s int32
	for _, t := range terms {
		switch {
		case strings.Contains(e.Title, t):
			s += 4
		case strings.Contains(e.Artist, t):
			s += 3
		case strings.Contains(e.Creator, t):
			s += 2
		default:
			s++
		}
	}
	if len(terms) > 1 {
		if strings.Contains(e.Title, full) {
			s += 6
		} else if strings.Contains(e.Artist, full) {
			s += 4
		}
	}
	if e.Title == full {
		s += 6
	}
	return s
}

// queryTerms tokenizes a query the same way entries are tokenized.
func queryTerms(q string) []string {
	var terms []string
	tokenize(strings.ToLower(q), func(t string) {
		if !slices.Contains(terms, t) {
			terms = append(terms, t)
		}
	})
	return terms
}

// isCJK reports runes from scripts written without spaces; each such rune
// becomes its own token so substrings of e.g. Japanese titles are found.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai)
}

// tokenize splits lower-cased s into letter/digit runs; CJK runes are
// emitted individually.
func tokenize(s string, fn func(string)) {
	start := -1
	for i, r := range s {
		switch {
		case isCJK(r):
			if start >= 0 {
				fn(s[start:i])
				start = -1
			}
			fn(s[i : i+utf8.RuneLen(r)])
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if start < 0 {
				start = i
			}
		default:
			if start >= 0 {
				fn(s[start:i])
				start = -1
			}
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}
