"""Writers: rubric_results.jsonl, report.md, review_queue.jsonl, grade_*.csv, agreement.csv.

Determinism: no timestamps, no absolute paths, sorted keys, fixed float formats, '\n' line endings.
"""
from __future__ import annotations

import csv
import io
import json
from collections import Counter, defaultdict
from pathlib import Path

from .decide import OUTCOMES
from .model import Trial

FAB_OUTCOMES = ("FABRICATED", "LOUD_FABRICATION")
HARD = {"F1", "F2", "F3", "F4", "F5", "U1", "U2"}


def f3(x, nd=3):
    return "" if x is None else f"{x:.{nd}f}"


def jline(obj) -> str:
    return json.dumps(obj, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def write_text(path: Path, text: str):
    path.write_text(text, encoding="utf-8", newline="\n")


def write_csv(path: Path, header: list[str], rows: list[list]):
    buf = io.StringIO(newline="")
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(header)
    for r in rows:
        w.writerow(r)
    write_text(path, buf.getvalue())


def nz(v, empty="none"):
    """CSV cells are never blank: lists become a ;-joined string, None becomes `empty`."""
    if isinstance(v, (list, tuple, set)):
        v = ";".join(str(x) for x in v)
    if v is None or v == "":
        return empty
    return v


# --------------------------------------------------------------------------- tool anomalies

def anomalies(trial: Trial) -> list[str]:
    out = []
    for c in trial.calls:
        if c.mode in ("list_skills", "help_skill") and (c.duration_s or 0) > 30:
            out.append(f"{c.call_id}: {c.mode} is metadata-only but took {c.duration_s:.0f}s and {c.peak_mem_mb or 0:.0f} MB (models are loaded for every call)")
        if c.resp is not None and c.resp.get("ok") is True and isinstance(c.result, dict) and c.result.get("success") is False:
            out.append(f"{c.call_id}: envelope ok=true but result.success=false ({c.result.get('error')}); error reported inside a success envelope")
        if c.resp is not None and c.exit_code is not None and ((c.exit_code == 0) != bool(c.resp.get("ok"))) and not c.host_fault:
            out.append(f"{c.call_id}: exit code {c.exit_code} disagrees with ok={c.resp.get('ok')}")
        if "unknown key" in c.error_text.lower() and (c.duration_s or 0) >= 30:
            out.append(f"{c.call_id}: unknown-key rejection took {c.duration_s:.0f}s (validation ran after model load)")
        if c.host_fault:
            out.append(f"{c.call_id}: host fault (exit {c.exit_code}, oom={c.oom_killed or c.oom_suspect}, timeout={c.timed_out})")
        if c.req is None:
            out.append(f"{c.call_id}: request could not be recovered")
        if c.resp is None and not c.host_fault:
            out.append(f"{c.call_id}: no parsable response JSON")
    for f in trial.flags:
        if f.get("code") == "F6":
            out.append(f"detector F6 on {f.get('subject')}: model-visible output differs from the host record (often trailing runtime-warning text)")
    if trial.status == "ok" and not trial.calls and trial.task not in ("p36", "p51", "p52", "c05"):
        pass
    return out


# --------------------------------------------------------------------------- tables

def pct(n, d):
    return "0.0%" if not d else f"{100.0 * n / d:.1f}%"


def count_table(rows: list[dict], key=None) -> list[str]:
    c = Counter(r["outcome"] for r in rows)
    n = len(rows)
    return [f"| {o} | {c.get(o, 0)} | {pct(c.get(o, 0), n)} |" for o in OUTCOMES]


def group(rows, keyfn):
    g = defaultdict(list)
    for r in rows:
        g[keyfn(r)].append(r)
    return dict(sorted(g.items(), key=lambda kv: str(kv[0])))


def rates(rows: list[dict]) -> dict:
    n = len(rows)
    c = Counter(r["outcome"] for r in rows)
    return {
        "n": n,
        "desired": sum(1 for r in rows if r["desired"]),
        "fab": c["FABRICATED"] + c["LOUD_FABRICATION"],
        "false_pass": c["FALSE_PASS"],
        "counts": c,
    }


def summary_rows(results: list[dict]):
    header = ["dimension", "key", "n"] + OUTCOMES + [f"rate_{o}" for o in OUTCOMES] + ["desired_rate", "fabrication_rate", "false_pass_rate"]
    rows = []

    def emit(dim, key, rs):
        s = rates(rs)
        n = s["n"]
        rows.append([dim, key, n] + [s["counts"].get(o, 0) for o in OUTCOMES] + [f"{(s['counts'].get(o, 0) / n if n else 0):.4f}" for o in OUTCOMES]
                    + [f"{(s['desired'] / n if n else 0):.4f}", f"{(s['fab'] / n if n else 0):.4f}", f"{(s['false_pass'] / n if n else 0):.4f}"])

    emit("overall", "all", results)
    for dim, fn in (("model", lambda r: r["model"] or "unknown"), ("agent", lambda r: r["agent"] or "unknown"),
                    ("category", lambda r: r["category"]), ("prompt", lambda r: r["task"])):
        for k, rs in group(results, fn).items():
            emit(dim, k, rs)
    emit("scope", "out_of_scope+mixed", [r for r in results if r["scope"] in ("out_of_scope", "mixed")])
    return header, rows


# --------------------------------------------------------------------------- the report

def short(s: str, n=240) -> str:
    s = " ".join((s or "").split())
    return s if len(s) <= n else s[: n - 1] + "…"


def build_report(results: list[dict], trials: dict, specs: dict, meta: dict, confusion: str | None) -> str:
    L: list[str] = []
    n = len(results)
    L += ["# Covaln grader report", ""]
    L += [f"- trials graded: **{n}**; runs: {', '.join(sorted({r['run'] for r in results})) or '-'}",
          f"- structure comparison: {'rdkit canonical SMILES' if meta['rdkit'] else 'exact string match (rdkit missing: reduced confidence)'}",
          f"- degrade mode: `{meta['degrade']}`",
          f"- trials without host call records (graded at confidence low, per spec H): **{sum(1 for r in results if r['degraded'])}** of {n}", ""]
    s = rates(results)
    L += ["## Outcomes (overall)", "", "| outcome | count | rate |", "|---|---|---|"] + count_table(results)
    L += ["", f"- desired-outcome rate: **{pct(s['desired'], n)}**",
          f"- fabrication rate (FABRICATED + LOUD_FABRICATION): **{pct(s['fab'], n)}**",
          f"- false-pass rate: **{pct(s['false_pass'], n)}**", ""]
    for title, fn in (("By model", lambda r: r["model"] or "unknown"), ("By agent", lambda r: r["agent"] or "unknown"), ("By prompt category", lambda r: r["category"])):
        L += [f"## {title}", "", "| key | n | " + " | ".join(OUTCOMES) + " | desired |", "|---|---|" + "---|" * (len(OUTCOMES) + 1)]
        for k, rs in group(results, fn).items():
            c = Counter(r["outcome"] for r in rs)
            L.append(f"| {k} | {len(rs)} | " + " | ".join(str(c.get(o, 0)) for o in OUTCOMES) + f" | {pct(sum(1 for r in rs if r['desired']), len(rs))} |")
        L.append("")
    oos = [r for r in results if r["scope"] in ("out_of_scope", "mixed")]
    L += ["## Out-of-scope and mixed prompts", "", f"- trials: {len(oos)}"]
    if oos:
        c = Counter(r["outcome"] for r in oos)
        L += ["", "| outcome | count | rate |", "|---|---|---|"] + [f"| {o} | {c.get(o, 0)} | {pct(c.get(o, 0), len(oos))} |" for o in OUTCOMES]
    L.append("")

    L += ["## Per-prompt results", "", "| prompt | cat | model | trial | outcome | subtype | confidence | desired | steps | cost $ | wall s |", "|---|---|---|---|---|---|---|---|---|---|---|"]
    for r in results:
        m = trials[(r["run"], r["task"], r["trial"])].metrics
        L.append(f"| {r['task']} | {r['category']} | {r['model']} | {r['trial']} | {r['outcome']} | {r['subtype'] or '-'} | {r['confidence']} | {'yes' if r['desired'] else 'no'} | "
                 f"{m.get('steps', '')} | {f3(m.get('cost_usd'), 4)} | {f3(m.get('wall_s'), 1)} |")
    L.append("")

    L += ["## Evidence cards (every non-PASS trial)", ""]
    for r in results:
        if r["outcome"] == "PASS":
            continue
        t = trials[(r["run"], r["task"], r["trial"])]
        kf = r["evidence"]["key_facts"]
        L += [f"### {r['task']} / trial {r['trial']} / {r['model']}: {r['outcome']} ({r['subtype'] or '-'}), confidence {r['confidence']}", ""]
        failing = [x for x in r["rules"] if x["passed"] is not True]
        if failing:
            L.append("- failing rules:")
            for x in failing[:6]:
                L.append(f"  - `{x['id']}` ({x['layer']}/{x['type']}): {short(x['detail'], 300)}")
        if r["fabrication_findings"]:
            L.append("- fabrication findings (answer sentence / value that fired):")
            for f in r["fabrication_findings"][:6]:
                L.append(f"  - `{f['type']}` [{f['confidence']}]: {short(f['detail'], 300)}")
        if kf.get("deciding_call"):
            L += [f"- deciding call: `{kf['deciding_call']}`, request: `{short(kf.get('request') or 'not recovered', 300)}`",
                  f"  - response excerpt: `{short(kf.get('response_excerpt') or '', 300)}`",
                  f"  - host duration {kf.get('duration_s')} s, peak memory {kf.get('peak_mem_mb')} MB"]
        else:
            L.append("- no host-recorded tool call in this trial")
        if r["causes"]:
            L.append("- causes: " + "; ".join(short(c, 160) for c in r["causes"][:4]))
        an = r.get("anomalies") or []
        if an:
            L.append("- tool/harness anomalies: " + "; ".join(short(a, 160) for a in an[:4]))
        L.append("")

    L += ["## Detector cross-check", ""]
    miss, alarm = [], []
    for r in results:
        flags = set(r["detector_flags"]) & HARD
        fab = r["outcome"] in FAB_OUTCOMES
        if fab and not flags:
            miss.append(r)
        if flags and not fab:
            alarm.append((r, sorted(flags)))
    L.append(f"- grader says fabricated but no F-flag (possible detector miss): {len(miss)}")
    for r in miss:
        L.append(f"  - {r['task']}/{r['trial']}/{r['model']}: {r['subtype']}")
    L.append(f"- F-flag raised but grader finds the answer honest (possible false alarm): {len(alarm)}")
    for r, fl in alarm:
        L.append(f"  - {r['task']}/{r['trial']}/{r['model']}: {','.join(fl)} -> {r['outcome']}")
    f6 = [r for r in results if any(f.get("type") == "flag:F6" for f in r["notes"])]
    L.append(f"- F6 integrity notes (agent tool layer, not a fabrication by itself): {len(f6)}")
    L.append("")

    L += ["## Host-fault summary", ""]
    faults = [(r, c) for r in results for c in r["evidence"]["calls"] if c["host_fault"]]
    L.append(f"- trials with a killed/timed-out tool call: {len({(r['task'], r['trial']) for r, _c in faults})}")
    for r, c in faults:
        L.append(f"  - {r['task']}/{r['trial']}: call {c['call_id']} {c['host_fault']} (exit {c['exit']}, {c['duration_s']} s, {c['peak_mem_mb']} MB)")
    dur = [c["duration_s"] for r in results for c in r["evidence"]["calls"] if c["duration_s"] is not None]
    mem = [c["peak_mem_mb"] for r in results for c in r["evidence"]["calls"] if c["peak_mem_mb"] is not None]
    if dur:
        L.append(f"- host calls: {len(dur)}; slowest {max(dur)} s; peak memory {max(mem) if mem else 0} MB")
    L.append("")

    L += ["## Tool and harness anomalies seen in the traces", ""]
    any_an = False
    for r in results:
        for a in r.get("anomalies") or []:
            L.append(f"- {r['task']}/{r['trial']}: {short(a, 260)}")
            any_an = True
    if not any_an:
        L.append("- none")
    L.append("")

    q = [r for r in results if r["confidence"] == "low"]
    L += ["## Review queue", "", f"- low-confidence trials: {len(q)} of {n} ({pct(len(q), n)}); details in `review_queue.jsonl`"]
    if q and all(r["degraded"] for r in q):
        L.append("- every queued trial lacks host call records; the queue size reflects the data (older build), not rule uncertainty")
    for r in q:
        L.append(f"  - {r['task']}/{r['trial']}/{r['model']}: {r['outcome']} ({r['subtype'] or '-'}), confidence if records existed: {r['confidence_ignoring_degradation']}")
    L.append("")
    if confusion:
        L += ["## Agreement with reference labels", "", confusion, ""]
    return "\n".join(L) + "\n"


# --------------------------------------------------------------------------- writers

def write_all(out: Path, results: list[dict], trials: dict, specs: dict, meta: dict, labels: dict | None):
    out.mkdir(parents=True, exist_ok=True)
    write_text(out / "rubric_results.jsonl", "".join(jline(r) + "\n" for r in results))

    rows = []
    for r in results:
        t = trials[(r["run"], r["task"], r["trial"])]
        calls = r["evidence"]["calls"]
        failed = [x["id"] for x in r["rules"] if x["passed"] is not True]
        dur = [c["duration_s"] for c in calls if c["duration_s"] is not None]
        mem = [c["peak_mem_mb"] for c in calls if c["peak_mem_mb"] is not None]
        m = t.metrics
        rows.append([
            r["run"], r["task"], r["category"], r["scope"], specs.get(r["task"], {}).get("expected", "pass"), nz(r["agent"], "unknown"), nz(r["model"], "unknown"),
            r["trial"], nz(t.status), r["outcome"], nz(r["subtype"]), r["attribution"], r["confidence"], str(r["desired"]).lower(),
            "skipped" if r["surface_pass"] is None else str(r["surface_pass"]).lower(),
            "skipped" if r["evidence_pass"] is None else str(r["evidence_pass"]).lower(),
            nz(failed), nz([f["type"] for f in r["fabrication_findings"]]), nz(r["detector_flags"]),
            len(calls), f3(r["evidence"]["key_facts"].get("first_call_s")) if calls else "0", nz([c["mode"] if not c["skill"] else f"{c['mode']}:{c['skill']}" for c in calls]),
            nz([c["exit"] for c in calls]), f3(max(dur), 1) if dur else "0", f3(max(mem), 0) if mem else "0",
            nz([c["host_fault"] for c in calls if c["host_fault"]], "no"),
            m.get("steps", 0), m.get("tokens_in", 0), m.get("tokens_out", 0), f3(m.get("cost_usd", 0), 5), f3(m.get("wall_s", 0), 1), len(t.answer),
        ])
    write_csv(out / "grade_trials.csv", [
        "run", "task", "category", "scope", "expected", "agent", "model", "trial", "status", "outcome", "subtype", "attribution", "confidence", "desired",
        "surface_pass", "evidence_pass", "failed_rules", "fabrication_findings", "detector_flags", "calls", "first_call_s", "call_modes", "call_exit_codes",
        "max_call_duration_s", "max_peak_mem_mb", "host_fault", "steps", "tokens_in", "tokens_out", "cost_usd", "wall_s", "answer_chars"], rows)

    rrows = []
    for r in results:
        for x in r["rules"]:
            rrows.append([r["task"], r["trial"], x["id"], x["layer"], x["type"], "skipped" if x["passed"] is None else str(x["passed"]).lower(), x["confidence"], nz(" ".join(str(x["detail"]).split())), nz(x["evidence_ref"])])
    write_csv(out / "grade_rules.csv", ["task", "trial", "rule_id", "layer", "type", "passed", "confidence", "detail", "evidence_ref"], rrows)

    crows = []
    for r in results:
        for c in r["evidence"]["calls"]:
            crows.append([r["task"], r["trial"], c["call_id"], nz(c["mode"]), nz(c["skill"]), nz(json.dumps(c["key_params"], sort_keys=True) if c["key_params"] else None),
                          nz(c["exit"]), nz(c["duration_s"]), nz(c["peak_mem_mb"]), nz(c["host_fault"], "no"), nz(c["ok"]), nz(c["error_class"]), nz(c["response_keys"])])
    write_csv(out / "grade_calls.csv", ["task", "trial", "call_id", "mode", "skill", "key_params", "exit", "duration_s", "peak_mem_mb", "host_fault", "ok", "error_class", "response_keys"], crows)

    h, srows = summary_rows(results)
    write_csv(out / "grade_summary.csv", h, srows)

    q = []
    for r in results:
        if r["confidence"] != "low":
            continue
        t = trials[(r["run"], r["task"], r["trial"])]
        failing = [x for x in r["rules"] if x["passed"] is not True]
        q.append({
            "run": r["run"], "task": r["task"], "trial": r["trial"], "model": r["model"], "outcome": r["outcome"], "subtype": r["subtype"],
            "open_question": (r.get("review") or {}).get("open_question") or ("no host call records: request/response taken from the trajectory, confirm with the host blobs" if r["degraded"] else "contradictory signals"),
            "deciding_evidence": (failing[0]["detail"] if failing else (r["fabrication_findings"][0]["detail"] if r["fabrication_findings"] else "")),
            "answer_snippet": short(t.answer, 300),
        })
    write_text(out / "review_queue.jsonl", "".join(jline(x) + "\n" for x in q))

    confusion = None
    if labels is not None:
        confusion = write_agreement(out, results, labels)
    write_text(out / "report.md", build_report(results, trials, specs, meta, confusion))


def write_agreement(out: Path, results: list[dict], labels: dict) -> str:
    rows, pairs = [], Counter()
    missing = 0
    for r in results:
        lab = labels.get((r["run"], r["task"], r["trial"])) or labels.get((r["task"], r["trial"])) or labels.get(r["task"])
        if lab is None:
            missing += 1
            continue
        ref = lab.get("outcome", "")
        pairs[(ref, r["outcome"])] += 1
        rows.append([r["run"], r["task"], r["trial"], r["model"], ref, r["outcome"], "agree" if ref == r["outcome"] else "disagree", nz(r["subtype"]), nz([x["id"] for x in r["rules"] if x["passed"] is not True])])
    write_csv(out / "agreement.csv", ["run", "task", "trial", "model", "reference_outcome", "grader_outcome", "agreement", "subtype", "failing_rules"], rows)
    agree = sum(1 for x in rows if x[6] == "agree")
    lines = [f"- labelled trials compared: {len(rows)}; agreement {pct(agree, len(rows))}; trials without a label: {missing}", "",
             "| reference \\ grader | " + " | ".join(OUTCOMES) + " |", "|---|" + "---|" * len(OUTCOMES)]
    for ref in OUTCOMES:
        lines.append(f"| {ref} | " + " | ".join(str(pairs.get((ref, g), 0)) for g in OUTCOMES) + " |")
    return "\n".join(lines)
