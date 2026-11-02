#!/usr/bin/env bash
# Installs Harbor's own ATIF validator into an isolated venv (tools/harbor-venv)
# so tests can check our trajectories against the real thing. Pinned.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$HOME/.local/bin"
uv venv tools/harbor-venv --python 3.12
uv pip install --python tools/harbor-venv/bin/python "harbor==0.23.0"
tools/harbor-venv/bin/python -m harbor.utils.trajectory_validator --help >/dev/null && echo "harbor 0.23.0 validator ready"
