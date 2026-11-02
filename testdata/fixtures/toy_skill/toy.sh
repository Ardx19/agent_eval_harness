#!/usr/bin/env bash
# Toy skill used by tests. Behaviour is chosen by the first argument.
HERE="$(cd "$(dirname "$0")" && pwd)"
case "${1:-}" in
  ok)      echo "{\"ok\": true, \"tool\": \"toy\", \"stdin\": \"$(cat)\"}" ;;
  fail)    echo "Traceback (most recent call last):" >&2
           echo "  File \"$HERE/impl/core.py\", line 7, in run" >&2
           echo "ConfigurationError: data_dir=$HERE/data and /home/someone/secret/weights.onnx missing" >&2
           echo "{\"ok\": false, \"error\": \"ConfigurationError: missing $HERE/data/x.hdf5\"}"; exit 1 ;;
  sleep)   sleep "${2:-5}" ;;
  big)     head -c 300000 /dev/zero | tr '\0' 'x' ;;
  burn)    python3 -c "b=bytearray(60*1024*1024); b[::4096]=b'x'*len(b[::4096]); import time; t=time.time()
while time.time()-t<0.4: pass" ;;
  suicide) kill -9 $$ ;;
  env)     env | cut -d= -f1 | sort | tr '\n' ' ' ;;
  file)    shift; [ "$1" = "--request" ] && cat "$2" ;;
  smiles)  echo '{"smiles": "F/C=C/F", "note": "C/C=C/C is not a path"}' ;;
  *)       echo "usage: toy.sh ok|fail|sleep|big|burn|suicide|env|smiles|file" >&2; exit 2 ;;
esac
