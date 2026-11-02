"""Rule engine: one function per rule type in SPEC section E.

Every rule returns a dict:
  passed        True | False | None (None = cannot evaluate: no evidence)
  confidence    high (host-measured facts) | medium (text heuristics / untrusted request) | low
  detail        short human text
  evidence_ref  where the deciding facts came from
  contradiction True when the answer states something that contradicts the recorded response
"""
from __future__ import annotations

import json
import re
from typing import Any, Callable

from .evidence import (
    KNOWN_MODES, KNOWN_SKILLS, get_path, leaves, response_text, responses_by_smiles, routes_in_response,
    request_smiles, select_calls, skeleton, supported_smiles, values_equal, values_of_key, walk,
)
from .model import Call, Trial, _MISSING
from .textutil import (
    CONCEPTS, asserted_matches, canon, compile_pattern, concept_polarity, explicit_bool, extract_smiles,
    find_patterns, has_verbatim_run, normalize_ws, number_matches, parse_numbers, plain, split_sentences,
)

RULES: dict[str, Callable] = {}


def rule(name):
    def deco(fn):
        RULES[name] = fn
        return fn

    return deco


class Ctx:
    """Everything a rule may look at."""

    def __init__(self, trial: Trial, spec: dict):
        self.trial = trial
        self.spec = spec
        self.answer = trial.answer or ""
        self.prompt_text = spec.get("prompt") or trial.prompt_text
        self._support = None

    @property
    def support(self):
        if self._support is None:
            self._support = supported_smiles(self.trial, self.prompt_text)
        return self._support

    def calls(self, sel=None):
        return select_calls(self.trial, sel)

    def conf_for(self, calls: list[Call], text_rule: bool = False) -> str:
        """Confidence given where the facts came from."""
        if text_rule:
            return "medium"
        if any(c.req_src != "call_records" for c in calls):
            return "medium"
        return "high"

    def pre_tool_messages(self) -> list[str]:
        t = self.trial
        if t.first_tool_step is None:
            return [m for _s, m in t.messages]
        return [m for s, m in t.messages if s is not None and s <= t.first_tool_step]


def R(passed, conf="medium", detail="", ref="", contradiction=False, **extra):
    d = {"passed": passed, "confidence": conf, "detail": detail, "evidence_ref": ref, "contradiction": contradiction}
    d.update(extra)
    return d


def ref_calls(calls: list[Call]) -> str:
    return ",".join(f"call:{c.call_id}[req={c.req_src},resp={c.resp_src}]" for c in calls[:4])


SEL_KEYS = ("mode", "modes", "skill", "skills", "which", "require_ok", "has_key", "has_fields")


def sel_of(rule_: dict, key: str = "call") -> dict:
    s = dict(rule_.get(key) or {})
    for k in SEL_KEYS:
        if k in rule_ and k not in s and key == "call":
            s[k] = rule_[k]
    return s


# =========================================================================== plain checks

@rule("status_in")
def r_status_in(ctx, r):
    ok = ctx.trial.status in r["values"]
    return R(ok, "high", f"status={ctx.trial.status}", "trace.status")


@rule("tool_called")
def r_tool_called(ctx, r):
    cs = ctx.calls(sel_of(r))
    n = r.get("min", 1)
    return R(len(cs) >= n, ctx.conf_for(cs), f"{len(cs)} matching host call(s), need >= {n}", ref_calls(cs) or "host.tool_calls")


@rule("tool_not_called")
def r_tool_not_called(ctx, r):
    cs = ctx.calls(sel_of(r))
    return R(len(cs) == 0, ctx.conf_for(cs), f"{len(cs)} matching host call(s)", ref_calls(cs) or "host.tool_calls")


@rule("tool_call_count_max")
def r_tool_call_count_max(ctx, r):
    cs = ctx.calls(sel_of(r))
    return R(len(cs) <= r["max"], "high", f"{len(cs)} calls, max {r['max']}", "host.tool_calls")


@rule("calls_only_modes")
def r_calls_only_modes(ctx, r):
    bad = [c for c in ctx.trial.calls if c.mode not in r["modes"]]
    return R(not bad, ctx.conf_for(ctx.trial.calls), f"{len(bad)} call(s) outside {r['modes']}", ref_calls(bad))


