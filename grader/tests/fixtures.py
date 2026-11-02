"""Build hand-made trace folders (trace.json + blobs/ + a host call-records blob) for tests.

The recorded run in data/ has no blobs and no call records, so evidence rules can only be tested on traces
we construct. `TraceBuilder` writes the same layout the recording system uses (SPEC section B).
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

NOISE = '\x1b[0;93m2026-10-03 21:27:09 [W:onnxruntime:Default] Failed to detect devices\x1b[m\n'


def sha(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


# --------------------------------------------------------------------------- response helpers

def wrap(mode, result, ok=True, error=None):
    return {"ok": ok, "tool": "retrosynthesis", "mode": mode, "result": result, "error": error}


def mol(smiles, in_stock=False, children=None):
    d = {"type": "mol", "hide": False, "smiles": smiles, "is_chemical": True, "in_stock": in_stock}
    if children:
        d["children"] = children
    return d


def rxn(children, prob=0.72):
    return {"type": "reaction", "hide": False, "is_reaction": True, "metadata": {"policy_probability": prob, "policy_name": "uspto"}, "children": children}


def route(root, leaves):
    return mol(root, False, [rxn([mol(s, True) for s in leaves])])


def feas_result(is_feasible=True, n_routes=3, steps=1, depth=2, inter=0, avail=1.0, conf=0.62, best=None, step_pass=True, reason="all thresholds met", max_steps=5, extra_thresholds=True):
    best = best or route("CCO", ["CC(=O)O", "C"])
    th = [{"threshold_name": "num_steps", "threshold_value": max_steps, "actual_value": steps, "passed": step_pass, "message": f"num_steps={steps} <= max_steps={max_steps}"}]
    if extra_thresholds:
        th += [
            {"threshold_name": "availability_ratio", "threshold_value": 0.5, "actual_value": avail, "passed": avail >= 0.5, "message": "availability"},
            {"threshold_name": "avg_confidence", "threshold_value": 0.3, "actual_value": conf, "passed": conf >= 0.3, "message": "confidence"},
        ]
    return {
        "is_feasible": is_feasible, "reason": reason, "num_routes": n_routes, "best_route_index": 0,
        "best_route_stats": {"index": 0, "num_steps": steps, "num_intermediates": inter, "depth": depth, "availability_ratio": avail, "avg_confidence": conf},
        "best_route": best, "routes": [best] * min(n_routes, 2), "threshold_results": {"results": th}, "message": "OK",
    }


def gen_result(routes, requested=5, found=None, canonical=None):
    found = len(routes) if found is None else found
    return {"success": True, "routes": routes, "search_time": 31.0, "message": "success",
            "metadata": {"routes_requested": requested, "routes_found": found, "routes_unique": found, "canonical_smiles": canonical, "cache_hit": False, "stock_used": "zinc"}}


def err_result(code, message="", **extra):
    return {"success": False, "error": code, "message": message, **extra}


# --------------------------------------------------------------------------- builder

class TraceBuilder:
    def __init__(self, task, prompt, model="golden-model", agent="pi", run="golden", seed=1):
        self.task, self.prompt, self.model, self.agent, self.run = task, prompt, model, agent, run
        self.steps = [{"step_id": 1, "timestamp": "t", "source": "user", "message": prompt}]
        self.host_calls, self.records, self.blobs, self.links, self.flags = [], [], {}, [], []
        self.status, self.failure, self.answer_text = "ok", None, ""
        self._t = 10.0
        self._n = 0
        self.metrics = {"wall_s": 300.0, "steps": 0, "tokens_in": 1000, "tokens_out": 100, "cost_usd": 0.01}
        self.records_enabled = True
        self.files_changed = []

    def _step(self, **kw):
        sid = len(self.steps) + 1
        st = {"step_id": sid, "timestamp": "t", "source": "agent", "model_name": self.model, "message": "", "tool_calls": [], "metrics": {"prompt_tokens": 10, "completion_tokens": 5}}
        st.update(kw)
        self.steps.append(st)
        return st

    def _blob(self, text: str) -> str:
        b = text.encode("utf-8")
        h = sha(b)
        self.blobs[h] = b
        return h

    def message(self, text):
        self._step(message=text)
        return self

    def shell(self, command, output, error=False):
        self._n += 1
        cid = f"call_{self._n:04d}"
        content = json.dumps({"error": output} if error else {"output": output})
        self._step(tool_calls=[{"tool_call_id": cid, "function_name": "bash", "arguments": {"command": command}}],
                   observation={"results": [{"source_call_id": cid, "content": content}]})
        return self

    def tool(self, request, response, duration=60.0, exit_code=0, mem=5400.0, message="", args_valid=True, validation_errors=None,
             stdout_extra=NOISE, timed_out=False, signaled=False, oom=False, via="heredoc", argv=None, raw_stdout=None):
        """One real tool run: an agent step, a host record, a call record and a stdout blob."""
        self._n += 1
        cid = f"call_{self._n:04d}"
        hid = f"tb{len(self.host_calls) + 1}"
        req_json = json.dumps(request, indent=2)
        if request.get("mode") == "list_skills":
            cmd = "./scripts/run_retrosynthesis.sh --list-skills"
        elif via == "pipe":
            cmd = f"echo '{json.dumps(request)}' | ./scripts/run_retrosynthesis.sh"
        else:
            cmd = f"./scripts/run_retrosynthesis.sh << 'EOF'\n{req_json}\nEOF\n"
        stdout = raw_stdout if raw_stdout is not None else (json.dumps(response, indent=2) + "\n" + stdout_extra if response is not None else "")
        h_out = self._blob(stdout)
        h_err = self._blob("")
        self._t += 5.0
        t_start = self._t
        self._t += duration
        self.host_calls.append({
            "call_id": hid, "pid": 100 + len(self.host_calls), "ppid": 1, "tool": "retrosynthesis", "argv": argv if argv is not None else [],
            "args_valid": args_valid, "t_start": t_start, "duration_s": duration, "exit_code": exit_code, "stdout_sha256": h_out,
            "stderr_sha256": h_err, "receipt_valid": True, "cpu_s": duration, "peak_mem_mb": mem, "oom_killed": oom, "outputs": []})
        self.records.append({
            "CallID": hid, "Tool": "retrosynthesis", "Argv": argv or [], "ArgsValid": args_valid,
            "Validation": {"errors": validation_errors or [], "warnings": None}, "TStart": t_start,
            "InputText": json.dumps(request), "Params": request,
            "Result": {"ExitCode": exit_code, "StdoutSHA256": h_out, "StderrSHA256": h_err, "Duration": int(duration * 1e9), "PeakMemMB": mem,
                       "TimedOut": timed_out, "Signaled": signaled, "OOMSuspect": oom, "Truncated": False},
            "RunnerError": ""})
        self.links.append({"tool_call_id": cid, "proxy_tool_use_id": cid, "host_call_id": hid, "observation_matches_verified": True, "model_saw_matches": True})
        self._step(message=message, tool_calls=[{"tool_call_id": cid, "function_name": "bash", "arguments": {"command": cmd}}],
                   observation={"results": [{"source_call_id": cid, "content": json.dumps({"output": stdout})}]})
        return self

    def flag(self, code, subject, evidence="x"):
        self.flags.append({"code": code, "subject": subject, "evidence": evidence})
        return self

    def answer(self, text):
        self.answer_text = text
        self._step(message=text)
        return self

    def set_status(self, status, failure=None):
        self.status, self.failure = status, failure
        return self

    def write(self, directory: Path, trial=1):
        directory.mkdir(parents=True, exist_ok=True)
        host = {"tool_calls": self.host_calls, "proxy_transcript": "sha256:" + "0" * 64, "files_changed": self.files_changed}
        if self.records_enabled:
            rec_text = json.dumps(self.records)
            host["call_records"] = "sha256:" + self._blob(rec_text)
        trace = {
            "schema_version": "evalharness-2", "task_id": self.task, "run_id": f"{self.run}", "trial": trial, "seed": 1,
            "agent_config": {"agent": self.agent, "version": "0", "provider": "test", "model": self.model, "skills": ["retrosynthesis"], "mcps": [], "web": False},
            "status": self.status, "trajectory": {"agent": {"name": self.agent}, "steps": self.steps, "final_metrics": {}},
            "host": host,
            "provenance": {"links": self.links, "shell_only_results": [], "unsupported_claims": [], "flags": self.flags},
            "integrity": {"stream_mismatches": [], "unexplained_file_changes": [], "tier_b_missing_events": []},
            "signals": {"answer_seeking": [], "eval_awareness": [], "canary_touched": [], "injection_followed": None},
            "final_answer": self.answer_text,
            "metrics": {**self.metrics, "steps": len(self.steps)},
        }
        if self.failure:
            trace["failure"] = self.failure
        (directory / "trace.json").write_text(json.dumps(trace, indent=1, sort_keys=True), encoding="utf-8", newline="\n")
        blobs = directory / "blobs"
        blobs.mkdir(exist_ok=True)
        for h, b in sorted(self.blobs.items()):
            (blobs / h).write_bytes(b)
        return directory / "trace.json"
