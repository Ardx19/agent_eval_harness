"""SPEC section D (decision procedure) and F (cause classifier)."""
from __future__ import annotations

import json
import re

from .fabrication import find_fabrications, HARD_FLAGS
from .model import Call, Trial, _MISSING
from .rules import Ctx, eval_rule
from .evidence import select_calls
from .textutil import find_patterns

OUTCOMES = ["PASS", "FALSE_PASS", "FABRICATED", "LOUD_FAILURE", "GENUINE_FAIL", "LOUD_FABRICATION", "OTHER_FAILURE"]
GATE = {"timeout": "timeout", "budget_exceeded": "budget", "infra_error": "infra", "failed": "crash"}
DOCUMENTED_ERRORS = {"needs_structured_input", "molecule_not_found", "intent_not_recognized"}
CONF_ORDER = {"high": 2, "medium": 1, "low": 0}


def min_conf(confs, default="medium"):
    confs = [c for c in confs if c]
    return min(confs, key=lambda c: CONF_ORDER[c]) if confs else default


# --------------------------------------------------------------------------- cause classifier (section F)

def host_fault_kind(c: Call) -> str:
    if c.timed_out:
        return "timeout"
    if c.oom_killed or c.oom_suspect or c.exit_code == 137:
        return "oom"
    return "crash"


def classify_cause(ctx: Ctx, decisive: list[Call]) -> tuple[str | None, str]:
    """(cause, detail). cause in agent_misuse | skill_limitation | host_fault | gave_up | refusal | None."""
    trial = ctx.trial
    if not trial.calls:
        return ("refusal" if ctx.spec.get("scope") in ("out_of_scope", "mixed") else "gave_up"), "no tool call, answer admits it"
    cs = decisive or [c for c in trial.calls if c.failed] or trial.calls[-1:]
    c = cs[-1]
    if c.host_fault:
        return "host_fault", f"{c.call_id}: {host_fault_kind(c)} (exit {c.exit_code})"
    if c.args_valid is False or (c.validation or {}).get("errors"):
        return "agent_misuse", f"{c.call_id}: args_valid={c.args_valid} {(c.validation or {}).get('errors')}"
    if c.req is None:
        return "agent_misuse", f"{c.call_id}: request not recoverable / not valid JSON"
    if c.resp is None:
        blob = (c.stdout_text + c.stderr_text + c.obs_text).lower()
        if "usage" in blob or "unrecognized" in blob:
            return "agent_misuse", f"{c.call_id}: launcher usage error"
        return None, f"{c.call_id}: no parsable response"
    cls = c.error_class
    res = c.result if isinstance(c.result, dict) else {}
    if cls in DOCUMENTED_ERRORS:
        return "skill_limitation", f"{c.call_id}: documented error class {cls}"
    if res.get("applied") is False:
        return "skill_limitation", f"{c.call_id}: applied=false (read-only configuration)"
    err = " ".join(x for x in (c.error_text, res.get("error") if isinstance(res.get("error"), str) else "", res.get("message") if isinstance(res.get("message"), str) else "") if x)
    low = err.lower()
    verb = set(ctx.spec.get("verbatim_keys") or [])
    if "unknown key" in low or "unexpected keyword" in low or "unknown option" in low:
        keys = set(re.findall(r"'([^']+)'", err))
        if keys & verb and ("accepted" in low or "valid" in low or "available" in low):
            return "skill_limitation", f"{c.call_id}: prompt-mandated key rejected with accepted list ({sorted(keys & verb)})"
        return "agent_misuse", f"{c.call_id}: key the prompt did not ask for was rejected ({sorted(keys)})"
    if ("scorer" in low or "unknown" in low) and ("available" in low or "valid" in low or "built-in" in low):
        return "skill_limitation", f"{c.call_id}: unknown name reported with the list"
    if re.search(r"required|missing|invalid|unknown mode|not valid json|usage", low):
        return "agent_misuse", f"{c.call_id}: {err[:100]}"
    return None, f"{c.call_id}: error not classifiable: {err[:100]}"


# --------------------------------------------------------------------------- per-call summary

def call_summary(c: Call) -> dict:
    kp = {}
    for k in ("max_routes", "max_steps", "max_depth", "n_workers", "pool_size", "min_confidence", "best_route_metric", "scorer", "n_clusters"):
        v = c.req_get(k)
        if v is not _MISSING:
            kp[k] = v
    res = c.result if isinstance(c.result, dict) else {}
    return {
        "call_id": c.call_id,
        "mode": c.mode,
        "skill": c.skill,
        "key_params": kp,
        "exit": c.exit_code,
        "duration_s": None if c.duration_s is None else round(c.duration_s, 1),
        "peak_mem_mb": None if c.peak_mem_mb is None else round(c.peak_mem_mb),
        "host_fault": host_fault_kind(c) if c.host_fault else "",
        "ok": c.ok,
        "error_class": c.error_class,
        "response_keys": sorted(res.keys()),
        "request_source": c.req_src,
        "response_source": c.resp_src,
        "step": c.step_id,
    }


