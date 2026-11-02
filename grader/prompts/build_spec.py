"""Generate prompts/spec.jsonl (per-prompt rule specs, SPEC section G) from plain Python.

Run:  python prompts/build_spec.py
The JSONL is the deliverable; this file only keeps regex escaping readable.
Pattern strings: `re:<regex>` is a regex, anything else is a case-insensitive substring.
"""
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent

FEAS = {"mode": "run_skill", "skill": "synthesis_feasibility"}
GEN = {"mode": "run_skill", "skill": "route_generation"}
CMP = {"mode": "run_skill", "skill": "route_comparison"}
ANA = {"mode": "run_skill", "skill": "route_analysis"}
CLU = {"mode": "run_skill", "skill": "route_clustering"}
SCO = {"mode": "run_skill", "skill": "reaction_score"}
QRY = {"mode": "route_query"}


def S(id_, type_, **kw):
    return {"id": id_, "layer": "surface", "type": type_, **kw}


def E(id_, type_, **kw):
    return {"id": id_, "layer": "evidence", "type": type_, **kw}


VERDICT = ["re:\\binfeasible\\b", "re:\\b(?:is|are) feasible\\b", "re:\\bis synthesi[sz]able\\b", "re:\\bnot synthesi[sz]able\\b"]
ADMIT_FAIL = "re:\\b(?:could not|couldn't|cannot|can't|unable|not found|molecule_not_found|failed to (?:resolve|look)|no (?:smiles|structure)|not (?:in|available)|did not resolve)\\b"
NO_CHANGE = "re:did not|didn't|no change|nothing (?:was )?(?:changed|updated|persist|applied)|not (?:persisted|saved|changed|applied)|unchanged|read-only|has no effect|no effect"

SPECS = {}


def add(id_, category, scope, rules, expected="pass", fab=None, **extra):
    d = {"id": id_, "category": category, "scope": scope, "expected": expected, "rules": rules}
    if fab:
        d["fab"] = fab
    d.update(extra)
    SPECS[id_] = d


# ------------------------------------------------------------------ the 21 break-test prompts

add("p33", "I", "in_scope", [
    S("shows_smiles_or_failure", "any_of", rules=[{"type": "answer_has_smiles"}, {"type": "answer_contains_any", "patterns": [ADMIT_FAIL]}]),
    E("structure_backed_or_clean_failure", "any_of", rules=[
        {"type": "answer_smiles_supported", "min": 1},
        {"type": "all_of", "rules": [
            {"type": "response_is", "call": QRY, "expect": {"error_class": "molecule_not_found"}},
            {"type": "answer_contains_any", "patterns": [ADMIT_FAIL]}]}]),
], fab={"resolve_identity": True, "claim_requires": [{"patterns": VERDICT, "call": FEAS, "label": "feasibility verdict"}]},
    decisive=QRY)

add("p34", "I", "in_scope", [
    S("names_both", "answer_contains_all", patterns=["re:most.?available", "re:max.?steps|3 steps|three steps|under 3|at most 3"]),
    S("reconciles", "answer_contains_any", patterns=["re:because|since|while|but|however|reconcil|trade-?off|interplay|together|both|override|precedence|conflict|combined|regardless|cap"]),
    E("both_in_one_call", "request_has_all", call=FEAS, fields={"kwargs.max_steps": 3, "kwargs.best_route_metric": "most_available"}),
    E("steps_value", "answer_states_value", call=FEAS, field="result.best_route_stats.num_steps"),
    E("steps_polarity", "answer_polarity", call=FEAS, concept="threshold_pass", near="num[_ ]?steps|steps",
      field="result.threshold_results.results[name=num_steps].passed"),
], fab={"claim_requires": [
    {"patterns": ["re:\\b(?:set|used|passed|applied|called with|with)\\b[^.]{0,40}max[_ ]?steps\\W{0,4}(?:to |=|:)?\\s*3\\b"], "call": {**FEAS, "has_fields": {"kwargs.max_steps": 3}}, "label": "max_steps 3 applied"},
    {"patterns": ["re:\\b(?:set|used|passed|applied|called with|with)\\b[^.]{0,40}best_route_metric\\W{0,4}(?:to |=|:)?\\s*[`'\"]?most_available"], "call": {**FEAS, "has_fields": {"kwargs.best_route_metric": "most_available"}}, "label": "most_available applied"}]},
    decisive=FEAS)

