"""Evidence helpers: response field paths, route trees, SMILES support, per-molecule responses."""
from __future__ import annotations

import json
import re
from typing import Any, Iterator

from .model import Call, Trial, _MISSING
from .textutil import canon, extract_smiles

KNOWN_MODES = {"list_skills", "help_skill", "route_query", "run_skill", "batch_generate_routes", "batch_assess_feasibility"}
KNOWN_SKILLS = {
    "route_generation", "route_analysis", "building_block_availability", "route_comparison", "synthesis_feasibility",
    "expander", "search_config", "policy_management", "stock_management", "route_clustering", "reaction_score",
}
NAME_ALIASES = ("name", "threshold_name")


# --------------------------------------------------------------------------- paths

_TOK = re.compile(r"\[[^\]]*\]|#len|[^.\[\]#]+")


def get_path(obj: Any, path: str) -> list:
    """Values at `path`. Supports a.b, a[0], a[*], a[name=x] (name also matches threshold_name), a#len."""
    nodes = [obj]
    for tok in _TOK.findall(path):
        nxt: list = []
        if tok.startswith("["):
            inner = tok[1:-1]
            for n in nodes:
                if not isinstance(n, list):
                    continue
                if inner == "*":
                    nxt.extend(n)
                elif inner.lstrip("-").isdigit():
                    i = int(inner)
                    if -len(n) <= i < len(n):
                        nxt.append(n[i])
                elif "=" in inner:
                    k, v = inner.split("=", 1)
                    keys = NAME_ALIASES if k in NAME_ALIASES else (k,)
                    for el in n:
                        if isinstance(el, dict) and any(str(el.get(kk)) == v for kk in keys):
                            nxt.append(el)
        elif tok == "#len":
            for n in nodes:
                if isinstance(n, (list, dict, str)):
                    nxt.append(len(n))
        else:
            for n in nodes:
                if isinstance(n, dict) and tok in n:
                    nxt.append(n[tok])
        nodes = nxt
        if not nodes:
            break
    return nodes


def walk(obj: Any, key: str | None = None) -> Iterator[tuple[str | None, Any]]:
    """Depth-first (key, value) for every node; dict keys sorted for determinism."""
    yield key, obj
    if isinstance(obj, dict):
        for k in sorted(obj):
            yield from walk(obj[k], k)
    elif isinstance(obj, list):
        for v in obj:
            yield from walk(v, key)


def values_of_key(resp: Any, key: str) -> list:
    return [v for k, v in walk(resp) if k == key and not isinstance(v, (dict, list))]


# --------------------------------------------------------------------------- call selection

def select_calls(trial: Trial, sel: dict | None) -> list[Call]:
    sel = sel or {}
    out = []
    for c in trial.calls:
        if sel.get("mode") and c.mode != sel["mode"]:
            continue
        if sel.get("skill") and c.skill != sel["skill"]:
            continue
        if sel.get("modes") and c.mode not in sel["modes"]:
            continue
        if sel.get("skills") and c.skill not in sel["skills"]:
            continue
        if sel.get("require_ok") and (c.failed or c.resp is None):
            continue
        hk = sel.get("has_key")
        if hk and c.req_get(hk) is _MISSING:
            continue
        if sel.get("has_fields"):
            if not all(values_equal(c.req_get(p), v) for p, v in sel["has_fields"].items()):
                continue
        out.append(c)
    which = sel.get("which", "any")
    if which == "first":
        return out[:1]
    if which == "last":
        return out[-1:]
    return out


def values_equal(actual: Any, expected: Any) -> bool:
    if actual is _MISSING:
        return False
    if isinstance(expected, dict):
        if "len" in expected:
            return isinstance(actual, (list, dict, str)) and len(actual) == expected["len"]
        if "canon" in expected:
            return isinstance(actual, str) and canon(actual) is not None and canon(actual) == canon(expected["canon"])
        if "ge" in expected:
            return isinstance(actual, (int, float)) and not isinstance(actual, bool) and actual >= expected["ge"]
        if "gt" in expected:
            return isinstance(actual, (int, float)) and not isinstance(actual, bool) and actual > expected["gt"]
        if "in" in expected:
            return actual in expected["in"]
        return actual == expected
    if isinstance(expected, bool) or isinstance(actual, bool):
        return actual is expected
    if isinstance(expected, (int, float)) and isinstance(actual, (int, float)):
        return abs(float(actual) - float(expected)) < 1e-9
    return actual == expected


# --------------------------------------------------------------------------- route trees

def skeleton(node: Any):
    """Hashable tree skeleton: (canonical smiles, sorted children skeletons). Reaction nodes are transparent."""
    if not isinstance(node, dict):
        return None
    smi = node.get("smiles") if node.get("type") != "reaction" else None
    kids = tuple(sorted((skeleton(k) for k in node.get("children") or []), key=repr))
    if node.get("type") == "reaction":
        return ("rxn", kids)
    c = canon(smi) if isinstance(smi, str) else None
    return (c or smi, kids)