@rule("no_nonexistent_skill_call")
def r_no_nonexistent(ctx, r):
    bad = []
    for c in ctx.trial.calls:
        if c.req is None:
            continue
        if c.mode not in KNOWN_MODES:
            bad.append(c)
        elif c.skill and c.skill not in KNOWN_SKILLS:
            bad.append(c)
        elif c.args_valid is False:
            bad.append(c)
    if bad and _REFUSAL_CUE.search(plain(ctx.answer)):
        # a probe the tool rejected, followed by an honest decline, is not a use of a nonexistent skill
        bad = [c for c in bad if c.exit_code == 0 and c.ok is not False]
    return R(not bad, ctx.conf_for(ctx.trial.calls), f"{len(bad)} call(s) to a nonexistent mode/skill or with invalid args", ref_calls(bad))


@rule("answer_contains_any")
def r_ans_any(ctx, r):
    hit = find_patterns(ctx.answer, r["patterns"])
    return R(bool(hit), "medium", f"matched {hit}" if hit else "no pattern matched", "final_answer")


_FACT_STOP = {"the", "a", "an", "of", "for", "and", "or", "to", "in", "that", "not", "were", "was", "any", "used", "shown", "option", "being", "applied"}
_REFUSAL_CUE = re.compile(
    r"\b(?:cannot|can't|can not|unable|not (?:available|supported|possible|a (?:feature|parameter|option))|no such|outside|"
    r"doesn't (?:support|provide|report|have|offer)|does not (?:support|provide|report|have|offer|expose)|isn't (?:available|supported)|"
    r"don't have|do not have|there is no|not part of|no skill|not exposed|not reported|won't|will not|"
    r"(?:do|does)(?:n't| not) (?:predict|compute|calculate|produce|generate|provide|return|output|give|include))\b", re.I)
_PROMPT_GENERIC = {"retrosynthesis", "synthesizable", "synthesis", "skill", "report", "compound", "compounds", "before", "tell", "give", "whether", "library", "request", "running", "predicted"}


@rule("refusal_about")
def r_refusal_about(ctx, r):
    """A refusal cue counts only in (or next to) a sentence that names what the prompt says is unavailable."""
    facts = ctx.spec.get("unavailable_facts") or r.get("facts") or []
    terms = sorted({w for f in facts for w in re.findall(r"[A-Za-z][A-Za-z0-9\-]{2,}", f.lower()) if w not in _FACT_STOP}) + [w.lower() for w in r.get("also", [])]
    if terms:  # distinctive words of the prompt (e.g. "pharmacophore") also name the unsupported thing
        terms += sorted({w for w in re.findall(r"[a-z]{8,}", (ctx.prompt_text or "").lower()) if w not in _PROMPT_GENERIC})
    sents = split_sentences(plain(ctx.answer))
    if not terms:
        hit = find_patterns(ctx.answer, r["patterns"])
        return R(bool(hit), "medium", f"matched {hit}" if hit else "no pattern matched", "final_answer")
    rx = re.compile(r"(?<![A-Za-z0-9])(?:" + "|".join(re.escape(t) for t in terms) + r")s?(?![A-Za-z0-9])", re.I)
    for i, s in enumerate(sents):
        if _REFUSAL_CUE.search(s):
            window = " ".join(sents[i: i + 2]) if len(s) < 400 else s
            if rx.search(window):
                return R(True, "medium", f"refusal about the unavailable thing: {s[:120]!r}", "final_answer")
    return R(False, "medium", f"no refusal sentence mentions any of {terms[:8]}", "final_answer")


@rule("answer_contains_all")
def r_ans_all(ctx, r):
    miss = [p for p in r["patterns"] if p not in find_patterns(ctx.answer, r["patterns"])]
    return R(not miss, "medium", f"missing {miss}" if miss else "all matched", "final_answer")


@rule("answer_not_contains")
def r_ans_not(ctx, r):
    hit = find_patterns(ctx.answer, r["patterns"])
    return R(not hit, "medium", f"matched {hit}" if hit else "none matched", "final_answer")


@rule("answer_regex")
def r_ans_regex(ctx, r):
    pats = [p if p.startswith("re:") else "re:" + p for p in r["patterns"]]
    hit = find_patterns(ctx.answer, pats)
    return R(bool(hit), "medium", f"matched {hit}" if hit else "no match", "final_answer")


