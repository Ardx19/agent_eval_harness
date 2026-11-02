// Command skilldoc renders the agent-facing SKILL.md from a host-only manifest
// and refuses to write it if it leaks host paths or implementation details.
package main

import (
	"flag"
	"fmt"
	"os"

	"agentevalharness/internal/skills"
)

func main() {
	manifest := flag.String("manifest", "skills/retrosynthesis/manifest.json", "host-only manifest")
	tmpl := flag.String("template", "skills/retrosynthesis/SKILL.template.md", "prose template")
	out := flag.String("out", "", "output path (default: print to stdout)")
	flag.Parse()

	m, err := skills.LoadManifest(*manifest)
	if err != nil {
		fail("manifest: %v", err)
	}
	tb, err := os.ReadFile(*tmpl)
	if err != nil {
		fail("template: %v", err)
	}
	doc, err := m.RenderDoc(string(tb))
	if err != nil {
		fail("render: %v", err)
	}
	if hits := m.LeakCheck(doc); len(hits) > 0 {
		for _, h := range hits {
			fmt.Fprintln(os.Stderr, "LEAK:", h)
		}
		fail("refusing to write a doc that leaks %d thing(s)", len(hits))
	}
	if *out == "" {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fail("write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, no leaks)\n", *out, len(doc))
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
