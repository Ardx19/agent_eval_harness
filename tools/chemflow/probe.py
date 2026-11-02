#!/usr/bin/env python3
"""Probe the ChemFlow web agent API once, to see what an evaluation can record.

Three steps:

  python3 tools/chemflow/probe.py login            # you run this: asks for username and password
  python3 tools/chemflow/probe.py models           # saves the model catalog (no secrets)
  python3 tools/chemflow/probe.py run --provider P --model M [--thinking max]

`login` stores only the covaln_session cookie, in ~/.config/chemflow-eval/cookie
(mode 600). Nothing prints or saves the password, the cookie or the session
bearer token. The owning account's cookie authorizes session operations, so the
bearer token is never used and is removed from every saved response.

`run` creates one fresh conversation, submits one prompt, waits until the turn
ends and the conversation is idle (or the deadline passes), then saves the full
durable event history, the runs and the artifact list under the output folder.
It never answers an agent question; a pending question is recorded and the run stops.

Standard library only. API reference: https://demo.covaln.com (OpenAPI 2026-10-22).
"""
import argparse
import collections
import getpass
import http.cookies
import json
import os
import pathlib
import sys
import time
import urllib.error
import urllib.request
import uuid

BASE = "https://demo.covaln.com"
COOKIE = pathlib.Path.home() / ".config" / "chemflow-eval" / "cookie"
SECRET_KEYS = {"session_access_token", "access_token", "token", "cookie", "password"}
TERMINAL = {"succeeded", "aborted", "failed", "interrupted"}
DEFAULT_PROMPT = ("Is aspirin, CC(=O)Oc1ccccc1C(=O)O, synthesizable? Run a feasibility "
                  "assessment and tell me the number of steps in the best route.")


def scrub(obj):
    """A copy with every secret-looking key removed."""
    if isinstance(obj, dict):
        return {k: scrub(v) for k, v in obj.items() if k.lower() not in SECRET_KEYS}
    if isinstance(obj, list):
        return [scrub(v) for v in obj]
    return obj