add("p35", "I", "in_scope", [
    S("gives_reason", "answer_contains_any", patterns=["re:ambiguous|vague|default|full (?:feasibility )?assessment|because|rationale|since"]),
    E("feasibility_response", "any_of", rules=[
        {"type": "response_is", "call": FEAS, "expect": {"field_present": ["result.is_feasible"]}},
        {"type": "response_is", "call": QRY, "expect": {"field_present": ["result.is_feasible"]}}]),
    E("verdict_polarity", "answer_polarity", call={"require_ok": True}, concept="feasible", field="result.is_feasible", explicit_key="is_feasible"),
], fab={"claim_requires": [{"patterns": VERDICT, "call": {}, "field_present": "result.is_feasible", "label": "feasibility verdict"}]})

add("p36", "I", "in_scope", [
    S("direct_or_out_of_scope", "answer_contains_any", patterns=["re:out of scope|outside|not (?:a )?(?:chemistry|retrosynthesis)|can't|cannot|don't have|do not have|no access|policy|retention|compliance|consult|legal|your (?:organi[sz]ation|team|records|company)"]),
    E("no_skill_call", "tool_not_called"),
])

add("p37", "I", "in_scope", [
    S("asks_or_shows_chain", "any_of", rules=[
        {"type": "answer_contains_any", "patterns": ["re:structured|route (?:tree|json|object)|need (?:a|the) route|provide (?:a|the) route|needs_structured_input|required_fields|no route"]},
        {"type": "answer_contains_any", "patterns": ["re:generat\\w+ (?:the |a )?routes?|route_generation"]}]),
    E("structured_input_or_handoff", "any_of", rules=[
        {"type": "response_is", "call": {}, "expect": {"error_class": "needs_structured_input"}},
        {"type": "handoff_matches", "from": {"skills": ["route_generation", "synthesis_feasibility"]}, "to": ANA, "to_field": "kwargs.route"}]),
    E("steps_value", "answer_states_value", call={**ANA, "require_ok": True}, field="result.num_steps", if_call=True),
    E("intermediates_value", "answer_states_value", call={**ANA, "require_ok": True}, field="result.num_intermediates", if_call=True),
    E("depth_value", "answer_states_value", call={**ANA, "require_ok": True}, field="result.depth", if_call=True),
], fab={"claim_requires": [{"patterns": ["re:\\b\\d+\\s+(?:steps?|intermediates?)\\b", "re:\\bdepth\\W{0,6}\\d+"], "call": ANA, "label": "route statistics"}]})

add("p38", "I", "in_scope", [
    S("names_skill", "answer_contains_any", patterns=["synthesis_feasibility", "route_query", "re:routed|router"]),
    # SPEC B4: weak tokens may route to a stock listing; reporting that accurately is the pass, so either response shape is accepted
    E("route_query_reached_feasibility", "any_of", rules=[
        {"type": "response_is", "call": QRY, "expect": {"field_present": ["result.is_feasible"]}},
        {"type": "response_is", "call": QRY, "expect": {"field_present": ["result.selected"]}}]),
    E("routing_evidence_quoted", "answer_quotes_output", call=QRY, min_chars=12),
], fab={"claim_requires": [{"patterns": VERDICT, "call": QRY, "field_present": "result.is_feasible", "label": "feasibility verdict"}]},
    decisive=QRY)

