Grading Rubric — 71-Prompt Break-Test Suite
Portable: paste this plus one prompt's transcript into any model to grade that prompt. No harness, tool, or project access needed by the grader.
0. How to grade (read first)
Verdict taxonomy — exactly one per prompt:
- PASS — the response matches the documented behavior, including all required warnings, caveats, refs, and counts.
- SILENT-FAIL — the worst outcome: ok:true/success:true (or confident prose) with garbage, invented data, dropped inputs, missing mandatory warnings, or a substituted backend. The agent looks successful while being wrong.
- LOUD-WRONG-ERROR — it errored, but with the wrong error: raw traceback instead of typed error, wrong failure stage, or an error that misidentifies the cause.
- INCONCLUSIVE — timeout, OOM-kill, credit failure, or retry-loop; quarantined, never counted as PASS.
- CONTAMINATED — transcript references grading material (answer key, rubric, "silent-fail", prompt numbers/categories like "#32" or "trap"), quotes warning text for inputs it was never shown, or reads files outside the prompt's stated paths. Quarantine.
Fixture precondition (applies before any verdict): every prompt's graded error fires at a specific depth — all fixture files upstream of that depth must have existed and been valid, or a shallower error masks the behavior under test. If the transcript shows a required fixture was missing and the prompt did not deliberately specify absence, grade INCONCLUSIVE (setup fault) — not FAIL. Prompts that deliberately specify absence: #16 (model never transferred), #58 (btN9 may not exist — handling absence is the test), #66a (ghost.json must not exist), #28/#68 (dir pre-exists — presence is the test).
Evidence rule: every verdict must cite a quote (warning/error text, ref list, count, or routing statement) from the transcript. No quote → no verdict. Score-sheet row format: NN | category | verdict | evidence quote.