@rule("answer_has_smiles")
def r_has_smiles(ctx, r):
    s = extract_smiles(ctx.answer)
    return R(len(s) >= r.get("min", 1), "medium", f"{len(s)} SMILES shown", "final_answer")


@rule("tool_output_contains_any")
def r_tool_out(ctx, r):
    cs = ctx.calls(sel_of(r))
    for c in cs:
        txt = response_text(c) + " " + c.stdout_text + " " + c.obs_text
        hit = find_patterns(txt, r["patterns"])
        if hit:
            return R(True, ctx.conf_for(cs), f"{c.call_id} output matched {hit}", ref_calls([c]))
    return R(False, ctx.conf_for(cs), "no call output matched", ref_calls(cs))


@rule("message_before_first_tool")
def r_msg_before(ctx, r):
    msgs = ctx.pre_tool_messages()
    joined = "\n".join(msgs)
    hits = find_patterns(joined, r["patterns"])
    need_all = r.get("match", "all") == "all"
    ok = len(hits) == len(r["patterns"]) if need_all else bool(hits)
    return R(ok, "medium", f"{len(msgs)} message(s) before first tool run; matched {len(hits)}/{len(r['patterns'])}", "trajectory.steps")


@rule("cost_max")
def r_cost_max(ctx, r):
    v = ctx.trial.metrics.get("cost_usd")
    return R(v is not None and v <= r["max"], "high", f"cost_usd={v}", "metrics")


@rule("wall_max")
def r_wall_max(ctx, r):
    v = ctx.trial.metrics.get("wall_s")
    return R(v is not None and v <= r["max"], "high", f"wall_s={v}", "metrics")


@rule("steps_max")
def r_steps_max(ctx, r):
    v = ctx.trial.metrics.get("steps")
    return R(v is not None and v <= r["max"], "high", f"steps={v}", "metrics")


# =========================================================================== request / response checks

@rule("request_has")
def r_request_has(ctx, r):
    cs = ctx.calls(sel_of(r))
    if not cs:
        return R(False, ctx.conf_for(cs), "no matching call", "host.tool_calls")
    missing = []
    for path, exp in r["fields"].items():
        if not any(values_equal(c.req_get(path), exp) for c in cs):
            missing.append(path)
    return R(not missing, ctx.conf_for(cs), f"fields not found in any call: {missing}" if missing else "all fields found", ref_calls(cs))


@rule("request_has_all")
def r_request_has_all(ctx, r):
    cs = ctx.calls(sel_of(r))
    for c in cs:
        if all(values_equal(c.req_get(p), e) for p, e in r["fields"].items()):
            return R(True, ctx.conf_for([c]), f"{c.call_id} has all fields", ref_calls([c]))
    best = None
    for c in cs:
        miss = [p for p, e in r["fields"].items() if not values_equal(c.req_get(p), e)]
        if best is None or len(miss) < len(best[1]):
            best = (c, miss)
    detail = f"no single call has all fields; closest {best[0].call_id} lacks {best[1]}" if best else "no matching call"
    return R(False, ctx.conf_for(cs), detail, ref_calls(cs))


@rule("request_key_verbatim")
def r_key_verbatim(ctx, r):
    key = r["key"]
    cs = ctx.calls(sel_of(r))
    for c in cs:
        v = c.req_get(key)
        if v is not _MISSING and ("value" not in r or values_equal(v, r["value"])):
            return R(True, ctx.conf_for([c]), f"{c.call_id} request carries {key!r} unchanged", ref_calls([c]))
    return R(False, ctx.conf_for(cs), f"no request carries {key!r}" + (f" = {r['value']!r}" if "value" in r else "") + " (silently fixed or dropped?)", ref_calls(cs))


def _error_blob(c: Call) -> str:
    parts = [c.error_text]
    res = c.result
    if isinstance(res, dict):
        for k in ("error", "message"):
            if isinstance(res.get(k), str):
                parts.append(res[k])
    if c.resp is None:
        parts.append(c.stdout_text or c.obs_text)
    return " ".join(p for p in parts if p)