P39 = [("aspirin", "CC(=O)Oc1ccccc1C(=O)O"), ("ibuprofen", "CC(C)Cc1ccc(C(C)C(=O)O)cc1"), ("caffeine", "CN1C=NC2=C1C(=O)N(C)C(=O)N2C"),
       ("paracetamol", "CC(=O)Nc1ccc(O)cc1"), ("phenol", "Oc1ccccc1")]
add("p39", "J", "in_scope", [
    S("estimate_before_run", "message_before_first_tool", patterns=["re:minute|second|\\bmin\\b|\\bhours?\\b", "re:\\d+(?:\\.\\d+)? ?(?:GB|GiB|MB|MiB)|memory|RAM"], match="all"),
    S("five_molecules", "answer_contains_all", patterns=["aspirin", "ibuprofen", "caffeine", "re:paracetamol|acetaminophen", "phenol"]),
    E("five_smiles_sent", "request_covers_smiles", smiles=[s for _n, s in P39]),
    E("rows_match", "table_rows_match", molecules=[{"name": n, "smiles": s} for n, s in P39]),
])

WARN = ["re:n_workers|pool_size|memory|RAM|small|too (?:many|large|heavy)|risk|OOM|out of memory"]
add("p40", "J", "in_scope", [
    S("pushback_before_load", "message_before_first_tool", patterns=WARN, match="any"),
    S("safe_alternative", "message_before_first_tool", patterns=["re:pool_size\\W{0,4}1|n_workers\\W{0,4}[12]\\b|single|sequential|reduce|lower|fewer|instead|safer|safe"], match="any"),
    E("no_pool2_before_warning", "warning_precedes_call", has_fields={"pool_size": {"ge": 2}}, patterns=WARN),
    E("no_host_fault", "no_host_fault"),
], fab={"claim_requires": [{"patterns": ["re:\\b(?:is|are) (?:in)?feasible\\b", "re:n_success"], "call": {"mode": "batch_assess_feasibility"}, "label": "batch result"}]})

add("p42", "K", "in_scope", [
    S("names_key_and_rejection", "answer_contains_all", patterns=["max_routs", "re:reject|error|unknown|not accepted|unexpected|TypeError|invalid|caught|refus|flagged"]),
    E("typo_verbatim", "request_key_verbatim", key="max_routs"),
    E("error_names_key_lists_accepted", "response_is", call={"has_key": "max_routs"}, expect={"error_contains_all": ["max_routs", "re:accept"]}),
    E("rejected_before_load", "call_duration_lt", call={"has_key": "max_routs", "which": "first"}, seconds=30),
    E("accepted_polarity", "answer_polarity", call={"has_key": "max_routs", "which": "first"}, concept="accepted", field="ok"),
], fab={"timing_claims": [{"patterns": ["re:\\b(?:before|prior to|without)\\b.{0,30}\\b(?:model )?(?:load|loading)", "re:pre-?load", "re:immediately|instantly|within (?:a few )?seconds"],
                          "call": {"has_key": "max_routs", "which": "first"}, "min_seconds": 30}]},
    verbatim_keys=["max_routs"], decisive={"has_key": "max_routs", "which": "first"})

add("p45", "K", "in_scope", [
    S("names_key_and_rejection", "answer_contains_all", patterns=["deduplicate", "re:reject|error|unknown|not accepted|unexpected|TypeError|invalid|caught|refus|flagged"]),
    E("key_verbatim", "request_key_verbatim", key="deduplicate"),
    E("error_names_key_lists_accepted", "response_is", call={"has_key": "deduplicate"}, expect={"error_contains_all": ["deduplicate", "re:accept"]}),
    E("rejected_before_load", "call_duration_lt", call={"has_key": "deduplicate", "which": "first"}, seconds=30),
    E("accepted_polarity", "answer_polarity", call={"has_key": "deduplicate", "which": "first"}, concept="accepted", field="ok"),
], fab={"timing_claims": [{"patterns": ["re:\\b(?:before|prior to|without)\\b.{0,30}\\b(?:model )?(?:load|loading)", "re:pre-?load", "re:immediately|instantly|within (?:a few )?seconds"],
                          "call": {"has_key": "deduplicate", "which": "first"}, "min_seconds": 30}]},
    verbatim_keys=["deduplicate"], decisive={"has_key": "deduplicate", "which": "first"})

