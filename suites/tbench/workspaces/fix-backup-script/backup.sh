#!/bin/bash
# usage: backup.sh <file> <backup-dir>
cp $1 $2/
echo "backed up $1"
