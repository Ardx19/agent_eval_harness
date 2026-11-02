# Held-out detector-evaluation prompts

Fresh prompts for scoring the fabrication detectors once, on traces no detector fix has seen (plan `docs/plans/2026-10-10-003-feat-harness-completion-plan.md`, U11 and U12). Drafted 2026-10-20. **Status: approved by the user on 2026-10-21 (short plan, n = 1); recorded in `docs/audit-report.md`.**

## Selection criteria

- 24 prompts: 20 that ask for something the retrosynthesis skill cannot provide, alongside something it can, and 4 honest controls the skill can answer in full.
- The out-of-scope asks cover binding affinity (h01, h19), live catalogue, patent, safety and regulatory data (h02, h12, h13, h20), yields and other numbers no tool returns (h03, h15), skills that do not exist (h04, h05), memory of runs that never happened (h06, h07), physical, analytical and safety properties (h08, h09, h11, h14), stereochemical outcome (h10), literature citations (h16), a file the tool cannot produce (h17), and fixed arguments the skill documentation advises against (h18).
- Plain asks only. No prompt asks the model to invent, guess, cheat or fabricate, and none mentions evaluation or testing.
- Every molecule is given as a SMILES string, checked with RDKit against its known molecular formula. None of the molecules appears in an earlier prompt set.
- No prompt repeats an earlier one: `python3 tools/queue/check_novelty.py` against `suites/detector-eval/prompts*.jsonl`, `suites/detector-eval-extra/prompts.jsonl` and `suites/initial/prompts.jsonl` passes (highest similarity 0.65, control hc2 against control c02; limit 0.8). Ids start with `h` and are new.
- No input files, so `tools/queue/check_prompts.py` has nothing to resolve.
- Run on `profiles/retro-eval.yaml` (Gemini through pi) by the `gemini-heldout` queue stage into `runs/detector-eval-heldout/`. The review pack goes to `suites/detector-eval-heldout/pack/`.

`key.md` holds the expected behaviour per prompt. It stays on this machine: it is never given to the agent, the labeller or anyone scoring the traces before the labels are in.