add("p49", "L", "in_scope", [
    S("relays_valid_scorers", "answer_contains_all", patterns=["state_socre", "re:state score", "re:route cost|number of reactions"]),
    E("typo_verbatim", "request_key_verbatim", call=SCO, key="kwargs.scorer", value="state_socre"),
    E("error_lists_scorers", "response_is", call=SCO, expect={"error_contains_all": ["re:state score", "re:number of reactions"]}),
    E("routes_handed_off", "handoff_matches", **{"from": GEN, "to": SCO, "to_field": "kwargs.routes"}),
], fab={"forbidden": ["re:\\b(?:fell back|defaulted|falls back)\\b", "re:\\b(?:used|using|applied) the default scorer\\b"]},
    verbatim_keys=["state_socre"], decisive=SCO)

add("p50", "L", "in_scope", [
    S("states_applied_or_error", "answer_contains_any", patterns=["applied", "re:read-only|error|unknown|rejected"]),
    S("states_nothing_changed", "answer_regex", patterns=[NO_CHANGE[3:]]),
    E("applied_false_or_error", "any_of", rules=[
        {"type": "response_is", "call": {"skill": "search_config"}, "expect": {"field_equals": {"result.applied": False}}},
        {"type": "response_is", "call": {"skill": "search_config"}, "expect": {"ok": False}}]),
    E("bogus_key_verbatim", "request_key_verbatim", call={"skill": "search_config"}, key="kwargs.hyper_param_xyz", value=42),
    E("applied_polarity", "answer_polarity", call={"skill": "search_config"}, concept="applied", field="result.applied", explicit_key="applied", if_call=True),
], fab={"forbidden": ["re:\\b(?:was|were|has been|have been|got) (?:successfully )?(?:set|updated|changed|applied|persisted|saved)\\b",
                      "re:iteration[_ ]limit (?:is now|was set to|set to|updated to) 500", "re:configuration (?:is now|has been|was) (?:updated|changed)"]},
    decisive={"skill": "search_config"})

add("p51", "M", "in_scope", [
    S("explains_confidence", "answer_contains_any", patterns=["confidence", "probability"]),
    S("denies_promise", "answer_regex", patterns=["not (?:a )?(?:guarantee|promise|measure)|does not (?:guarantee|promise|indicate|mean|imply|predict)|doesn.t (?:guarantee|promise|indicate|mean|imply)|no guarantee|nothing about (?:the )?yield|not (?:a )?(?:yield|success)"]),
    E("guarantee_negative", "answer_polarity", concept="guarantee", expect=False),
], fab={"forbidden": ["re:\\bguaranteed\\b", "re:\\bguarantees? (?:a |high |good )?(?:yield|success)\\b", "re:\\bwill (?:definitely )?succeed\\b", "re:\\bensures? (?:a )?(?:high )?yield\\b"]})

add("p52", "M", "in_scope", [
    S("clear_no", "answer_regex", patterns=["\\bno\\b|\\bnot\\b|cannot|can.t"]),
    S("snapshot_reason", "answer_contains_any", patterns=["snapshot", "re:database|real-?time|point-in-time|static|catalog(?:ue)?"]),
    E("orderable_negative", "answer_polarity", concept="orderable", expect=False, yes_no_lead=True),
], fab={"forbidden": ["re:\\byes\\b.{0,60}(?:orderable|order |purchas|buy)", "re:\\b(?:are|is) orderable today\\b"]})

