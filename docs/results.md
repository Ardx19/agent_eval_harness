# Results

Short plan agreed with the user on 2026-10-21: one held-out Gemini run (n = 1), labels on the held-out pack only, no detector fixes, one scoring. This page is filled in as the steps finish.

## Freeze (2026-10-21, before the held-out run)

The detectors are frozen as they are. None of these files changes until the held-out scoring is saved; the scoring step checks the hashes again.

| What | sha256 |
|---|---|
| `internal/provenance/*.go`, combined: `(cd internal/provenance && LC_ALL=C sha256sum *.go | sha256sum)` | `82d9bab7f4ea7e9be4424c580806c5b335f96d31adca34070ffc96d7309b18b7` |
| `internal/evaldet/evaldet.go` | `03bd4e4e8f95149fe0346cef3e88754e4d1b7911c7c934ec46ba82efb18a3a37` |
| `bin/harness` (a fresh build from source gives the same hash) | `956d80db8e3c4e0293299777f25ca6f00cb30c6e234efea0ac703ecd91d1dd4b` |
| `profiles/retro-eval.yaml` | `deb54ce027a99607e556f34ed022f00693bd7b7804f38d1f574b41448dae6262` |
| `suites/detector-eval-heldout/prompts.jsonl` | `ce8c2aa4e338f76fcc34d782e8364d495822f4fd05a67762311fabf6c0537f3e` |
| `tools/labelpack/LABELLING_PROMPT.md` | `38c1d301526decac7e63141a1decba2e951b424a1f2286025b6cf60b125ee50b` |

Per file in `internal/provenance`:

```
54beecb6b6724b0da2a3cb314b31b8ae1124f659f8cd7dc00bbc49608678f88b  claims.go
a612a74b4c14527afa1d60d0e177e3418594cc0f8339433c06a96cfca1b15328  join.go
97f9714a66de2bd8fd2c1ff0c5af5b0eba00302937bd2db7a73e9d1fb56d1920  mutation_test.go
45e46a02228044eb85e3be56ac5dca020404c6eda1241f4b0928fb5a52f4d9d1  numbers.go
dbab4116b8b9eee567cf429d7ca8bcc169343872cef64e4e889188bd09bc346b  opencode_test.go
406265635af43ce0c4da8949710bfdcf0fb8e788557d80a98420ce3bc41a2995  scenarios_test.go
41b45255ce0812a9770f7af41c28dd4ba3f5683b4fd3f0669057e0b820a3b801  shell.go
4f9631c9d1f6d7a741a1436d2b7fe1a9f334df1def7a57eb2d06576c9aa902fc  shell_test.go
```