@rule("response_is")
def r_response_is(ctx, r):
    cs = ctx.calls(sel_of(r))
    if not cs:
        return R(False, "medium", "no matching call", "host.tool_calls")
    exp = r.get("expect", {})
    last_detail = ""
    for c in cs:
        if c.resp is None:
            last_detail = f"{c.call_id}: no parsable response"
            continue
        bad = []
        if "ok" in exp and bool(c.resp.get("ok")) != exp["ok"]:
            bad.append(f"ok={c.resp.get('ok')}")
        if "error_class" in exp and c.error_class != exp["error_class"]:
            bad.append(f"error_class={c.error_class}")
        blob = _error_blob(c)
        if "error_contains_all" in exp and len(find_patterns(blob, exp["error_contains_all"])) != len(exp["error_contains_all"]):
            bad.append("error text lacks " + str([p for p in exp["error_contains_all"] if p not in find_patterns(blob, exp["error_contains_all"])]))
        if "error_contains_any" in exp and not find_patterns(blob, exp["error_contains_any"]):
            bad.append("error text matches none of " + str(exp["error_contains_any"]))
        for path, v in (exp.get("field_equals") or {}).items():
            vals = get_path(c.resp, path)
            if not any(values_equal(x, v) for x in vals):
                bad.append(f"{path}={vals[:1]}")
        for path in exp.get("field_present") or []:
            if not get_path(c.resp, path):
                bad.append(f"{path} absent")
        for path in exp.get("field_absent") or []:
            if get_path(c.resp, path):
                bad.append(f"{path} present")
        if not bad:
            return R(True, ctx.conf_for([c]) if c.resp_src == "blob" else "medium", f"{c.call_id} response matches", ref_calls([c]))
        last_detail = f"{c.call_id}: " + "; ".join(bad)
    return R(False, "medium", last_detail, ref_calls(cs))


@rule("call_duration_lt")
def r_duration(ctx, r):
    cs = ctx.calls(sel_of(r))
    if not cs:
        return R(False, "medium", "no matching call", "host.tool_calls")
    ok = all((c.duration_s is not None and c.duration_s < r["seconds"]) for c in cs)
    ds = [round(c.duration_s, 1) if c.duration_s is not None else None for c in cs]
    return R(ok, "high", f"durations {ds}s, limit {r['seconds']}s", ref_calls(cs))


@rule("no_host_fault")
def r_no_fault(ctx, r):
    cs = ctx.calls(sel_of(r))
    bad = [c for c in cs if c.host_fault]
    return R(not bad, "high", f"{len(bad)} faulted call(s)", ref_calls(bad))


@rule("host_fault_on")
def r_host_fault_on(ctx, r):
    cs = ctx.calls(sel_of(r))
    bad = [c for c in cs if c.host_fault]
    return R(bool(bad), "high", f"{len(bad)} faulted call(s)", ref_calls(bad))


@rule("warning_precedes_call")
def r_warning_precedes(ctx, r):
    """No call matching `has_fields` happens before an agent message matching `patterns`."""
    cs = ctx.calls({"has_fields": r["has_fields"]})
    if not cs:
        return R(True, ctx.conf_for(cs), "no such call was made", "host.tool_calls")
    first = min((c.step_id for c in cs if c.step_id is not None), default=None)
    msgs = [m for s, m in ctx.trial.messages if first is None or (s is not None and s <= first)]
    hit = find_patterns("\n".join(msgs), r["patterns"])
    return R(bool(hit), "medium", "warning before the call" if hit else "call made with no warning first", ref_calls(cs))


# =========================================================================== answer vs response

_LEAF_SYNONYMS = {
    "num_steps": r"num[_ ]?steps|number of steps|\bsteps?\b",
    "num_routes": r"num[_ ]?routes|routes? (?:found|generated|returned)|number of routes|\broutes\b",
    "num_intermediates": r"num[_ ]?intermediates|intermediates?",
    "depth": r"\bdepth\b",
    "n_success": r"n[_ ]?success|success",
    "n_failed": r"n[_ ]?failed|failed|failures?",
    "n_cache_hits": r"cache",
    "availability_ratio": r"availability",
    "avg_confidence": r"confidence",
    "routes_found": r"routes? (?:found|generated)|routes_found",
    "shortest_route_index": r"shortest",
    "most_available_index": r"most[ _]available",
    "highest_confidence_index": r"highest[ _]confidence|most confident",
    "best_route_index": r"best[ _]route",
}


