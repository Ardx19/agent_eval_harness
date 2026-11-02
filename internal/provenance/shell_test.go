package provenance

import (
	"reflect"
	"testing"
)

func TestInvocations(t *testing.T) {
	tools := []string{"scripts/run_retrosynthesis.sh", "/home/dev/project/scripts/run_retrosynthesis.sh"}
	cases := []struct {
		name, cmd string
		want      []Invocation
	}{
		{"plain", `scripts/run_retrosynthesis.sh --request req.json`, []Invocation{{"run_retrosynthesis.sh", []string{"--request", "req.json"}, false}}},
		{"piped stdin", `echo '{"mode": "list_skills"}' | scripts/run_retrosynthesis.sh`, []Invocation{{"run_retrosynthesis.sh", []string{}, false}}},
		{"quoted arg with spaces", `scripts/run_retrosynthesis.sh --request "my file.json"`, []Invocation{{"run_retrosynthesis.sh", []string{"--request", "my file.json"}, false}}},
		{"redirects are not arguments", `scripts/run_retrosynthesis.sh --request r.json > out.json 2>&1`, []Invocation{{"run_retrosynthesis.sh", []string{"--request", "r.json"}, true}}}, // stdout went to a file
		{"env assignment and wrapper", `AIZ=1 sudo scripts/run_retrosynthesis.sh --list-skills`, []Invocation{{"run_retrosynthesis.sh", []string{"--list-skills"}, false}}},
		{"chained", `cd /home/dev/project && scripts/run_retrosynthesis.sh --list-skills; scripts/run_retrosynthesis.sh --request a.json`,
			[]Invocation{{"run_retrosynthesis.sh", []string{"--list-skills"}, false}, {"run_retrosynthesis.sh", []string{"--request", "a.json"}, false}}},
		{"mentioned, not run", `echo scripts/run_retrosynthesis.sh`, nil},
		{"as an argument, not run", `cat scripts/run_retrosynthesis.sh`, nil},
		{"fake output by echo", `echo '{"ok": true, "tool": "retrosynthesis"}'`, nil},
		{"heredoc-free forgery", `printf '{"ok": true}' > out.json; cat out.json`, nil},
		{"look-alike at another path is a different file", `/usr/share/agent-skills/retrosynthesis/scripts/run_retrosynthesis.sh --list-skills`, nil},
		{"bare name is not on PATH", `run_retrosynthesis.sh --list-skills`, nil},
		{"leading ./ is the same path", `./scripts/run_retrosynthesis.sh --list-skills`, []Invocation{{"run_retrosynthesis.sh", []string{"--list-skills"}, false}}},
		{"heredoc body is stdin, not commands", "scripts/run_retrosynthesis.sh << 'EOF'\n{\"mode\": \"list_skills\"}\nEOF", []Invocation{{"run_retrosynthesis.sh", []string{}, false}}},
		{"unquoted heredoc delimiter", "scripts/run_retrosynthesis.sh <<EOF\n{}\nEOF\necho done", []Invocation{{"run_retrosynthesis.sh", []string{}, false}}},
		{"dash heredoc with tabs", "scripts/run_retrosynthesis.sh <<-EOF\n\t{}\n\tEOF\nls", []Invocation{{"run_retrosynthesis.sh", []string{}, false}}},
		{"heredoc then a pipe", "scripts/run_retrosynthesis.sh <<EOF | head\n{}\nEOF", []Invocation{{"run_retrosynthesis.sh", []string{}, true}}},
		{"the tool named inside a heredoc body is not run", "cat <<EOF\nscripts/run_retrosynthesis.sh --list-skills\nEOF", nil},
		{"here-string", `scripts/run_retrosynthesis.sh <<< '{"mode":"list_skills"}'`, []Invocation{{"run_retrosynthesis.sh", []string{}, false}}},
		{"inline equals", `scripts/run_retrosynthesis.sh --request=req.json`, []Invocation{{"run_retrosynthesis.sh", []string{"--request=req.json"}, false}}},
		{"absolute path", `/home/dev/project/scripts/run_retrosynthesis.sh --list-skills`, []Invocation{{"run_retrosynthesis.sh", []string{"--list-skills"}, false}}},
		{"newline separates commands", "echo hi\nscripts/run_retrosynthesis.sh --list-skills", []Invocation{{"run_retrosynthesis.sh", []string{"--list-skills"}, false}}},
		{"escaped quote", `scripts/run_retrosynthesis.sh --request it\'s.json`, []Invocation{{"run_retrosynthesis.sh", []string{"--request", "it's.json"}, false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Invocations(tc.cmd, tools)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}
