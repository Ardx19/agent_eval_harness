// Command layertool packs a directory tree into a read-only guest layer image.
//
//	layertool --src tree --out env.ext4 --label env
//
// It also writes <out>.entries.jsonl, which the diff uses as the layer's contents.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"agentevalharness/internal/sandbox"
)

func main() {
	src := flag.String("src", "", "directory whose contents become the layer root")
	out := flag.String("out", "", "output ext4 image")
	label := flag.String("label", "env", "ext4 label the guest finds the drive by")
	flag.Parse()
	if *src == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: layertool --src dir --out image.ext4 [--label env]")
		os.Exit(2)
	}
	entries, err := sandbox.BuildLayer(context.Background(), sandbox.NewSystem(), *src, *out, *label)
	if err == nil {
		err = sandbox.WriteEntries(*out+".entries.jsonl", entries)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "layertool:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d entries\n", *out, len(entries))
}