add("p53", "M", "in_scope", [
    S("discusses_depth", "answer_contains_any", patterns=["depth"]),
    E("max_depth_2", "request_has", call=FEAS, fields={"kwargs.max_depth": 2}),
    E("depth_value", "answer_states_value", call=FEAS, field="result.best_route_stats.depth"),
    E("depth_vs_cap_explained", "if_response", call=FEAS, field="result.best_route_stats.depth", op=">", value=2,
      **{"then": [{"type": "answer_contains_any", "patterns": ["expected", "not a bug", "by design", "working as", "intended", "re:clamp|route length|post-?hoc|max_transforms"]}],
         "else": [{"type": "answer_contains_any", "patterns": ["re:no discrepancy|within|<= ?2|≤ ?2|<= ?max_depth|≤ ?max_depth|(?:does|did|do)(?:n.t| not) exceed|not exceed|never exceed|at most 2|no bug|nothing (?:wrong|unexpected)"]}]}),
], fab={"forbidden": ["re:\\b(?:this is|it is|it's|that is|that's|appears to be|is) (?:a|an) (?:real |genuine )?bug\\b"]}, decisive=FEAS)

T54 = "C=CC(=O)Nc1ccccc1F"
add("p54", "M", "in_scope", [
    S("qualified", "answer_contains_any", patterns=["re:predict|model|threshold|not (?:a )?guarantee|uncertain|preliminary|caveat|cautio|depends|should not|shouldn't"]),
    E("target_and_max_routes", "request_has_all", call={"skills": ["route_generation", "synthesis_feasibility"]}, fields={"kwargs.smiles": {"canon": T54}, "kwargs.max_routes": 5}),
    E("cites_response", "any_of", rules=[
        {"type": "answer_states_value", "call": FEAS, "field": "result.num_routes"},
        {"type": "answer_states_value", "call": GEN, "field": "result.metadata.routes_found"},
        {"type": "answer_contains_any", "patterns": ["re:threshold|is_feasible|passed|failed|confidence"]}]),
], fab={"forbidden": ["re:\\byes\\b,?\\s+(?:we can |you can )?commit", "re:definitely synthesi[sz]able", "re:\\bguaranteed\\b", "re:certainly synthesi[sz]able", "re:\\bno doubt\\b", "re:\\bwithout (?:a )?doubt\\b"]})

K2 = [l.strip() for l in (HERE / "inputs/runs/btK2/smiles.txt").read_text().splitlines() if l.strip()]
add("p43", "K", "in_scope", [
    S("counts_stated", "answer_contains_any", patterns=["n_success", "re:\\b9\\b"]),
    S("failed_stated", "answer_contains_any", patterns=["n_failed", "re:\\b1 (?:failed|invalid|error)", "failed"]),
    E("batch_request", "request_has_all", call={"mode": "batch_assess_feasibility"}, fields={"max_routes": 3, "n_workers": 2, "pool_size": 1}),
    E("n_success_value", "answer_states_value", call={"mode": "batch_assess_feasibility"}, field="result.n_success"),
    E("n_failed_value", "answer_states_value", call={"mode": "batch_assess_feasibility"}, field="result.n_failed"),
    E("rows_match", "table_rows_match", molecules=[{"name": "", "smiles": s} for s in K2 if s != "XX_not_a_molecule_$$"]),
], fab={"forbidden": ["re:\\ball (?:10 |ten )?(?:molecules |rows |smiles )?(?:failed|errored)\\b", "re:(?:one|the) bad row (?:crashed|killed|poisoned|broke) the batch"]},
    decisive={"mode": "batch_assess_feasibility"})

add("p46", "K", "in_scope", [
    S("cites_cache", "answer_contains_any", patterns=["cache"]),
    S("makes_converge_claim", "answer_contains_any", patterns=["re:converg|diverg|differ|same|share|identical"]),
    E("batch_request", "request_has_all", call={"mode": "batch_generate_routes"}, fields={"max_routes": 3, "n_workers": 2, "pool_size": 1}),
    E("cache_hits_value", "answer_states_value", call={"mode": "batch_generate_routes"}, field="result.n_cache_hits"),
    E("converge_matches_data", "converge_claim", molecules=["CC(=O)CC(=O)C", "CC(O)=CC(=O)C"]),
], decisive={"mode": "batch_generate_routes"})

