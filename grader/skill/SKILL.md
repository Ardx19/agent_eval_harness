---
name: retrosynthesis
description: Use AiZynthFinder-based retrosynthesis skills to generate routes, analyze steps, check building block availability, compare routes, assess synthesis feasibility, configure search parameters, manage policies and stocks, cluster routes, score reactions, and perform single-step molecular expansion for drug molecules. Trigger when the user provides a SMILES string and asks about synthesis, retrosynthesis, routes, precursors, synthesizability, pathway, synthetic route, building blocks, starting materials, feasibility, feasible, retrosynthetic, disconnection, in stock, available, compare routes, best route, configure search, expand molecule, cluster routes, score reactions, policy management, stock management.
---

# Retrosynthesis Skill

## Overview

This skill gives you 11 AiZynthFinder-based retrosynthesis skills through a single command, `scripts/run_retrosynthesis.sh`. You describe what you want as a JSON request; the command runs it and prints a JSON result. Routes are model-predicted disconnection trees scored by a template-prediction policy.

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
scripts/run_retrosynthesis.sh --request req.json
scripts/run_retrosynthesis.sh --list-skills
echo '<json>' | scripts/run_retrosynthesis.sh
```

Run it from the project directory. The request is a JSON object:

| Mode | Required keys | What it does |
|---|---|---|
| `list_skills` | none | List the skills. |
| `help_skill` | skill | Describe one skill. |
| `route_query` | query | Natural-language request, routed to a skill by keyword matching. |
| `run_skill` | skill, kwargs | Run one skill with structured arguments (preferred when the intent is clear). |
| `batch_generate_routes` | smiles_list | Generate routes for many molecules in one call. |
| `batch_assess_feasibility` | smiles_list | Assess feasibility of many molecules in one call. |

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

All interactions with these skills go through `scripts/run_retrosynthesis.sh`. Do not try to reimplement its logic, thresholds or scoring yourself. If a call fails, report the error you received instead of working around it.

## Natural-language routing

`{"mode": "route_query", "query": "is aspirin synthesizable in under 3 steps?"}` detects the intent by keyword matching, extracts a SMILES string (or resolves a common drug name such as aspirin, ibuprofen, paracetamol, caffeine or morphine) and parses threshold constraints.

Supported threshold phrases: `"under N steps"` and `"max N steps"` set `max_steps`; `"at least N% available"` sets `min_availability`; `"confidence above N"` sets `min_confidence`; `"shortest"` and `"most available"` set `best_route_metric`.

A molecule is resolved from a valid SMILES string in the query, then from the common-name list. If neither resolves you get `{"success": false, "error": "molecule_not_found", "message": "..."}`. If the intent is not recognized you get `{"success": false, "error": "intent_not_recognized", "message": "...", "available_skills": [...]}`. Prefer `run_skill` with an explicit skill name whenever the intent is clear; it is more predictable than keyword matching.

---

## Available skills (reference)

Use each as `{"mode": "run_skill", "skill": "<name>", "kwargs": {...}}`.

### 1. `route_generation`

Generates retrosynthesis routes for a target molecule.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `smiles` | string | yes | - | - |
| `max_routes` | integer | no | `10` | >= 1; first N unique routes |
| `config` | object | no | - | search settings, see FinderConfig |
| `max_depth` | integer | no | - | >= 1 |
| `deduplicate` | boolean | no | `true` | - |

Response: `success`, `routes` (list of route trees), `search_time`, `message`, `metadata` (`routes_requested`, `routes_found`, `routes_unique`, `config_hash`, `stock_used`, `policies_used`, `cache_hit`).

**Speed:** Slow (about 10-30 s of search, on top of the per-call model load).

### 2. `route_analysis`

Analyzes a single route tree for steps, intermediates and depth.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `route` | object | yes | - | a route tree |

Response: `num_steps`, `num_intermediates`, `depth`.

**Speed:** Fast tree walk (the per-call model load still applies).

### 3. `building_block_availability`

Checks precursor availability from the `in_stock` fields of a route tree.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `route` | object | yes | - | a route tree |

Response: `available_precursors` (SMILES list), `unavailable_precursors`, `availability_ratio`, `message`.

### 4. `route_comparison`

Compares routes and identifies the best by steps, availability and confidence.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `routes` | array | yes | - | list of route trees |

Response: `shortest_route_index`, `most_available_index`, `highest_confidence_index`, `summary` (per-route stats), `message`.

### 5. `synthesis_feasibility`

End-to-end feasibility assessment. Composes route generation, analysis, availability and comparison.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `smiles` | string | yes | - | - |
| `best_route_metric` | string | no | - | one of most_available / shortest / highest_confidence |
| `max_steps` | integer | no | `5` | >= 1 |
| `min_availability` | number | no | `0.5` | >= 0; <= 1 |
| `min_confidence` | number | no | `0.3` | >= 0; <= 1; 0.6 is unrealistically high and rejects nearly all routes |
| `max_intermediates` | integer | no | `6` | >= 0 |
| `max_depth` | integer | no | - | >= 1 |
| `stock` | string | no | `zinc` | - |
| `config` | object | no | - | wins over `stock` if both are given |

Response: `is_feasible`, `reason`, `num_routes`, `best_route_index`, `best_route_stats`, `best_route`, `routes`, `threshold_results`, `message`.

**Speed:** Slow (about 10-30 s, on top of the per-call model load).

`config` vs `stock`: if `config` is supplied it wins (it already carries `stock` plus time and iteration limits and policies); a non-default `stock` alongside `config` produces a warning and is ignored. `max_depth` limits **route length** (a `max_transforms` clamp), which is related to but not the same as the tree depth that `route_analysis` reports; omitting it means unconstrained.

### 6. `expander`

Single-step molecular expansion without a full tree search.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `smiles` | string | yes | - | - |
| `return_n` | integer | no | `5` | >= 1 |
| `config_path` | string | no | - | - |

Response: `reactions` (each with `reactants_smiles`, `reaction_smiles` and metadata), `success`, `message`.

**Speed:** Slow.

### 7. `search_config`

Reads MCTS search configuration. **Read-only:** overrides are validated, never applied, and the response carries `"applied": false`.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `config_path` | string | no | - | - |
| `iteration_limit` | integer | no | - | >= 1 |
| `time_limit` | number | no | - | >= 0 |
| `max_transforms` | integer | no | - | >= 1 |
| `C` | number | no | - | - |
| `return_first` | boolean | no | - | - |
| `exclude_target_from_stock` | boolean | no | - | - |
| `break_bonds` | array | no | - | - |
| `freeze_bonds` | array | no | - | - |
| `break_bonds_operator` | string | no | - | one of and / or |

Response: `config` (the active search configuration), `success`, `message`, `applied` (always `false`).

**Speed:** Slow.

### 8. `policy_management`

Lists the available expansion and filter policies (fast), or selects or deselects one (slow).

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `action` | string | no | - | one of list / select / deselect |
| `policy_type` | string | no | - | one of expansion / filter; for select/deselect |
| `key` | string | no | - | - |
| `config_path` | string | no | - | - |

Response: `available` (dict), `selected` (dict of live selections), `success`, `message`.

**Speed:** Hybrid: fast for `list`, slow for `select`/`deselect`.

### 9. `stock_management`

Lists the available stocks (`zinc`, `mcule`), or selects or deselects one.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `action` | string | no | - | one of list / select / deselect |
| `key` | string | no | - | - |
| `config_path` | string | no | - | - |

Response: `available` (dict), `selected` (the live selection **list**, not a string), `success`, `message`.

**Speed:** Hybrid: fast for `list`, slow for `select`/`deselect`.

### 10. `route_clustering`

Clusters routes by structural similarity and computes a pairwise distance matrix.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `routes` | array | yes | - | - |
| `n_clusters` | integer | no | `5` | >= 1 |
| `max_clusters` | integer | no | `5` | >= 1 |
| `config_path` | string | no | - | - |

Response: `labels` (cluster per route), `distance_matrix`, `success`, `message`.

**Speed:** Slow.

### 11. `reaction_score`

Scores routes with built-in scorers and can extract per-reaction metadata.

| Parameter | Type | Required | Default | Notes |
|---|---|---|---|---|
| `routes` | array | yes | - | - |
| `scorer` | string | no | `state score` | - |
| `include_per_reaction` | boolean | no | `false` | - |
| `config_path` | string | no | - | - |

Response: `scored_routes`, `per_reaction_scores`, `success`, `message`.

**Speed:** Slow.

Built-in scorers: `state score`, `number of reactions`, `number of pre-cursors`, `number of pre-cursors in stock`, `route cost`. An unknown scorer name returns an error that lists the available ones.

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