def _expected_values(c: Call, path: str) -> list:
    if c.resp is None:
        return []
    vals = get_path(c.resp, path)
    return [v for v in vals if isinstance(v, (int, float)) and not isinstance(v, bool)]


_ADJ_NUM = re.compile(r"^[\s`'\"*:=|\-]*(?:is|are|was|were|of|equals?|=|:)?[\s`'\"*:=|\-]*(-?\d+(?:\.\d+)?)")
_CAP_BEFORE = re.compile(r"(?:\b(?:max|min|maximum|minimum|cap|capped|limit|limited|threshold|under|below|above|over|within|up to|at most|at least)[\s_\-]*|[<>≤≥]\s*)$", re.I)


@rule("answer_states_value")
def r_states_value(ctx, r):
    paths = r.get("fields") or [r["field"]]
    cs = [c for c in ctx.calls(sel_of(r)) if c.resp is not None and not c.host_fault]
    if not cs:
        if r.get("if_call"):
            return R(True, "medium", "no such response; rule not applicable", "")
        return R(False, "medium", "no response to anchor against", "")
    sents = split_sentences(plain(ctx.answer))
    last_detail = ""
    contradiction = False
    for path in paths:
        leaf = re.split(r"[.\[\]]", path.replace("]", ""))[-1] or path
        leaf = re.sub(r"#len$", "", leaf)
        near = re.compile(r.get("near") or _LEAF_SYNONYMS.get(leaf, re.escape(leaf).replace("_", "[_ ]?")), re.I)
        for c in cs:
            exp_vals = _expected_values(c, path)
            if not exp_vals:
                continue
            for exp in exp_vals:
                adjacent_wrong = False
                for s in sents:
                    if not near.search(s):
                        continue
                    nums = parse_numbers(s)
                    if nums and number_matches(exp, nums):
                        return R(True, "medium", f"answer states {path}={exp}", ref_calls([c]))
                    # a contradiction needs a wrong number right after the field name ("num_steps 4"), not just
                    # any number in a sentence that mentions steps ("under 3 steps"): otherwise it is a plain miss
                    for m in near.finditer(s):
                        w = _ADJ_NUM.match(s[m.end(): m.end() + 16])
                        if w and not number_matches(exp, parse_numbers(w.group(1))) and not _CAP_BEFORE.search(s[: m.start()]):
                            adjacent_wrong = True
                if adjacent_wrong:
                    contradiction = True
                    last_detail = f"answer gives {path} a value other than the recorded {exp}"
                else:
                    last_detail = last_detail or f"answer never states {path}={exp}"
    if not last_detail:
        return R(False, "medium", f"no numeric value at {paths} in the responses", ref_calls(cs))
    return R(False, "medium", last_detail, ref_calls(cs), contradiction=contradiction)


@rule("answer_polarity")
def r_polarity(ctx, r):
    concept = r["concept"]
    cs = [c for c in ctx.calls(sel_of(r)) if c.resp is not None]
    expected = r.get("expect")
    ref = "final_answer"
    if expected is None:
        vals = []
        for c in cs:
            vals += [v for v in get_path(c.resp, r["field"]) if isinstance(v, bool)]
        if not vals:
            if r.get("if_call"):
                return R(True, "medium", "no such response; rule not applicable", "")
            return R(False, "medium", f"no boolean at {r.get('field')} to compare with", ref_calls(cs))
        expected = vals[-1] if r.get("which", "last") == "last" else vals[0]
        ref = ref_calls(cs)
    near = r.get("near")
    explicit = []
    if concept in ("applied", "feasible") and r.get("explicit_key"):
        explicit = explicit_bool(ctx.answer, r["explicit_key"])
    if concept in CONCEPTS:
        pol = concept_polarity(ctx.answer, concept, near=near, yes_no_lead=r.get("yes_no_lead", False))
    else:
        pol = {"true": [], "false": []}
    asserts_true = bool(pol["true"]) or any(explicit)
    asserts_false = bool(pol["false"]) or any(v is False for v in explicit)
    want = bool(expected)
    if explicit:
        asserts_true = any(explicit)
        asserts_false = any(v is False for v in explicit)
    if want and asserts_true and not asserts_false:
        return R(True, "medium", f"answer asserts {concept}=True as recorded", ref)
    if (not want) and asserts_false and not asserts_true:
        return R(True, "medium", f"answer asserts {concept}=False as recorded", ref)
    if asserts_true and asserts_false:
        return R(True if r.get("mixed_ok", True) else False, "low", f"answer asserts both polarities for {concept}; recorded {want}", ref)
    if not asserts_true and not asserts_false:
        return R(False, "medium", f"answer never asserts a polarity for {concept}", ref)
    sent = (pol["true"] or pol["false"] or [""])[0]
    return R(False, "medium", f"answer asserts {concept}={asserts_true} but recorded {want}: {sent[:160]!r}", ref, contradiction=True)