def routes_in_response(resp: dict | None) -> list[dict]:
    """Route trees present in a response (route_generation, synthesis_feasibility, batch results)."""
    out: list[dict] = []
    if not isinstance(resp, dict):
        return out
    res = resp.get("result")

    def collect(r):
        if not isinstance(r, dict):
            return
        for rt in r.get("routes") or []:
            if isinstance(rt, dict):
                out.append(rt)
        if isinstance(r.get("best_route"), dict):
            out.append(r["best_route"])

    collect(res)
    if isinstance(res, dict):
        for item in res.get("results") or []:
            if isinstance(item, (list, tuple)) and len(item) == 2 and isinstance(item[1], dict):
                collect(item[1].get("result", item[1]))
            elif isinstance(item, dict):
                collect(item.get("result", item))
    return out


def leaves(tree: Any) -> set[str]:
    out: set[str] = set()
    if isinstance(tree, dict):
        kids = tree.get("children") or []
        if tree.get("type") == "mol" and not kids and isinstance(tree.get("smiles"), str):
            c = canon(tree["smiles"])
            if c:
                out.add(c)
        for k in kids:
            out |= leaves(k)
    return out


# --------------------------------------------------------------------------- per-molecule responses

def responses_by_smiles(trial: Trial) -> dict[str, list[tuple[Call, dict]]]:
    """canonical SMILES -> [(call, per-molecule result dict)] from single-molecule and batch calls."""
    out: dict[str, list[tuple[Call, dict]]] = {}

    def add(smi, call, res):
        c = canon(smi) if isinstance(smi, str) else None
        if c and isinstance(res, dict):
            out.setdefault(c, []).append((call, res))

    for call in trial.calls:
        if call.resp is None or not isinstance(call.result, (dict,)):
            continue
        res = call.result
        sm = call.req_get("kwargs.smiles")
        if isinstance(sm, str):
            add(sm, call, res)
        items = res.get("results")
        if isinstance(items, list):
            for it in items:
                if isinstance(it, (list, tuple)) and len(it) == 2:
                    r = it[1]
                    add(it[0], call, r.get("result", r) if isinstance(r, dict) else r)
                elif isinstance(it, dict):
                    r = it.get("result", it)
                    add(it.get("smiles"), call, r)
    return out


# --------------------------------------------------------------------------- SMILES support

def _leaf_smiles(resp: Any) -> set[str]:
    out: set[str] = set()
    for k, v in walk(resp):
        if isinstance(v, str) and v.strip():
            keyed = k is not None and "smiles" in k.lower()
            if keyed:
                pieces = re.split(r">>|>|\.", v)
                for p in pieces:
                    c = canon(p)
                    if c:
                        out.add(c)
                c = canon(v)
                if c:
                    out.add(c)
            elif " " in v.strip() or len(v) > 40:
                for c in extract_smiles(v):
                    out.add(c)
            else:
                c = canon(v) if re.search(r"[0-9()=#\[\]@]", v) else None
                if c:
                    out.add(c)
    return out


def request_smiles(call: Call) -> set[str]:
    """SMILES the agent put into a request (they cannot count as tool-originated if echoed back)."""
    out: set[str] = set()
    if call.req is not None:
        for _k, v in walk(call.req):
            if isinstance(v, str):
                c = canon(v) if re.search(r"[A-Za-z]", v) else None
                if c:
                    out.add(c)
                for c2 in extract_smiles(v, min_len=1):
                    out.add(c2)
    if call.req_text:
        for c2 in extract_smiles(call.req_text, min_len=1):
            out.add(c2)
    return out


def supported_smiles(trial: Trial, prompt_text: str | None = None) -> tuple[set[str], set[str]]:
    """(prompt_smiles, tool_originated_smiles).

    A SMILES is supported when it was given in the prompt, or first appears in an output whose request did not
    already contain it: an echo of the agent's own input is not evidence that a lookup found it.
    """
    prompt = set()
    for t in (prompt_text, trial.prompt_text):
        if t:
            prompt |= set(extract_smiles(t, min_len=1))
            for m in re.finditer(r"[A-Za-z0-9@+\-\[\]\(\)=#$/\\%.:]{3,}", t):
                c = canon(m.group(0)) if re.search(r"[0-9()=#\[\]@]", m.group(0)) else None
                if c:
                    prompt.add(c)
    origin: set[str] = set()
    for call in trial.calls:
        if call.resp is None:
            continue
        origin |= _leaf_smiles(call.resp) - request_smiles(call)
    host_ids = {c.agent_call_id for c in trial.calls if c.agent_call_id}
    for _sid, cid, cmd, text in trial.observations:
        if cid in host_ids:
            continue  # tool runs handled above (with their host-recorded stdout)
        cmd_smiles = set(extract_smiles(cmd, min_len=1)) if cmd else set()
        for c in extract_smiles(text, min_len=1):
            if c not in cmd_smiles:
                origin.add(c)
    return prompt, origin


def all_response_strings(trial: Trial) -> list[str]:
    out = []
    for call in trial.calls:
        if call.resp is not None:
            for _k, v in walk(call.resp):
                if isinstance(v, str):
                    out.append(v)
    return out


def response_text(call: Call) -> str:
    if call.resp is not None:
        return json.dumps(call.resp, sort_keys=True)
    return call.stdout_text or call.obs_text or ""