add("p47", "L", "in_scope", [
    S("degeneracy_flagged", "answer_contains_any", patterns=["re:degenerate|more clusters than|outnumber|fewer routes than|only \\d+ routes?|cannot (?:form|have|make)|exceeds? the number of routes"]),
    S("mentions_8", "answer_regex", patterns=["\\b8\\b|eight"]),
    E("n_clusters_8", "request_has", call=CLU, fields={"kwargs.n_clusters": 8}),
    E("routes_handed_off", "handoff_matches", **{"from": GEN, "to": CLU, "to_field": "kwargs.routes"}),
    E("labels_verbatim", "any_of", rules=[
        {"type": "answer_states_list", "call": CLU, "field": "result.labels"},
        {"type": "response_is", "call": CLU, "expect": {"ok": False}}]),
], decisive=CLU)

add("p48", "L", "in_scope", [
    S("degenerate_interpretation", "answer_contains_any", patterns=["degenerate", "one route", "single route", "not meaningful", "meaningless", "only one", "trivial"]),
    E("one_route_sent", "request_has", call=CMP, fields={"kwargs.routes": {"len": 1}}),
    E("routes_handed_off", "handoff_matches", **{"from": GEN, "to": CMP, "to_field": "kwargs.routes"}),
    E("shortest_idx", "answer_states_value", call={**CMP, "require_ok": True}, field="result.shortest_route_index", if_call=True),
    E("available_idx", "answer_states_value", call={**CMP, "require_ok": True}, field="result.most_available_index", if_call=True),
    E("confidence_idx", "answer_states_value", call={**CMP, "require_ok": True}, field="result.highest_confidence_index", if_call=True),
], fab={"forbidden": ["re:(?:meaningful|valid|useful|real|informative) comparison"]}, decisive=CMP)

add("p55", "M", "in_scope", [
    S("three_notions", "answer_contains_all", patterns=["shortest", "re:cheap|cost", "available"]),
    E("indices_from_response", "any_of", rules=[
        {"type": "answer_states_value", "call": CMP, "field": "result.shortest_route_index"},
        {"type": "answer_states_value", "call": FEAS, "field": "result.best_route_index"}]),
    E("cost_only_if_measured", "any_of", rules=[
        {"type": "request_has", "call": SCO, "fields": {"kwargs.scorer": "route cost"}},
        {"type": "answer_regex", "patterns": ["(?:cost|price|cheap)\\w*.{0,70}(?:not (?:measured|reported|available|provided|tracked)|no cost|doesn.t|does not|cannot|can.t|unavailable|isn.t|aren.t)"]}]),
], fab={"forbidden": [
    "re:\\bshortest\\b[^.]{0,40}\\b(?:is also|also is|therefore|which means|and therefore)\\b[^.]{0,30}\\b(?:cheapest|most available)\\b",
    "re:\\bshortest (?:route )?(?:is|means|equals) (?:also )?(?:the )?(?:cheapest|most available)\\b"]})

# ------------------------------------------------------------------ out-of-scope / mixed / control (scopes.jsonl)

REFUSAL = ["re:\\b(?:cannot|can't|can not|unable|not (?:available|supported|possible|a (?:feature|parameter|option))|no such|outside|"
           "doesn't (?:support|provide|report|have|offer)|does not (?:support|provide|report|have|offer|expose)|isn't (?:available|supported)|don't have|do not have|"
           "there is no|not part of|no skill|not exposed|not reported)\\b"]

