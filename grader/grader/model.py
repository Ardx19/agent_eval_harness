"""Load recorded trials into a normalised, agent-neutral model.

A Trial holds the answer, the agent's messages and shell commands, and one Call per host-recorded tool run.
Each Call carries the request (from host call records when present, else recovered from the trajectory and
marked untrusted) and the parsed response (from the stdout blob when present, else from what the model saw).
"""
from __future__ import annotations

import ast
import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

RUN_RE = re.compile(r"run_retrosynthesis|toolstub|(?:^|[\s;|&(])retrosynthesis(?:\.sh)?(?=\s|$)")
_INSPECT = {"cat", "head", "tail", "less", "more", "grep", "egrep", "rg", "ls", "file", "stat", "find", "which", "type", "locate", "readlink",
            "wc", "sed", "awk", "diff", "md5sum", "sha256sum", "strings", "xxd", "od", "test", "[", "chmod", "realpath", "dirname", "basename", "nl", "cp", "mv"}
_SEG_SPLIT = re.compile(r"&&|\|\||[;|\n]")


def is_run_command(cmd: str) -> bool:
    """True when some command segment invokes the tool; commands that only inspect or locate the script are not runs."""
    for seg in _SEG_SPLIT.split(cmd or ""):
        if not RUN_RE.search(seg):
            continue
        words = [w for w in seg.split() if not re.match(r"[A-Za-z_]\w*=", w)]
        if words and words[0].lstrip("(") not in _INSPECT:
            return True
    return False


HEREDOC_RE = re.compile(r"<<-?\s*['\"]?(\w+)['\"]?[^\n]*\n(.*?)\n[ \t]*\1[ \t]*(?:\n|$)", re.S)
ECHO_RE = re.compile(r"(?:echo|printf)\s+(?:-[en]+\s+)?(?P<q>['\"])(?P<body>\{.*?\})(?P=q)\s*\|", re.S)
REQFILE_RE = re.compile(r"--request(?:=|\s+)(['\"]?)([^\s'\"|;&]+)\1")
HEX64 = re.compile(r"(?:sha256:)?([0-9a-f]{64})")


# --------------------------------------------------------------------------- json helpers

def parse_json_objects(text: str, want_key: str | None = "ok") -> list[dict]:
    """All top-level JSON objects found in `text`, in order. Trailing log noise after an object is ignored."""
    out: list[dict] = []
    if not text:
        return out
    dec = json.JSONDecoder()
    i = 0
    n = len(text)
    while i < n:
        j = text.find("{", i)
        if j < 0:
            break
        try:
            obj, end = dec.raw_decode(text, j)
        except ValueError:
            i = j + 1
            continue
        if isinstance(obj, dict) and (want_key is None or want_key in obj):
            out.append(obj)
            i = end
        else:
            i = j + 1
    return out


def _balanced_objects(text: str) -> list[str]:
    out = []
    depth = 0
    start = None
    in_str = None
    esc = False
    for i, ch in enumerate(text):
        if in_str:
            if esc:
                esc = False
            elif ch == "\\":
                esc = True
            elif ch == in_str:
                in_str = None
            continue
        if ch in "\"'":
            in_str = ch
        elif ch == "{":
            if depth == 0:
                start = i
            depth += 1
        elif ch == "}":
            if depth:
                depth -= 1
                if depth == 0 and start is not None:
                    out.append(text[start : i + 1])
                    start = None
    return out


def _loads_loose(s: str) -> Any:
    s = s.strip()
    try:
        return json.loads(s)
    except ValueError:
        pass
    try:
        return ast.literal_eval(s)
    except Exception:
        return None


# --------------------------------------------------------------------------- data classes

