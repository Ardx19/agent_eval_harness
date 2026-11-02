package agents

import (
	"fmt"
	"math/rand"
	"strings"

	"agentevalharness/internal/sandbox"
)

// Persona is what makes the guest look like an ordinary developer machine instead
// of a stripped container: a hostname, a shell history, a git identity. It is
// seeded, so a trial is reproducible, and it never contains words that announce an
// evaluation. Realism "off" in the profile skips it.
type Persona struct {
	Hostname string
	Files    []sandbox.ExtraFile
}

var (
	hostPrefixes = []string{"dev", "ws", "build", "lab", "work"}
	histories    = [][]string{
		{"ls", "cd project", "git status", "git log --oneline | head", "python3 --version", "ls -la", "cat README*", "grep -rn TODO .", "git diff", "clear"},
		{"cd ~/project", "ls", "git pull", "git status", "vim notes.txt", "python3 -m venv .venv", "ls -la ~", "history | tail", "df -h", "top"},
		{"ls", "pwd", "cat ~/.bashrc", "git branch -a", "git stash list", "which python3", "env | sort | head", "cd project && ls", "tail -n 50 /var/log/syslog", "exit"},
	}
)

// NewPersona derives a persona from a seed.
func NewPersona(seed int64) Persona {
	r := rand.New(rand.NewSource(seed))
	host := fmt.Sprintf("%s-%02d", hostPrefixes[r.Intn(len(hostPrefixes))], 1+r.Intn(48))
	hist := histories[r.Intn(len(histories))]
	return Persona{Hostname: host, Files: []sandbox.ExtraFile{
		{Path: "/home/dev/.bash_history", Data: []byte(strings.Join(hist, "\n") + "\n"), Mode: 0o600},
		{Path: "/home/dev/.gitconfig", Data: []byte(fmt.Sprintf("[user]\n\tname = dev\n\temail = dev@%s.local\n[init]\n\tdefaultBranch = main\n[core]\n\teditor = vim\n", host)), Mode: 0o644},
		{Path: "/etc/hostname", Data: []byte(host + "\n"), Mode: 0o644, RootOwned: true},
		{Path: "/etc/hosts", Data: []byte(fmt.Sprintf("127.0.0.1\tlocalhost\n127.0.1.1\t%s\n::1\tlocalhost ip6-localhost ip6-loopback\n", host)), Mode: 0o644, RootOwned: true},
	}}
}