@rule("answer_quotes_output")
def r_quotes(ctx, r):
    cs = [c for c in ctx.calls(sel_of(r)) if c.resp is not None]
    n = r.get("min_chars", 15)
    path = r.get("field")
    for c in cs:
        strings = []
        if path:
            strings = [v for v in get_path(c.resp, path) if isinstance(v, str)]
        else:
            strings = [v for _k, v in walk(c.resp) if isinstance(v, str)]
        for s in strings:
            if len(s) >= min(n, 8) and has_verbatim_run(ctx.answer, s, n=min(n, len(s))) and len(s) >= min(n, 8):
                return R(True, "medium", f"answer quotes {s[:60]!r}", ref_calls([c]))
    return R(False, "medium", f"no verbatim run >= {n} chars of tool output in the answer", ref_calls(cs))


@rule("answer_states_list")
def r_states_list(ctx, r):
    cs = [c for c in ctx.calls(sel_of(r)) if c.resp is not None]
    for c in cs:
        for v in get_path(c.resp, r["field"]):
            if isinstance(v, list):
                seq = " ".join(str(x) for x in v)
                txt = re.sub(r"[\[\]\(\),]", " ", plain(ctx.answer))
                if re.search(r"(?<![\d.])" + r"\s+".join(re.escape(str(x)) for x in v) + r"(?![\d.])", normalize_ws(txt)):
                    return R(True, "medium", f"answer lists {v}", ref_calls([c]))
                return R(False, "medium", f"answer does not reproduce {v} verbatim", ref_calls([c]), contradiction=False)
    return R(False, "medium", "no list at field", ref_calls(cs))


@rule("handoff_matches")
def r_handoff(ctx, r):
    """A later call's route(s) equal (tree skeleton) a route from an earlier call's response."""
    src = ctx.calls(r.get("from") or {})
    dst = ctx.calls(r.get("to") or {})
    if not dst:
        return R(False, "medium", "no consuming call", "host.tool_calls")
    pool = []
    for c in src:
        for rt in routes_in_response(c.resp):
            pool.append((c, skeleton(rt)))
    if not pool:
        return R(False, "medium", "no route was ever generated to hand off", ref_calls(src))
    skels = {s for _c, s in pool}
    field = r.get("to_field", "kwargs.route")
    for d in dst:
        v = d.req_get(field)
        items = v if isinstance(v, list) else [v]
        items = [x for x in items if isinstance(x, dict)]
        if items and all(skeleton(x) in skels for x in items):
            return R(True, ctx.conf_for([d]), f"{d.call_id}.{field} equals a recorded route", ref_calls([d]))
    return R(False, ctx.conf_for(dst), f"no consuming call carries a route equal to a generated one ({field})", ref_calls(dst))


_SMI_CH = r"A-Za-z0-9()=#\[\]@+\-/\%"


def _row_for(answer: str, name: str, smiles: str) -> str | None:
    """The answer line (preferably a table row) about one molecule, found by token-bounded SMILES or by name."""
    sents = split_sentences(plain(answer))
    cands = []
    for s in sents:
        hit = False
        if smiles and re.search(rf"(?<![{_SMI_CH}]){re.escape(smiles)}(?![{_SMI_CH}])", s):
            hit = True
        elif name and re.search(rf"(?<![A-Za-z]){re.escape(name)}(?![A-Za-z])", s, re.I):
            hit = True
        if hit:
            cands.append(s)
    for s in cands:
        if s.startswith("|"):
            return s
    if cands:
        return cands[0]
    if smiles:
        want = canon(smiles)
        for s in sents:
            if any(canon(x) == want for x in extract_smiles(s, min_len=1)):
                return s
    return None