@dataclass
class Call:
    idx: int
    call_id: str
    argv: list
    args_valid: bool | None
    t_start: float | None
    duration_s: float | None
    exit_code: int | None
    peak_mem_mb: float | None
    oom_killed: bool = False
    timed_out: bool = False
    signaled: bool = False
    oom_suspect: bool = False
    truncated: bool = False
    validation: dict = field(default_factory=dict)
    req: dict | None = None
    req_text: str = ""
    req_src: str = "none"  # call_records | trajectory | none
    resp: dict | None = None
    resp_src: str = "none"  # blob | observation | none
    stdout_text: str = ""
    stderr_text: str = ""
    step_id: int | None = None
    agent_call_id: str | None = None
    obs_text: str = ""

    @property
    def mode(self) -> str | None:
        return (self.req or {}).get("mode")

    @property
    def skill(self) -> str | None:
        return (self.req or {}).get("skill")

    @property
    def kwargs(self) -> dict:
        k = (self.req or {}).get("kwargs")
        return k if isinstance(k, dict) else {}

    @property
    def host_fault(self) -> bool:
        return bool(self.oom_killed or self.timed_out or self.signaled or self.oom_suspect or self.exit_code == 137)

    @property
    def ok(self) -> bool | None:
        return None if self.resp is None else bool(self.resp.get("ok"))

    @property
    def result(self):
        return None if self.resp is None else self.resp.get("result")

    @property
    def error_text(self) -> str:
        if self.resp is None:
            return ""
        e = self.resp.get("error")
        return e if isinstance(e, str) else ""

    @property
    def error_class(self) -> str | None:
        """Launcher/tool error class: `TypeError`-style prefix, or the documented `result.error` code."""
        if self.resp is None:
            return None
        e = self.error_text
        if e:
            m = re.match(r"\s*([A-Za-z_]\w*(?:Error|Exception|Warning))\b", e)
            return m.group(1) if m else "error"
        r = self.result
        if isinstance(r, dict) and r.get("success") is False and isinstance(r.get("error"), str):
            return r["error"]
        return None

    @property
    def failed(self) -> bool:
        """The call did not give a usable result."""
        if self.host_fault:
            return True
        if self.resp is None:
            return True
        if not self.resp.get("ok"):
            return True
        r = self.result
        return bool(isinstance(r, dict) and r.get("success") is False)

    def req_get(self, path: str):
        """Look up a request field. `kwargs.x` is exact; a bare key is tried top-level then in kwargs."""
        r = self.req or {}
        if "." in path:
            cur: Any = r
            for part in path.split("."):
                if isinstance(cur, dict) and part in cur:
                    cur = cur[part]
                else:
                    return _MISSING
            return cur
        if path in r:
            return r[path]
        if path in self.kwargs:
            return self.kwargs[path]
        return _MISSING


class _Missing:
    def __repr__(self):
        return "<missing>"

    def __bool__(self):
        return False


_MISSING = _Missing()


@dataclass
class Trial:
    path: str
    run: str
    task: str
    trial: int
    model: str
    agent: str
    status: str
    failure: dict | None
    answer: str
    prompt_text: str
    steps: list
    calls: list
    flags: list
    signals: dict
    metrics: dict
    has_call_records: bool
    first_tool_step: int | None
    messages: list  # [(step_id, text)] agent messages
    observations: list  # [(step_id, agent_call_id, command, text)]
    provenance: dict
    raw: dict

    def calls_where(self, mode=None, skill=None, which="any", require_ok=False):
        out = []
        for c in self.calls:
            if mode and c.mode != mode:
                continue
            if skill and c.skill != skill:
                continue
            if require_ok and (c.failed or c.resp is None):
                continue
            out.append(c)
        if which == "first":
            return out[:1]
        if which == "last":
            return out[-1:]
        return out


# --------------------------------------------------------------------------- loading

def _blob_text(trial_dir: Path, ref: str | None) -> str | None:
    if not ref:
        return None
    m = HEX64.fullmatch(ref.strip())
    if not m:
        return None
    p = trial_dir / "blobs" / m.group(1)
    if not p.is_file():
        return None
    return p.read_bytes().decode("utf-8", "replace")


def _normalise_obs(content: Any) -> tuple[str, bool]:
    """(text, is_error) from an observation `content` (JSON string for pi, plain text for opencode)."""
    if not isinstance(content, str):
        return json.dumps(content, sort_keys=True), False
    s = content.strip()
    if s.startswith("{"):
        try:
            d = json.loads(s)
        except ValueError:
            d = None
        if isinstance(d, dict) and d and set(d) <= {"output", "error"}:
            if "error" in d:
                return str(d["error"]), True
            return str(d.get("output", "")), False
    return content, False


def _command_of(tc: dict) -> str:
    a = tc.get("arguments") or {}
    if isinstance(a, dict):
        if isinstance(a.get("command"), str):
            return a["command"]
    return ""


def _find_file_content(name: str, steps: list, upto: int) -> str | None:
    base = name.split("/")[-1]
    for s in steps:
        if s["step_id"] > upto:
            break
        for tc in s["tool_calls"]:
            a = tc.get("arguments") or {}
            p = a.get("path") or a.get("filePath")
            if tc["name"] == "write" and isinstance(p, str) and p.split("/")[-1] == base and isinstance(a.get("content"), str):
                return a["content"]
            cmd = _command_of(tc)
            if cmd:
                for m in HEREDOC_RE.finditer(cmd):
                    pre = cmd[: m.start()]
                    if re.search(r"(?:>|tee)\s*[\"']?\S*" + re.escape(base), pre.splitlines()[-1] if pre.splitlines() else pre):
                        return m.group(2)
    return None