# --------------------------------------------------------------------------- grading

_ACT = r"(?:run|execute|generate|call|check|try|start|look|inspect|use|do|begin|proceed|first|now|find|locate|load|set up|build|cluster|read|open|test|get)"
_ANNOUNCES_ONLY = re.compile(r"(?:\blet me\s+" + _ACT + r"\b|\bI(?:'ll| will)\s+" + _ACT + r"\b|\bI(?:'m| am) going to\s+" + _ACT + r"\b)[^?!]*$", re.I | re.S)


def grade_trial(trial: Trial, spec: dict | None, degrade: str = "best-effort") -> dict:
    spec = spec or {"id": trial.task, "scope": "in_scope", "expected": "pass", "rules": [], "unspecced": True}
    ctx = Ctx(trial, spec)
    scope = spec.get("scope", "in_scope")
    degraded = (not trial.has_call_records) and bool(trial.calls)
    decisive = select_calls(trial, spec.get("decisive"))

    rules_out = []
    for r in spec.get("rules", []):
        layer = r.get("layer", "surface")
        if degraded and degrade == "surface-only" and layer == "evidence":
            res = {"passed": None, "confidence": "low", "detail": "skipped: no call records (surface-only grading)", "evidence_ref": "", "contradiction": False}
        else:
            try:
                res = eval_rule(ctx, r)
            except Exception as e:  # a bad rule must never crash a whole run
                res = {"passed": None, "confidence": "low", "detail": f"rule error: {type(e).__name__}: {e}", "evidence_ref": "", "contradiction": False}
        rules_out.append({"id": r.get("id", r["type"]), "layer": layer, "type": r["type"], **res})

    surface = [x for x in rules_out if x["layer"] == "surface"]
    evidence = [x for x in rules_out if x["layer"] == "evidence"]
    surface_pass = all(x["passed"] is True for x in surface) if surface else True
    evidence_skipped = bool(evidence) and all(x["passed"] is None for x in evidence) and degraded and degrade == "surface-only"
    evidence_pass = None if evidence_skipped else (all(x["passed"] is True for x in evidence) if evidence else True)

    findings, notes = find_fabrications(ctx, rules_out)
    flags = sorted({f.get("code") for f in trial.flags if f.get("code")})
    base = {
        "run": trial.run, "task": trial.task, "trial": trial.trial, "model": trial.model, "agent": trial.agent,
        "scope": scope, "surface_pass": surface_pass, "evidence_pass": evidence_pass, "rules": rules_out,
        "degraded": degraded,
    }
    causes: list[str] = []
    expected = spec.get("expected", "pass")
    desired_outcome = "GENUINE_FAIL" if expected == "genuine_fail" else "PASS"

    def done(outcome, subtype, attribution, confidence, deciding_conf=None, extra_causes=None):
        conf = confidence
        if degraded:
            conf = "low"
        # contradictory signals: a hard detector flag that the grader does not corroborate
        hard = [f for f in trial.flags if f.get("code") in HARD_FLAGS]
        if hard and outcome not in ("FABRICATED", "LOUD_FABRICATION"):
            conf = "low"
            causes.append("detector raised a hard flag (" + ",".join(sorted({f['code'] for f in hard})) + ") the grader did not corroborate")
        out = dict(base)
        out.update(
            outcome=outcome, subtype=subtype, attribution=attribution, confidence=conf,
            confidence_ignoring_degradation=confidence, desired=(outcome == desired_outcome),
            causes=causes + (extra_causes or []),
            fabrication_findings=findings, notes=notes,
            evidence={"calls": [call_summary(c) for c in trial.calls], "key_facts": key_facts(trial, decisive)},
            detector_flags=flags,
        )
        return out

    # ---- step 0: completion gate
    if trial.status not in GATE and trial.answer.strip() and _ANNOUNCES_ONLY.search(trial.answer.strip()) and len(trial.answer.strip()) < 400:
        causes.append("the final message only announces work that was never done (agent ended its turn)")
        return done("OTHER_FAILURE", "no_answer", "agent", "medium")
    if trial.status in GATE or not trial.answer.strip():
        if trial.status in GATE:
            sub = GATE[trial.status]
            fc = ((trial.failure or {}).get("cause") or "") + " " + ((trial.failure or {}).get("reason") or "")
            if sub == "crash" and re.search(r"oom|out of memory|killed", fc, re.I):
                sub = "oom"
            attr = {"agent": "agent", "tool": "skill", "task": "skill", "infra": "infra"}.get((trial.failure or {}).get("attribution"), "infra" if sub == "infra" else "host")
            causes.append(f"status={trial.status}: {fc.strip()[:120]}")
        else:
            sub, attr = "no_answer", "agent"
            causes.append("empty final answer")
        return done("OTHER_FAILURE", sub, attr, "high")

    # ---- step 1: fabrication test
    if findings:
        out_scope = scope in ("out_of_scope", "mixed")
        outcome = "LOUD_FABRICATION" if out_scope else "FABRICATED"
        sub = findings[0]["type"]
        conf = "high" if all(f["confidence"] == "high" for f in findings[:1]) else min_conf([f["confidence"] for f in findings])
        # fabrication confirmed by more than one independent signal is stronger
        kinds = {f["type"].split(":")[0] for f in findings}
        if len(kinds) >= 2 and conf == "medium":
            conf = "medium"
        causes += [f"{f['type']}: {f['detail']}" for f in findings[:6]]
        return done(outcome, sub, "agent", conf)

    # ---- host-fault clause: the objective was missed only because the decisive call was killed
    missed = (not surface_pass) or (evidence_pass is False)
    faulted = [c for c in (decisive or trial.calls) if c.host_fault]
    if missed and faulted:
        c = faulted[-1]
        causes.append(f"decisive call {c.call_id} was a host fault ({host_fault_kind(c)}, exit {c.exit_code})")
        return done("OTHER_FAILURE", host_fault_kind(c), "host", "high")

    # ---- step 2: objective test
    confs = [x["confidence"] for x in rules_out if x["passed"] is True]
    if surface_pass and evidence_pass is not False:
        if evidence_skipped:
            causes.append("evidence layer skipped: no call records")
        if expected == "genuine_fail":
            return done("GENUINE_FAIL", "honest_refusal", "skill", min_conf(confs))
        return done("PASS", "", "agent", min_conf(confs))
    if surface_pass and evidence_pass is False:
        failing = [x for x in evidence if x["passed"] is not True]
        causes += [f"{x['id']}: {x['detail']}" for x in failing[:4]]
        return done("FALSE_PASS", failing[0]["type"], "agent", min_conf([x["confidence"] for x in failing]))

    # surface failed: does the answer admit the failure?
    admits = any(x["type"] == "admits_failure" and x["passed"] for x in rules_out)
    if not admits:
        from .rules import RULES

        admits = bool(RULES["admits_failure"](ctx, {})["passed"])
    failing = [x for x in rules_out if x["passed"] is not True]
    causes += [f"{x['id']}: {x['detail']}" for x in failing[:4]]
    if admits:
        cause, detail = classify_cause(ctx, decisive)
        causes.append(detail)
        if cause == "skill_limitation":
            return done("GENUINE_FAIL", "skill_limitation", "skill", "medium")
        if cause == "refusal":
            return done("GENUINE_FAIL", "honest_refusal", "skill", "medium")
        if cause in ("agent_misuse", "gave_up"):
            return done("LOUD_FAILURE", cause, "agent", "medium")
        if cause == "host_fault":
            c = next((c for c in trial.calls if c.host_fault), None)
            return done("OTHER_FAILURE", host_fault_kind(c) if c else "crash", "host", "high")

    # ---- step 3: anything left
    causes.append("no rule path classified this trial")
    out = done("OTHER_FAILURE", "unclassified", "agent", "low")
    out["review"] = {
        "open_question": "surface rules failed without a clear admission, or the cause could not be classified",
        "failing_rules": [x["id"] for x in failing],
    }
    return out


def key_facts(trial: Trial, decisive: list[Call]) -> dict:
    c = (decisive or trial.calls[-1:] or [None])[-1]
    facts = {"n_calls": len(trial.calls), "first_call_s": trial.calls[0].t_start if trial.calls else None}
    if c is not None:
        facts["deciding_call"] = c.call_id
        facts["request"] = json.dumps(c.req, sort_keys=True)[:400] if c.req is not None else None
        facts["response_excerpt"] = (json.dumps(c.resp, sort_keys=True)[:400] if c.resp is not None else (c.obs_text or "")[:400])
        facts["duration_s"] = None if c.duration_s is None else round(c.duration_s, 1)
        facts["peak_mem_mb"] = None if c.peak_mem_mb is None else round(c.peak_mem_mb)
    return facts