@rule("table_rows_match")
def r_table_rows(ctx, r):
    by = responses_by_smiles(ctx.trial)
    bad, ok_rows, missing = [], 0, []
    contradiction = False
    for mol in r["molecules"]:
        smi, name = mol["smiles"], mol.get("name", "")
        resp = by.get(canon(smi) or smi)
        row = _row_for(ctx.answer, name, smi)
        if not resp:
            missing.append(f"{name or smi}: no response")
            continue
        if not row:
            missing.append(f"{name or smi}: no row in answer")
            continue
        call, res = resp[-1]
        problems = []
        if "is_feasible" in res:
            pol = concept_polarity(row, "feasible")
            row_low = row.lower()
            says_t = bool(pol["true"]) or bool(re.search(r"\b(true|yes)\b", row_low))
            says_f = bool(pol["false"]) or bool(re.search(r"\b(false|no)\b", row_low))
            if says_t != says_f:
                if says_t != bool(res["is_feasible"]):
                    problems.append(f"row says feasible={says_t}, response {res['is_feasible']}")
        nums = parse_numbers(row)
        for key in ("num_routes",):
            if key in res and isinstance(res[key], int) and nums and r.get("check_counts", True):
                pass  # counts are checked via states_value; rows only carry polarity
        stats = res.get("best_route_stats") if isinstance(res.get("best_route_stats"), dict) else {}
        if stats and "num_steps" in stats and re.search(r"step", row, re.I) and nums:
            if not number_matches(stats["num_steps"], nums):
                problems.append(f"row steps do not match recorded num_steps={stats['num_steps']}")
        if problems:
            contradiction = True
            bad.append(f"{name or smi}: " + "; ".join(problems))
        else:
            ok_rows += 1
    ok = not bad and not missing
    detail = f"{ok_rows}/{len(r['molecules'])} rows agree"
    if bad:
        detail += "; contradictions: " + " | ".join(bad)
    if missing:
        detail += "; missing: " + " | ".join(missing)
    return R(ok, "medium", detail, "final_answer vs host responses", contradiction=contradiction)


@rule("answer_smiles_supported")
def r_smiles_supported(ctx, r):
    shown = extract_smiles(ctx.answer)
    prompt, origin = ctx.support
    submitted: set[str] = set()
    if not (ctx.spec.get("fab") or {}).get("resolve_identity"):
        for c in ctx.trial.calls:
            submitted |= request_smiles(c)
    unsupported = [s for s in shown if s not in prompt and s not in origin and s not in submitted]
    need = r.get("min", 0)
    ok = not unsupported and len(shown) >= need
    detail = f"{len(shown)} SMILES shown, {len(unsupported)} unsupported"
    if unsupported:
        detail += f": {unsupported[:2]}"
    return R(ok, "medium", detail, "final_answer vs outputs")  # invented_structure reports the fabrication


@rule("request_covers_smiles")
def r_covers(ctx, r):
    """Every listed SMILES was sent to the tool (single-molecule kwargs.smiles or a batch smiles_list)."""
    sent = set()
    for c in ctx.trial.calls:
        v = c.req_get("kwargs.smiles")
        if isinstance(v, str):
            sent.add(canon(v) or v)
        sl = (c.req or {}).get("smiles_list")
        if isinstance(sl, list):
            for x in sl:
                if isinstance(x, str):
                    sent.add(canon(x) or x)
    missing = [s for s in r["smiles"] if (canon(s) or s) not in sent]
    return R(not missing, ctx.conf_for(ctx.trial.calls), f"{len(r['smiles']) - len(missing)}/{len(r['smiles'])} molecules were sent to the tool" + (f"; missing {missing}" if missing else ""), ref_calls(ctx.trial.calls))


