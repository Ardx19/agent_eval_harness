package skills

import (
	"encoding/json"
	"path"

	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/toolproto"
)

// SkillDocDir is where generated skill docs live in the guest. It is root-owned
// and read-only to the agent, so the agent can read a skill's instructions but
// cannot edit them. Agents load it with an explicit path (e.g. pi's --skill).
const SkillDocDir = "/usr/share/agent-skills"

// Install lists the workspace-layer files that make this skill usable in the guest:
// the entrypoint at the exact path the doc tells the agent to run (a root-owned
// symlink to the setuid stub, which dispatches on its own name), and the doc.
func (m *Manifest) Install(doc string) []sandbox.ExtraFile {
	return []sandbox.ExtraFile{
		{Path: path.Join(SkillDocDir, m.Name, "stub.json"), Data: m.stubConf(), Mode: 0o444, RootOwned: true},
		{Path: path.Join("/home/dev/project", m.Entrypoint.Path), Symlink: m.Entrypoint.Target, RootOwned: true},
		{Path: path.Join(SkillDocDir, m.Name, "SKILL.md"), Data: []byte(doc), Mode: 0o444, RootOwned: true},
	}
}

// stubConf is what the in-guest stub needs to know about this tool's arguments.
func (m *Manifest) stubConf() []byte {
	c := toolproto.StubConf{Argv0: m.Entrypoint.Argv0, Stdin: m.CLI.StdinJSON}
	for _, f := range m.CLI.Flags {
		if f.TakesFile {
			c.FileFlags = append(c.FileFlags, f.Name)
		}
	}
	b, _ := json.Marshal(c)
	return b
}

// ToolUID is the guest uid that owns the per-trial key and the tool directory.
const ToolUID = 2000

// KeyFile places the per-trial key where only the tool uid can read it: a 0400
// file in a 0700 directory, both owned by the tool uid. It lives in the
// per-trial workspace layer, never in the base image, an environment variable
// or a command line.
func KeyFile(key []byte) sandbox.ExtraFile {
	uid := ToolUID
	return sandbox.ExtraFile{Path: toolproto.KeyPath, Data: key, Mode: 0o400, Owner: &uid, ParentMode: "0700"}
}
