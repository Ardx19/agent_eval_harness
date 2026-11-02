// Package provenance decides, from evidence the agent cannot alter, whether
// what the agent said it did actually happened.
package provenance

import (
	"path"
	"strings"
)

// Invocation is a tool invoked inside a shell command the model ran.
type Invocation struct {
	Argv0 string   // basename of the command word, e.g. run_retrosynthesis.sh
	Args  []string // as the shell would pass them
	// Consumed is true when the command's stdout is piped onward or redirected to a
	// file, so what the model read afterwards is derived output, not the tool's own.
	Consumed bool
}

// token is a shell word or an operator.
type token struct {
	text string
	op   bool
}

// tokenize splits a command line the way a shell would, well enough to find
// simple commands: quotes and backslashes are honoured, and the operators
// | || & && ; newline > >> < are separate tokens. It does not expand anything.
func tokenize(s string) []token {
	var out []token
	var cur strings.Builder
	has := false
	var pending []heredoc
	flush := func() {
		if has {
			out = append(out, token{text: cur.String()})
			cur.Reset()
			has = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			has = true
		case c == '\'':
			has = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case c == '"':
			has = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
			}
		case c == ' ' || c == '\t' || c == '\r':
			flush()
		case c == '\n' || c == ';':
			flush()
			out = append(out, token{text: ";", op: true})
			if c == '\n' && len(pending) > 0 {
				i = skipHeredocBodies(rs, i+1, pending) - 1
				pending = nil
			}
		case c == '|' || c == '&':
			flush()
			if i+1 < len(rs) && rs[i+1] == c {
				i++
				out = append(out, token{text: string(c) + string(c), op: true})
			} else {
				out = append(out, token{text: string(c), op: true})
			}
		case c == '<' && i+2 < len(rs) && rs[i+1] == '<' && rs[i+2] != '<':
			// heredoc: "<< EOF" or "<<-'EOF'". The delimiter is not an argument and the
			// body, which starts on the next line, is stdin, not commands.
			flush()
			i += 2
			dash := false
			if i < len(rs) && rs[i] == '-' {
				dash = true
				i++
			}
			for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
				i++
			}
			var d strings.Builder
			for ; i < len(rs) && !strings.ContainsRune(" \t\n;|&<>", rs[i]); i++ {
				if rs[i] != '\'' && rs[i] != '"' && rs[i] != '\\' {
					d.WriteRune(rs[i])
				}
			}
			i--
			pending = append(pending, heredoc{delim: d.String(), dash: dash})
			out = append(out, token{text: "<<heredoc", op: true})
		case c == '<' && i+2 < len(rs) && rs[i+1] == '<' && rs[i+2] == '<':
			flush()
			i += 2
			out = append(out, token{text: "<<<", op: true})
		case c == '>' || c == '<':
			// a digit just before a redirection ("2>") is part of it, not an argument
			fd := ""
			if has && isDigits(cur.String()) {
				fd = cur.String()
				cur.Reset()
				has = false
			} else {
				flush()
			}
			op := fd + string(c)
			if c == '>' && i+1 < len(rs) && rs[i+1] == '>' {
				i++
				op = fd + ">>"
			}
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++
				op += "&"
			}
			out = append(out, token{text: op, op: true})
		default:
			cur.WriteRune(c)
			has = true
		}
	}
	flush()
	return out
}

type heredoc struct {
	delim string
	dash  bool
}

// skipHeredocBodies returns the index just after the last heredoc terminator.
func skipHeredocBodies(rs []rune, from int, docs []heredoc) int {
	i := from
	for _, d := range docs {
		for i < len(rs) {
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			line := string(rs[i:j])
			if d.dash {
				line = strings.TrimLeft(line, "\t")
			}
			i = j + 1
			if line == d.delim {
				break
			}
		}
	}
	if i > len(rs) {
		i = len(rs)
	}
	return i
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var sepOps = map[string]bool{";": true, "|": true, "||": true, "&": true, "&&": true}
var wrappers = map[string]bool{"sudo": true, "env": true, "time": true, "nohup": true, "exec": true, "command": true, "nice": true, "timeout": true}

func isRedirect(op string) bool {
	return strings.HasSuffix(strings.TrimRight(op, "&"), ">") || strings.HasSuffix(strings.TrimRight(op, "&"), ">>") || strings.HasSuffix(strings.TrimRight(op, "&"), "<")
}

// redirectsStdout is true for ">" ">>" "1>" "1>>" (to a file), not for "2>" or ">&".
func redirectsStdout(op string) bool {
	switch op {
	case ">", ">>", "1>", "1>>":
		return true
	}
	return false
}

// Invocations returns every simple command whose command word is exactly one of
// the entrypoint paths (compared after cleaning, so "./scripts/tool.sh" matches
// "scripts/tool.sh"). Matching the path and not just the file name matters: a
// look-alike at another path ("/usr/share/x/scripts/tool.sh") is a different
// file, and a failed attempt at it must not be paired with a later real call.
// "echo scripts/tool.sh" is not an invocation; "echo x | scripts/tool.sh --a" is.
func Invocations(command string, entrypoints []string) []Invocation {
	want := map[string]bool{}
	for _, e := range entrypoints {
		want[path.Clean(e)] = true
	}
	type simple struct {
		words    []string
		consumed bool
	}
	var cmds []simple
	cur := simple{}
	toks := tokenize(command)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.op && sepOps[t.text]:
			cur.consumed = cur.consumed || t.text == "|"
			cmds, cur = append(cmds, cur), simple{}
		case t.op && t.text == "<<heredoc":
			// the delimiter and body were consumed by the tokenizer; nothing to skip
		case t.op && isRedirect(t.text):
			if redirectsStdout(t.text) {
				cur.consumed = true
			}
			i++ // the redirection target is not an argument
		case t.op:
		default:
			cur.words = append(cur.words, t.text)
		}
	}
	cmds = append(cmds, cur)
	var out []Invocation
	for _, c := range cmds {
		words := c.words
		w := 0
		for w < len(words) && (isAssignment(words[w]) || wrappers[path.Base(words[w])]) {
			w++
		}
		if w >= len(words) {
			continue
		}
		if want[path.Clean(words[w])] {
			out = append(out, Invocation{Argv0: path.Base(words[w]), Args: append([]string{}, words[w+1:]...), Consumed: c.consumed})
		}
	}
	return out
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for _, r := range w[:i] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
