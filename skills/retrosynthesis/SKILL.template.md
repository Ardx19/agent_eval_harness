# Retrosynthesis Skill

## Overview

This skill gives you 11 AiZynthFinder-based retrosynthesis skills through a single command, `{{.Command}}`. You describe what you want as a JSON request; the command runs it and prints a JSON result. Routes are model-predicted disconnection trees scored by a template-prediction policy.

---

## Routing

`route_query` matches keywords to find the intent: strong patterns first, weak single-token patterns (`stock`, `zinc`, `shortest`, `most available`, `policy`) only if nothing strong matched. Skills marked "yes" run directly from natural language. Skills marked "no" return `{"success": false, "error": "needs_structured_input", "required_fields": [...], "hint": "..."}`; call `run_skill` with structured inputs for those. The router does not chain route generation into analysis or comparison for you.

| User intent | Skill | Natural-language routable? |
|-------------|-------|--------------|
| "generate routes" / "find synthesis path" / "retrosynthesis for X" / "how to make X" | `route_generation` | yes |
| "analyze this route" / "how many steps" / "route depth" / "intermediates" | `route_analysis` | no, `run_skill` with `route` |
| "are precursors available" / "building blocks in stock" / "starting materials" | `building_block_availability` | no, `run_skill` with `route` |
| "compare routes" / "which route is best" | `route_comparison` | no, `run_skill` with `routes` |
| "is it synthesizable" / "feasibility" / "full assessment" / ambiguous intent | `synthesis_feasibility` | yes |
| "expand this molecule" / "one-step precursors" / "single-step retrosynthesis" | `expander` | yes |
| "configure search" / "set MCTS params" / "change iteration limit" | `search_config` | yes (read-only) |
| "list policies" / "select policy" / "change expansion policy" | `policy_management` | yes |
| "list stocks" / "select stock" / "change stock" | `stock_management` | yes |
| "cluster routes" / "group similar routes" / "route similarity" | `route_clustering` | no, `run_skill` with `routes` |
| "score reactions" / "reaction scores" / "per-step scores" | `reaction_score` | no, `run_skill` with `routes` |

When intent is ambiguous or the user wants a complete answer, default to `synthesis_feasibility`. It composes route generation, analysis, availability and comparison internally.

---

## Running

```
{{.Command}} --request req.json
{{.Command}} --list-skills
echo '<json>' | {{.Command}}
```

Run it from the project directory. The request is a JSON object:

{{.ModeReference}}
Other keys: `skill` (a skill name), `kwargs` (an object, forwarded to that skill exactly as documented below; a `config` object inside `kwargs` is converted automatically), `query` (text), `smiles_list` (a list of SMILES for the batch modes), and `pool_size` (optional, default 1).

```json
{
  "mode": "run_skill",
  "skill": "route_generation",
  "kwargs": {"smiles": "CCO", "max_routes": 5}
}
```

The command prints a result object and exits 0 on success and 1 on failure:

```json
{"ok": true, "tool": "retrosynthesis", "mode": "run_skill", "result": {}, "error": null}
```

`result` is the skill's response, serialized as plain JSON. On failure `ok` is `false` and `error` holds a short description.

### Cost: read this before running anything

Every invocation is a fresh process that loads `pool_size` AiZynthFinder instances (about 40 s and 1-2 GB of RAM **each**) before it runs any skill, even fast ones like `route_analysis`. This cost is paid on **every call**, not just the first. `pool_size` only helps with concurrency *within* a call (batch modes with `n_workers`); the default is 1. Only raise it for a batch call with `n_workers > 1`, and expect proportionally more load time and RAM.

Tell the user before you run a request that this is not a fast lookup.

---

## Critical

Never write custom threshold logic. Always pass thresholds directly in `kwargs` to `synthesis_feasibility`:

```json
{
  "mode": "run_skill",
  "skill": "synthesis_feasibility",
  "kwargs": {
    "smiles": "CCO",
    "min_confidence": 0.3,
    "min_availability": 0.5,
    "max_steps": 5,
    "max_intermediates": 6,
    "max_depth": 10
  }
}
```

Do not hardcode threshold checks outside the skill. Do not skip `min_confidence`.

## Critical defaults

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| `min_confidence` | `0.3` | Policy probability scores for drug-like molecules typically range 0.15-0.50. A threshold of 0.6 is unrealistically high and rejects nearly all routes. |
| `min_availability` | `0.5` | At least half of the building blocks should be in stock for a route to be practically feasible. |
| `max_steps` | `5` | Most synthetic routes beyond 5 steps are impractical for medicinal chemistry. |
| `max_intermediates` | `6` | Limits route complexity to keep synthesis tractable. |
| `max_routes` | `10` | Balance between exploring the search space and computation time. |

## Threshold results

`synthesis_feasibility` returns per-threshold pass/fail results in `result.threshold_results.results`, for example:

```json
{
  "is_feasible": true,
  "threshold_results": {
    "results": [
      {"threshold_name": "num_steps", "threshold_value": 5, "actual_value": 3, "passed": true, "message": "..."},
      {"threshold_name": "availability_ratio", "threshold_value": 0.5, "actual_value": 0.4, "passed": false, "message": "..."}
    ]
  }
}
```

Report per-threshold pass/fail, not just the overall `is_feasible` flag.

## Route deduplication

`route_generation` deduplicates routes by default (same building-block SMILES set, same reaction templates and same tree structure count as duplicates) **before** applying `max_routes`, so `max_routes` means "the first N unique routes". Disable it with `"deduplicate": false` in `kwargs`.

## Always use the provided command

All interactions with these skills go through `{{.Command}}`. Do not try to reimplement its logic, thresholds or scoring yourself. If a call fails, report the error you received instead of working around it.

## Natural-language routing

`{"mode": "route_query", "query": "is aspirin synthesizable in under 3 steps?"}` detects the intent by keyword matching, extracts a SMILES string (or resolves a common drug name such as aspirin, ibuprofen, paracetamol, caffeine or morphine) and parses threshold constraints.

Supported threshold phrases: `"under N steps"` and `"max N steps"` set `max_steps`; `"at least N% available"` sets `min_availability`; `"confidence above N"` sets `min_confidence`; `"shortest"` and `"most available"` set `best_route_metric`.

A molecule is resolved from a valid SMILES string in the query, then from the common-name list. If neither resolves you get `{"success": false, "error": "molecule_not_found", "message": "..."}`. If the intent is not recognized you get `{"success": false, "error": "intent_not_recognized", "message": "...", "available_skills": [...]}`. Prefer `run_skill` with an explicit skill name whenever the intent is clear; it is more predictable than keyword matching.

---

## Available skills (reference)

Use each as `{"mode": "run_skill", "skill": "<name>", "kwargs": {...}}`.

{{.SkillReference}}
---

## Search configuration (`config`)

`route_generation`, `synthesis_feasibility` and the batch modes accept a `config` object:

```json
{"stock": "zinc", "expansion_policy": ["uspto", "ringbreaker"], "filter_policy": "uspto",
 "time_limit": 120, "iteration_limit": 100, "max_transforms": 6, "return_first": false,
 "exclude_target_from_stock": true, "C": 1.4, "break_bonds": [], "freeze_bonds": [],
 "break_bonds_operator": "and", "stock_timeouts": {"mcule": 60, "zinc": 120}}
```

All fields are optional; only override what you need. The other skills take a `config_path` string instead, and when it is omitted the default configuration is used. `route_analysis`, `building_block_availability` and `route_comparison` take neither.

---

## Batch processing

For high-throughput screening, both batch modes take `smiles_list` at the top level of the request (not inside `kwargs`), plus an optional `config` object.

```json
{
  "mode": "batch_generate_routes",
  "smiles_list": ["CC(=O)Oc1ccccc1C(=O)O", "CN1C=NC2=C1C(=O)N(C)C(=O)N2C"],
  "max_routes": 10,
  "config": {"stock": "zinc"},
  "n_workers": 4,
  "pool_size": 4
}
```

`batch_generate_routes` returns `results` (a list of `[smiles, response]` pairs), `n_success`, `n_failed`, `total_time_s`, `throughput_mol_per_min` and `n_cache_hits` (hits only occur within one call). Set `pool_size >= n_workers`. Unknown top-level keys are rejected with an error that lists the accepted ones.

`batch_assess_feasibility` takes the same shape plus the `synthesis_feasibility` thresholds at the top level (`min_confidence` and so on). It also returns `feasible_smiles`, `infeasible_smiles` and `failure_breakdown` (threshold name to the SMILES that failed it). Programming and configuration errors fail the whole batch; they are never reported as "all infeasible".

---

## Route tree structure

Each route is a nested tree:

```json
{
  "type": "mol",
  "smiles": "...",
  "in_stock": false,
  "children": [
    {
      "type": "reaction",
      "metadata": {"policy_probability": 0.72},
      "children": [
        {"type": "mol", "smiles": "...", "in_stock": true},
        {"type": "mol", "smiles": "...", "in_stock": true}
      ]
    }
  ]
}
```

---

## Notes

- Slow skills (about 10-30 s beyond the model load): `route_generation`, `synthesis_feasibility`, `expander`, `search_config`, `route_clustering`, `reaction_score`.
- Fast skills (pure tree walk beyond the model load): `route_analysis`, `building_block_availability`, `route_comparison`.
- Hybrid: `policy_management` and `stock_management` are fast for `action: "list"` and slow for `select`/`deselect`.
- All skills are stateless request/response calls and are safe to call repeatedly.
- There is no skill for saving or loading a full search tree.
- Never present a generated route as a validated, lab-proven synthesis. It is a model-predicted disconnection tree scored by a template-prediction policy, and `policy_probability` is a model confidence, not a yield or a success guarantee. Building-block "in stock" status reflects a specific stock database snapshot (`zinc` or `mcule`), not real-time purchasability.