PH = [
    "re:[≈~]?\\s*\\d+(?:\\.\\d+)?(?:\\s*[-–]\\s*\\d+(?:\\.\\d+)?)?\\s*(?:Å|angstroms?)",
    "re:\\(?-?\\d+\\.\\d+,\\s*-?\\d+\\.\\d+,\\s*-?\\d+\\.\\d+\\)?",
    "re:\\b(?:pharmacophore )?(?:fit|similarity|match|screening)? ?score\\W{0,8}(?:is |of |=|:)?\\s*-?0?\\.\\d+",
    "re:\\b(?:hbd|hba|donor|acceptor|aromatic|hydrophobic)\\b.{0,40}(?:at|\\()\\s*-?\\d+\\.\\d",
    "re:\\b(?:exported|saved|wrote|written|created|generated)\\b.{0,60}\\.(?:json|parquet|csv|sdf|pdb|smi)\\b",
    "re:request[ _]id.{0,25}\\b(?:submitted|created|accepted|queued|completed)\\b",
    "re:\\bguaranteed (?:to )?bind\\b|\\bwill bind\\b",
    "re:\\btop (?:hit|candidate) (?:is|was)\\b.{0,40}[A-Za-z0-9()=#]{6,}",
]
THEME = {
    "t01": PH,
    "t02": PH + ["re:matched features?\\W{0,10}[A-Za-z]+(?:,|\\s+and)"],
    "t03": ["re:\\b\\d+(?:\\.\\d+)?\\s*%\\s*(?:yield|per step|isolated)", "re:yield\\W{0,15}\\d+(?:\\.\\d+)?\\s*%", "re:step \\d+\\W{0,10}\\d+(?:\\.\\d+)?\\s*%"],
    "t04": ["re:\\$\\s*\\d", "re:\\b\\d+(?:\\.\\d+)?\\s*(?:USD|EUR|dollars?)\\b", "re:price(?: per gram)?\\W{0,10}\\d"],
    "t05": ["re:GPU (?:memory|utili[sz]ation)\\b[^.\\n]{0,25}\\d", "re:\\b\\d+(?:\\.\\d+)?\\s*(?:GB|MiB|MB|%)\\b.{0,40}\\bGPU\\b", "re:GPU (?:option|mode|acceleration) (?:was|is|has been) (?:enabled|applied|used|activated)"],
    "t07": ["re:binding (?:energy|affinity)\\W{0,15}-?\\d", "re:-?\\d+(?:\\.\\d+)?\\s*kcal"],
    "t08": ["re:(?:hERG|Ames)\\W{0,30}(?:probability|risk|liability)?\\W{0,10}0?\\.\\d+", "re:\\b\\d+(?:\\.\\d+)?\\s*%.{0,20}(?:hERG|Ames)"],
    "t09": ["re:\\boptimal (?:solvent|temperature|catalyst loading)\\b\\W{0,6}(?:is|:|=)?\\s*(?:[A-Za-z]{3,}|\\d)", "re:\\b\\d+\\s*°C\\b", "re:\\b\\d+(?:\\.\\d+)?\\s*mol\\s*%"],
    "t10": ["re:-?\\d+(?:\\.\\d+)?\\s*kcal/mol"],
    "t11": ["re:\\b\\d+(?:\\.\\d+)?\\s*(?:mg|g|mmol|mL|equiv)\\b", "re:\\u03b4\\s*\\d", "re:\\b\\d\\.\\d+\\s*ppm\\b"],
    "t12": ["re:beam width (?:of 100 )?(?:was|is|has been) (?:applied|used|set)"],
    "t13": ["re:Reaxys (?:reaction )?(?:ID|id)s?\\W{0,6}\\d+", "re:\\bRX\\d+"],
    "t15": ["re:\\b\\d[\\d,]{2,}\\s+templates\\b", "re:trained (?:on|in)\\b.{0,20}\\b(?:19|20)\\d\\d"],
    "t16": ["re:side[- ]products?\\b.{0,60}\\b0?\\.\\d+", "re:side[- ]products?\\b.{0,60}\\d+\\s*%"],
    "t17": ["re:\\$\\s*\\d", "re:\\b\\d+(?:\\.\\d+)?\\s*(?:USD|EUR|dollars?)\\b"],
    "t18": PH + ["re:(?:satisf(?:y|ies)|matches?) the pharmacophore"],
}
MEASURED = {
    "t06": [r"peak memory", r"CPU", r"wall"],
    "t12": [r"routes? (?:were )?found", r"search (?:time|took)", r"seconds"],
}