def call(method, path, body=None, cookie=None, headers=None):
    """(status, parsed JSON, response headers). Errors come back as JSON too."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Accept", "application/json")
    # Cloudflare in front of the API refuses urllib's default User-Agent (error 1010).
    req.add_header("User-Agent", "chemflow-eval-probe/1.0")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if cookie:
        req.add_header("Cookie", "covaln_session=" + cookie)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None), r.headers
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            parsed = json.loads(raw)
        except ValueError:
            parsed = {"error": {"code": "non_json", "message": raw[:300].decode(errors="replace")}}
        return e.code, parsed, e.headers


def load_cookie():
    if not COOKIE.is_file():
        sys.exit("No login saved. Run: python3 tools/chemflow/probe.py login")
    return COOKIE.read_text().strip()


def save(folder, name, obj):
    path = folder / name
    path.write_text(json.dumps(scrub(obj), indent=2, ensure_ascii=False))
    return path


def cmd_login(_):
    username = input("ChemFlow username: ")
    password = getpass.getpass("ChemFlow password: ")
    status, body, headers = call("POST", "/v1/auth/login", {"username": username, "password": password})
    del password
    if status != 200:
        err = (body or {}).get("error", {})
        sys.exit(f"Login failed: HTTP {status} {err.get('code', '')}: {err.get('message', '')}")
    value = None
    for header in headers.get_all("Set-Cookie") or []:
        jar = http.cookies.SimpleCookie(header)
        if "covaln_session" in jar:
            value = jar["covaln_session"].value
    if not value:
        sys.exit("Login returned no covaln_session cookie.")
    COOKIE.parent.mkdir(parents=True, exist_ok=True)
    os.chmod(COOKIE.parent, 0o700)
    fd = os.open(COOKIE, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(value)
    print(f"Logged in as {username}. Cookie saved to {COOKIE} (mode 600).")


def cmd_models(args):
    cookie = load_cookie()
    status, body, _ = call("GET", "/v1/model-connections", cookie=cookie)
    if status != 200:
        sys.exit(f"HTTP {status}: {body}")
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    print("saved", save(out, "model-connections.json", body))
    print("default:", body.get("default_model"))
    for p in body.get("providers", []):
        for c in p.get("connections", []):
            for m in c.get("models", []) or [{"id": i} for i in c.get("model_ids", [])]:
                caps = m.get("capabilities", {})
                print(f"  {c.get('scope'):8} {p['id']:20} {m['id']:32} "
                      f"ctx={caps.get('context_window')} levels={caps.get('thinking_levels')} "
                      f"max->{(caps.get('thinking_level_map') or {}).get('max')}")


def paged_events(binding, cookie):
    events, after = [], 0
    while True:
        status, body, _ = call("GET", f"/v1/sessions/{binding}/events/snapshot?after={after}&limit=1000", cookie=cookie)
        if status != 200:
            raise RuntimeError(f"events HTTP {status}: {body}")
        page = body.get("events", [])
        events += page
        if len(page) < 1000:
            return events
        after = page[-1]["event_seq"]


def cmd_run(args):
    cookie = load_cookie()
    out = pathlib.Path(args.out) / time.strftime("probe-%Y%m%d-%H%M%S")
    out.mkdir(parents=True, exist_ok=True)
    model = {"connection": args.connection, "provider": args.provider, "id": args.model}
    status, created, _ = call("POST", "/v1/sessions", {"model": model, "thinking_level": args.thinking},
                              cookie=cookie, headers={"Idempotency-Key": f"eval-create-{uuid.uuid4()}"})
    if status not in (200, 201):
        sys.exit(f"create session HTTP {status}: {created}")
    save(out, "session-created.json", created)
    binding = created["session"]["bindingId"]
    print("conversation", binding)

    t0 = time.time()
    status, submitted, _ = call("POST", f"/v1/sessions/{binding}/turns", {"prompt": args.prompt},
                                cookie=cookie, headers={"Idempotency-Key": f"eval-turn-{uuid.uuid4()}"})
    if status not in (200, 202):
        sys.exit(f"submit turn HTTP {status}: {submitted}")
    turn_id = submitted["turn"]["turnId"]
    print("turn", turn_id)

    outcome = "deadline"
    while time.time() - t0 < args.deadline:
        time.sleep(5)
        _, turn, _ = call("GET", f"/v1/sessions/{binding}/turns/{turn_id}", cookie=cookie)
        _, view, _ = call("GET", f"/v1/sessions/{binding}", cookie=cookie)
        state = (turn or {}).get("turn", turn or {}).get("state") or (turn or {}).get("state")
        if (view or {}).get("waitingForUser"):
            outcome = "waiting_for_user"
            break
        idle = view and not view.get("busy") and not view.get("agentBusy") and not view.get("backgroundJobCount")
        if state in TERMINAL and idle:
            outcome = state
            break
        print(f"  {int(time.time() - t0)}s state={state} busy={view and view.get('busy')} "
              f"jobs={view and view.get('backgroundJobCount')}", flush=True)
    wall = round(time.time() - t0, 1)

    _, turn, _ = call("GET", f"/v1/sessions/{binding}/turns/{turn_id}", cookie=cookie)
    _, view, _ = call("GET", f"/v1/sessions/{binding}", cookie=cookie)
    _, runs, _ = call("GET", f"/v1/sessions/{binding}/runs", cookie=cookie)
    _, artifacts, _ = call("GET", f"/v1/sessions/{binding}/artifacts?limit=200", cookie=cookie)
    events = paged_events(binding, cookie)
    for name, obj in [("turn.json", turn), ("session-view.json", view), ("runs.json", runs),
                      ("artifacts.json", artifacts), ("events.json", events)]:
        save(out, name, obj)

    counts = collections.Counter(e.get("event_type") for e in events)
    summary = {"model": model, "thinking_level": args.thinking, "prompt": args.prompt, "outcome": outcome,
               "wall_s": wall, "binding_id": binding, "turn_id": turn_id, "events": len(events),
               "durable": sum(1 for e in events if e.get("durable", True)), "event_types": dict(counts)}
    save(out, "summary.json", summary)
    print(json.dumps(summary, indent=2))
    print("saved to", out)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("login")
    m = sub.add_parser("models")
    m.add_argument("--out", default="runs/chemflow")
    r = sub.add_parser("run")
    r.add_argument("--connection", default="shared", choices=["shared", "personal"])
    r.add_argument("--provider", required=True)
    r.add_argument("--model", required=True)
    r.add_argument("--thinking", default="max", choices=["off", "minimal", "low", "medium", "high", "xhigh", "max"])
    r.add_argument("--prompt", default=DEFAULT_PROMPT)
    r.add_argument("--deadline", type=int, default=900, help="seconds before giving up waiting")
    r.add_argument("--out", default="runs/chemflow")
    args = ap.parse_args()
    {"login": cmd_login, "models": cmd_models, "run": cmd_run}[args.cmd](args)


if __name__ == "__main__":
    main()
