#!/bin/bash
# usage: backup.sh <file> <backup-dir>
if [ ! -f "$1" ]; then
  echo "backup.sh: no such file: $1" >&2
  exit 1
fi
mkdir -p "$2"
cp "$1" "$2/"
echo "backed up $1"
