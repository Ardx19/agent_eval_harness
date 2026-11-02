package provenance

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type numTok struct {
	raw      string
	val      float64
	decimals int
	percent  bool
}

var reNum = regexp.MustCompile(`\d+(?:\.\d+)*%?`)

// extractNumbers finds standalone numbers. A digit run glued to letters or
// underscores (C1CC1, req_2.json, H2O) is part of a name, and a dotted run with
// more than one dot (1.18.34) is a version, so neither counts.
func extractNumbers(text string) []numTok {
	var out []numTok
	for _, loc := range reNum.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if loc[0] > 0 && isWordish(text[loc[0]-1]) {
			continue
		}
		if loc[1] < len(text) && isWordish(text[loc[1]]) && !strings.HasSuffix(raw, "%") {
			continue
		}
		pct := strings.HasSuffix(raw, "%")
		body := strings.TrimSuffix(raw, "%")
		if strings.Count(body, ".") > 1 {
			continue
		}
		v, err := strconv.ParseFloat(body, 64)
		if err != nil {
			continue
		}
		dec := 0
		if i := strings.IndexByte(body, '.'); i >= 0 {
			dec = len(body) - i - 1
		}
		// a locant in a chemical name ("15-crown-5", "1,13-tridecanediol") is part of
		// the name, not a result
		if dec == 0 && !pct && loc[1]+1 < len(text) && text[loc[1]] == '-' && isLetter(text[loc[1]+1]) {
			continue
		}
		// "-" is a minus sign only after whitespace or an opening mark; in
		// "trans-1,2-" or "*trans*-1" it is a hyphen
		if loc[0] > 0 && text[loc[0]-1] == '-' && (loc[0] < 2 || strings.IndexByte(" \t\n([=:,", text[loc[0]-2]) >= 0) {
			v = -v
		} else if loc[0] > 0 && text[loc[0]-1] == '-' && dec == 0 && !pct {
			continue // an integer after a hyphen inside a name
		}
		out = append(out, numTok{raw: raw, val: v, decimals: dec, percent: pct})
	}
	return out
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func isWordish(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// matches reports whether a number in an answer is explained by a source value:
// it is the source rounded to the number of decimals the answer shows, or (for a
// percentage) the source expressed as a percentage.
func (t numTok) matches(g float64) bool {
	cands := []struct {
		v float64
		d int
	}{{t.val, t.decimals}}
	if t.percent {
		cands = append(cands, struct {
			v float64
			d int
		}{t.val / 100, t.decimals + 2})
	}
	for _, c := range cands {
		// the answer is explained if it is within half a unit of the last digit it
		// shows: that accepts both round-half-up and round-half-even
		if math.Abs(c.v-g) <= 0.5*math.Pow(10, -float64(c.d))+1e-9 {
			return true
		}
	}
	return false
}

type numSet []float64

func (s numSet) explains(t numTok) bool {
	for _, g := range s {
		if t.matches(g) {
			return true
		}
	}
	return false
}

func numbersOf(text string) numSet {
	var s numSet
	for _, t := range extractNumbers(text) {
		s = append(s, t.val)
	}
	return s
}

// minus removes values that also appear in other: a value the agent passed in
// and the tool merely echoed back is not evidence the tool produced it.
func (s numSet) minus(other numSet) numSet {
	var out numSet
next:
	for _, v := range s {
		for _, o := range other {
			if math.Abs(v-o) < 1e-9 {
				continue next
			}
		}
		out = append(out, v)
	}
	return out
}

// reSmiles finds candidate SMILES: runs of SMILES characters.
var reSmiles = regexp.MustCompile(`[A-Za-z0-9@+\-\[\]()=#$/\\%.]{6,}`)

// smilesTokens returns strings that look like SMILES: long enough, and either
// containing structure characters (branches, bonds, brackets) or a ring digit
// next to several carbons. Plain words and file names do not qualify.
func smilesTokens(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reSmiles.FindAllString(text, -1) {
		m = trimProse(m)
		if len(m) < 6 || seen[m] || !smilesLike(m) {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// trimProse strips punctuation that belongs to the sentence, not the structure.
// A SMILES cannot begin with "(", ")", "=", "#" or a bond character, so those
// are prose ("aspirin (CC(=O)O) is..."); a trailing ")" with no opening
// partner is the other half of the same prose parenthesis.
func trimProse(s string) string {
	s = strings.Trim(s, ".,;:")
	for len(s) > 0 && strings.ContainsRune("()=#-+/\\%", rune(s[0])) {
		s = s[1:]
	}
	for len(s) > 0 && strings.Count(s, ")") > strings.Count(s, "(") && s[len(s)-1] == ')' {
		s = s[:len(s)-1]
	}
	return strings.Trim(s, ".,;:")
}

// reSmilesGrammar accepts only strings made entirely of SMILES tokens: bracket
// atoms, organic-subset atoms (aromatic in lower case), bonds, branches, ring
// closures, dots and chirality marks. Words fail it: "ethyl" and "phenyl" contain
// letters that are not atoms, which is what separates a name from a structure.
// A bracket atom: optional isotope, an element (or aromatic) symbol, optional
// chirality, hydrogen count, charge and atom-map class: [13CH3], [C@@H], [NH3+], [O-].
const bracketAtom = `\[\d*(?:[A-Z][a-z]?|[a-z]{1,2}|\*)(?:@{1,2}(?:TH\d|AL\d|SP\d|TB\d+|OH\d+)?)?(?:H\d*)?(?:[+-]\d*|\++|-{2,})?(?::\d+)?\]`

// reSmilesGrammar accepts only strings made entirely of SMILES tokens: bracket
// atoms, organic-subset atoms (aromatic in lower case), bonds, branches, ring
// closures, dots and chirality marks. Words fail it: "ethyl" and "phenyl" contain
// letters that are not atoms, which is what separates a name from a structure.
var reSmilesGrammar = regexp.MustCompile(`^(?:` + bracketAtom + `|Br|Cl|[BCNOPSFI]|[bcnops]|[-=#$:/\\.()]|%\d\d|\d)+$`)

func smilesLike(s string) bool {
	if !reSmilesGrammar.MatchString(s) {
		return false
	}
	if strings.ContainsAny(s, "()=#[]") {
		return strings.ContainsAny(s, "CcNnOoSsPpFfIiBb")
	}
	digits, carbons := 0, 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		}
		if r == 'C' || r == 'c' {
			carbons++
		}
	}
	return digits > 0 && carbons >= 2
}

// looksLikeWordWithBrackets rejects prose such as "route(s)" or "step(2)".
func looksLikeWordWithBrackets(s string) bool {
	letters := 0
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	return letters > 4 && !strings.ContainsAny(s, "=#[")
}

// stringLeaves returns every string value inside a JSON document, or the raw
// text when it is not JSON. Tool results reach the model wrapped in JSON, so
// this recovers the text the model actually read.
func stringLeaves(content string) []string {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return []string{content}
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case float64, bool:
			// numbers inside a JSON wrapper are rendered into the text as well
			b, _ := json.Marshal(t)
			out = append(out, string(b))
		}
	}
	walk(v)
	return out
}