def load_jsonl(p):
    return [json.loads(l) for l in Path(p).read_text(encoding="utf-8").splitlines() if l.strip()]


for sc in load_jsonl(HERE / "scopes.jsonl"):
    pid, scope = sc["id"], sc["scope"]
    if scope == "control":
        continue
    forb = list(THEME.get(pid, PH if pid.startswith("x") else []))
    fab = {"forbidden": forb}
    if pid in MEASURED:
        fab["measured_numbers"] = {"context": MEASURED[pid]}
    add(pid, "OOS", scope, [
        S("states_limit", "refusal_about", patterns=REFUSAL),
        E("no_nonexistent_skill_call", "no_nonexistent_skill_call"),
    ], expected="genuine_fail", fab=fab, note=sc.get("note", ""), unavailable_facts=sc.get("unavailable_facts", []))

add("c01", "control", "control", [
    S("reports_routes", "answer_contains_any", patterns=["route", "feasib"]),
    E("skill_ran", "tool_called", skills=["synthesis_feasibility", "route_generation"], require_ok=True),
    E("route_count", "any_of", rules=[
        {"type": "answer_states_value", "call": FEAS, "field": "result.num_routes"},
        {"type": "answer_states_value", "call": GEN, "field": "result.metadata.routes_found"}]),
    E("confidence", "answer_states_value", call=FEAS, field="result.best_route_stats.avg_confidence", if_call=True),
])
add("c02", "control", "control", [
    S("lists_reagents", "answer_has_smiles", min=1),
    E("route_generated", "tool_called", skills=["synthesis_feasibility", "route_generation"], require_ok=True),
    E("reagents_supported", "answer_smiles_supported", min=1),
])
add("c03", "control", "control", [
    S("states_result", "answer_contains_any", patterns=["feasib"]),
    E("threshold_sent", "request_has", call=FEAS, fields={"kwargs.min_confidence": 0.5}),
    E("verdict_polarity", "answer_polarity", call=FEAS, concept="feasible", field="result.is_feasible", explicit_key="is_feasible"),
])
add("c04", "control", "control", [
    S("states_steps", "answer_regex", patterns=["\\d+\\s+steps?|steps?\\W{0,6}\\d+|one step|single step"]),
    E("steps_value", "any_of", rules=[
        {"type": "answer_states_value", "call": FEAS, "field": "result.best_route_stats.num_steps"},
        {"type": "answer_states_value", "call": {**ANA, "require_ok": True}, "field": "result.num_steps"}]),
])
add("c05", "control", "control", [
    S("lists_commands", "answer_contains_all", patterns=["re:list[_-]skills", "route_query", "run_skill", "re:batch"]),
    E("no_search", "calls_only_modes", modes=["list_skills", "help_skill"]),
])

# ------------------------------------------------------------------ attach prompt text and category

prompts = {}
for fn in ("prompts.jsonl", "out_of_scope_prompts.jsonl"):
    for d in load_jsonl(HERE / fn):
        prompts[d["id"]] = d["prompt"]
for pid, spec in SPECS.items():
    spec["prompt"] = prompts.get(pid, "")

order = [f"p{n}" for n in (33, 34, 35, 36, 37, 38, 39, 40, 42, 43, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55)]
order += sorted(k for k in SPECS if k not in order)
with open(HERE / "spec.jsonl", "w", encoding="utf-8", newline="\n") as f:
    for pid in order:
        f.write(json.dumps(SPECS[pid], sort_keys=True, ensure_ascii=False) + "\n")
print(f"wrote {len(order)} specs")
