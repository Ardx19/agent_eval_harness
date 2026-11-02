# Grader handoff

Build a deterministic, offline grader for recorded AI-agent runs on a retrosynthesis tool. Read `SPEC.md` first.

## Set up
- Python 3.10+, standard library only. Optional: `pip install rdkit` (structure comparison), `pytest` (tests).
- No GPU, VM, network, API keys or chemistry software needed: the tool's real outputs are inside the recorded runs.

## What is here
| File | Purpose |
|---|---|
| `SPEC.md` | the full specification: data format, seven outcomes, decision procedure, rules, per-prompt specs, outputs, acceptance criteria |
| `skill/SKILL.md` | the tool's reference (response shapes the rules read) |
| `prompts/prompts.jsonl` | the 21 retrosynthesis prompts |
| `prompts/rubric.jsonl` | original rubric text for them |
| `prompts/checks_draft.jsonl` | starting set of deterministic checks |
| `prompts/original_rubric_header.md` | older grading instructions for a model grader (reference only) |
| `prompts/out_of_scope_prompts.jsonl`, `prompts/scopes.jsonl` | prompts the tool cannot satisfy, with scope tags and the facts that cannot exist |
| `prompts/inputs/` | small input files some prompts refer to |

The recorded runs (traces with their `blobs/` folders) are delivered separately.

## First steps
1. Read `SPEC.md` sections B (data) and C-D (outcomes and procedure).
2. Open two or three recorded trials and follow one tool call from `trace.json` through `host.call_records` to its stdout blob.
3. Write `prompts/spec.jsonl` for the 21 prompts (section G), then the grader, then golden fixtures and tests (section I).
4. Send back what section A lists under "What you deliver back".


## Grader (added)
- Run: `python grade_retro.py --data data/ --spec prompts/spec.jsonl --out out/`
- Tests: `python tests/golden_gen.py && python -m pytest tests`
- Decisions, open questions and data gaps: `NOTES.md`
- Rebuild the spec after editing it: `python prompts/build_spec.py`