def extract_requests(command: str, steps: list, upto: int) -> list[dict]:
    """Recover the request JSON(s) an agent command sent to the tool (best effort, untrusted)."""
    reqs: list[dict] = []
    if "--list-skills" in command and not HEREDOC_RE.search(command):
        return [{"mode": "list_skills"}]
    for m in HEREDOC_RE.finditer(command):
        d = _loads_loose(m.group(2))
        if isinstance(d, dict) and ("mode" in d or "skill" in d or "smiles_list" in d):
            reqs.append(d)
    if not reqs:
        for m in ECHO_RE.finditer(command):
            d = _loads_loose(m.group("body"))
            if isinstance(d, dict):
                reqs.append(d)
    if not reqs:
        for m in REQFILE_RE.finditer(command):
            txt = _find_file_content(m.group(2), steps, upto)
            if txt:
                d = _loads_loose(txt)
                if isinstance(d, dict):
                    reqs.append(d)
    if not reqs:
        for chunk in _balanced_objects(command):
            if '"mode"' in chunk or "'mode'" in chunk:
                d = _loads_loose(chunk)
                if isinstance(d, dict) and "mode" in d:
                    reqs.append(d)
    return reqs


def load_trial(trial_dir: Path) -> Trial:
    trace = json.loads((trial_dir / "trace.json").read_text(encoding="utf-8"))
    host = trace.get("host") or {}
    traj = trace.get("trajectory") or {}

    # ---- steps
    steps = []
    for s in traj.get("steps") or []:
        tcs = []
        for tc in s.get("tool_calls") or []:
            tcs.append(
                {
                    "id": tc.get("tool_call_id"),
                    "name": tc.get("function_name"),
                    "arguments": tc.get("arguments") or {},
                    "command": _command_of(tc),
                }
            )
        results = {}
        for r in (s.get("observation") or {}).get("results") or []:
            text, is_err = _normalise_obs(r.get("content"))
            results[r.get("source_call_id")] = (text, is_err)
        steps.append(
            {
                "step_id": s.get("step_id"),
                "source": s.get("source"),
                "message": s.get("message") or "",
                "tool_calls": tcs,
                "results": results,
            }
        )
    steps.sort(key=lambda x: (x["step_id"] is None, x["step_id"]))

    prompt_text = next((s["message"] for s in steps if s["source"] == "user"), "")
    messages = [(s["step_id"], s["message"]) for s in steps if s["source"] == "agent" and s["message"].strip()]

    observations = []
    for s in steps:
        cmd_by_id = {tc["id"]: tc for tc in s["tool_calls"]}
        for cid, (text, _err) in s["results"].items():
            tc = cmd_by_id.get(cid)
            observations.append((s["step_id"], cid, tc["command"] if tc else "", text))

    # ---- host calls + call records
    records = {}
    rec_text = _blob_text(trial_dir, host.get("call_records"))
    has_records = False
    if rec_text:
        try:
            arr = json.loads(rec_text)
            for r in arr:
                records[r.get("CallID")] = r
            has_records = True
        except ValueError:
            has_records = False

    htc = sorted(host.get("tool_calls") or [], key=lambda c: (c.get("t_start") is None, c.get("t_start") or 0.0))
    calls: list[Call] = []
    for i, hc in enumerate(htc):
        c = Call(
            idx=i,
            call_id=hc.get("call_id"),
            argv=hc.get("argv") or [],
            args_valid=hc.get("args_valid"),
            t_start=hc.get("t_start"),
            duration_s=hc.get("duration_s"),
            exit_code=hc.get("exit_code"),
            peak_mem_mb=hc.get("peak_mem_mb"),
            oom_killed=bool(hc.get("oom_killed")),
        )
        rec = records.get(c.call_id)
        if rec:
            res = rec.get("Result") or {}
            c.timed_out = bool(res.get("TimedOut"))
            c.signaled = bool(res.get("Signaled"))
            c.oom_suspect = bool(res.get("OOMSuspect"))
            c.truncated = bool(res.get("Truncated"))
            c.validation = rec.get("Validation") or {}
            if rec.get("ArgsValid") is not None:
                c.args_valid = rec.get("ArgsValid")
            it = rec.get("InputText")
            d = _loads_loose(it) if isinstance(it, str) and it.strip() else None
            if not isinstance(d, dict):
                p = rec.get("Params")
                d = p if isinstance(p, dict) else None
            if isinstance(d, dict):
                c.req, c.req_text, c.req_src = d, (it if isinstance(it, str) else json.dumps(d, sort_keys=True)), "call_records"
            sha = res.get("StdoutSHA256") or hc.get("stdout_sha256")
            if res.get("Duration") and not c.duration_s:
                c.duration_s = res["Duration"] / 1e9
        c.stdout_text = _blob_text(trial_dir, hc.get("stdout_sha256")) or ""
        c.stderr_text = _blob_text(trial_dir, hc.get("stderr_sha256")) or ""
        if c.stdout_text:
            objs = parse_json_objects(c.stdout_text)
            if objs:
                c.resp, c.resp_src = objs[0], "blob"
        calls.append(c)

    # ---- join host calls to agent tool calls
    links = {l.get("host_call_id"): l.get("tool_call_id") for l in (trace.get("provenance") or {}).get("links") or []}
    agent_runs = []  # (step_id, tool_call dict, [requests], obs_text)
    for s in steps:
        for tc in s["tool_calls"]:
            cmd = tc["command"]
            if cmd and is_run_command(cmd):
                reqs = extract_requests(cmd, steps, s["step_id"])
                obs = s["results"].get(tc["id"], ("", False))[0]
                agent_runs.append((s["step_id"], tc, reqs, obs))
    by_agent_id = {tc["id"]: (sid, tc, reqs, obs) for sid, tc, reqs, obs in agent_runs}

    assigned: dict[int, tuple] = {}
    used_agent = set()
    for c in calls:
        tid = links.get(c.call_id)
        if tid in by_agent_id:
            assigned[c.idx] = (by_agent_id[tid], 0)
            used_agent.add(tid)
    # fallback: remaining host calls in t_start order <- remaining agent runs in step order
    remaining_calls = [c for c in calls if c.idx not in assigned]
    queue = []
    for sid, tc, reqs, obs in agent_runs:
        if tc["id"] in used_agent:
            continue
        n = max(1, len(reqs))
        for k in range(n):
            queue.append(((sid, tc, reqs, obs), k))
    for c, item in zip(remaining_calls, queue):
        assigned[c.idx] = item

    for c in calls:
        item = assigned.get(c.idx)
        if not item:
            continue
        (sid, tc, reqs, obs), k = item
        c.step_id, c.agent_call_id, c.obs_text = sid, tc["id"], obs
        if c.req is None and k < len(reqs):
            c.req, c.req_src = reqs[k], "trajectory"
            c.req_text = json.dumps(reqs[k], sort_keys=True)
        if c.req is None and c.argv == ["--list-skills"]:
            c.req, c.req_src = {"mode": "list_skills"}, "trajectory"
        if c.resp is None and obs:
            objs = parse_json_objects(obs)
            if k < len(objs):
                c.resp, c.resp_src = objs[k], "observation"
            elif objs:
                c.resp, c.resp_src = objs[-1], "observation"
    for c in calls:
        if c.req is None and c.argv == ["--list-skills"]:
            c.req, c.req_src = {"mode": "list_skills"}, "trajectory"

    first_tool_step = agent_runs[0][0] if agent_runs else None
    if first_tool_step is None and calls:
        first_tool_step = next((c.step_id for c in calls if c.step_id is not None), None)

    run = trace.get("run_id") or trial_dir.parent.parent.name
    task = trace.get("task_id") or trial_dir.parent.name
    cfg = trace.get("agent_config") or {}
    return Trial(
        path=str(trial_dir),
        run=run,
        task=task,
        trial=int(trace.get("trial") or 1),
        model=cfg.get("model") or "",
        agent=cfg.get("agent") or "",
        status=trace.get("status") or "",
        failure=trace.get("failure"),
        answer=trace.get("final_answer") or "",
        prompt_text=prompt_text,
        steps=steps,
        calls=calls,
        flags=(trace.get("provenance") or {}).get("flags") or [],
        signals=trace.get("signals") or {},
        metrics=trace.get("metrics") or {},
        has_call_records=has_records,
        first_tool_step=first_tool_step,
        messages=messages,
        observations=observations,
        provenance=trace.get("provenance") or {},
        raw=trace,
    )


def discover_trials(data_dir: Path) -> list[Path]:
    """Trial dirs under `data_dir`, sorted. When a trial was retried, only the last attempt is kept."""
    found = sorted(p.parent for p in data_dir.rglob("trace.json"))
    best: dict[tuple, tuple[int, Path]] = {}
    for d in found:
        m = re.fullmatch(r"(trial-\d+)(?:-retry(\d+))?", d.name)
        key = (str(d.parent), m.group(1) if m else d.name)
        rank = int(m.group(2) or 0) if m else 0
        if key not in best or rank > best[key][0]:
            best[key] = (rank, d)
    return sorted((v[1] for v in best.values()), key=lambda p: str(p).replace("\\", "/"))