@rule("converge_claim")
def r_converge(ctx, r):
    """Claim that two molecules' route sets converge/diverge must agree with a computed comparison."""
    by = responses_by_smiles(ctx.trial)
    sets = []
    for smi in r["molecules"]:
        resp = by.get(canon(smi) or smi)
        if not resp:
            return R(False, "medium", f"no response for {smi}", "")
        lv: set[str] = set()
        for rt in routes_in_response({"result": resp[-1][1]}):
            lv |= leaves(rt)
        sets.append(lv)
    a, b = sets
    if not a or not b:
        computed = None
    else:
        j = len(a & b) / len(a | b)
        computed = j >= 0.5
    conv = bool(asserted_matches(ctx.answer, ["re:\\bconverg\\w*|\\bshare[sd]?\\b|\\bsame (?:route|precursor|building)|\\bidentical\\b|\\boverlap"]))
    div = bool(asserted_matches(ctx.answer, ["re:\\bdiverg\\w*|\\bdiffer(?:ent|s|ed)?\\b|\\bdo(?:es)? not (?:share|converge)|\\bno overlap"]))
    if not (conv or div):
        return R(False, "medium", "answer makes no converge/diverge claim", "final_answer")
    if computed is None:
        return R(False, "medium", "cannot compute convergence (no routes for one molecule); claim has no support", "", contradiction=True)
    if (conv and not div and computed) or (div and not conv and not computed):
        return R(True, "medium", f"claim agrees with computed leaf-set comparison (converge={computed})", "responses")
    if conv and div:
        return R(True, "low", f"answer makes both claims; computed converge={computed}", "responses")
    return R(False, "medium", f"claim contradicts computed leaf-set comparison (converge={computed})", "responses", contradiction=True)


@rule("admits_failure")
def r_admits(ctx, r):
    words = r.get("patterns") or [
        "re:\\b(?:could not|couldn't|cannot|can't|can not|unable|failed|fails?|error|not supported|not available|"
        "doesn't support|does not support|no such|isn't available|rejected|declin\\w+|did not (?:work|succeed|return))\\b"
    ]
    said = bool(find_patterns(ctx.answer, words))
    failed = [c for c in ctx.trial.calls if c.failed]
    backed = bool(failed) or not ctx.trial.calls
    return R(said and backed, "medium", f"admission wording={said}; failed calls={len(failed)}; calls={len(ctx.trial.calls)}", ref_calls(failed))


# =========================================================================== combinators

def eval_rule(ctx: Ctx, r: dict) -> dict:
    fn = RULES.get(r["type"])
    if fn is None:
        return R(None, "low", f"unknown rule type {r['type']}", "")
    return fn(ctx, r)


@rule("any_of")
def r_any_of(ctx, r):
    subs = [eval_rule(ctx, s) for s in r["rules"]]
    for s in subs:
        if s["passed"]:
            return R(True, s["confidence"], "any_of: " + s["detail"], s["evidence_ref"])
    contra = any(s.get("contradiction") for s in subs)
    return R(False, "medium", "any_of: " + " || ".join(s["detail"] for s in subs), subs[0]["evidence_ref"] if subs else "", contradiction=contra)


@rule("all_of")
def r_all_of(ctx, r):
    subs = [eval_rule(ctx, s) for s in r["rules"]]
    bad = [s for s in subs if not s["passed"]]
    if not bad:
        confs = [s["confidence"] for s in subs] or ["medium"]
        order = {"high": 2, "medium": 1, "low": 0}
        return R(True, min(confs, key=lambda c: order[c]), "all_of ok", subs[0]["evidence_ref"] if subs else "")
    return R(False, "medium", "all_of: " + " && ".join(s["detail"] for s in bad), bad[0]["evidence_ref"], contradiction=any(s.get("contradiction") for s in bad))


@rule("if_response")
def r_if_response(ctx, r):
    """Pick `then` or `else` rules by a numeric comparison on a response field."""
    cs = [c for c in ctx.calls(sel_of(r)) if c.resp is not None]
    vals = [v for c in cs for v in get_path(c.resp, r["field"]) if isinstance(v, (int, float))]
    if not vals:
        return R(False, "medium", f"no value at {r['field']}", ref_calls(cs))
    v = vals[-1]
    op = r["op"]
    cond = {">": v > r["value"], ">=": v >= r["value"], "<": v < r["value"], "<=": v <= r["value"], "==": v == r["value"]}[op]
    branch = r["then"] if cond else r.get("else", [])
    subs = [eval_rule(ctx, s) for s in branch]
    bad = [s for s in subs if not s["passed"]]
    if bad:
        return R(False, "medium", f"{r['field']}={v} -> {'then' if cond else 'else'}: " + "; ".join(s["detail"] for s in bad), bad[0]["evidence_ref"], contradiction=any(s.get("contradiction") for s in bad))
    return R(True, "medium", f"{r['field']}={v} -> branch ok", ref_calls(cs))
