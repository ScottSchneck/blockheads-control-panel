#!/bin/sh
# Replaces the placeholder GitHub owner ("yourname") with yours, in the Go
# module path, imports and links. Run once from the repository root:
#   sh scripts/set-owner.sh your-github-name
set -eu
owner=${1:?usage: sh scripts/set-owner.sh your-github-name}
grep -rl --exclude-dir=.git --exclude=set-owner.sh "github.com/yourname/blockheads-control-panel" . |
  while read -r f; do
    sed -i.bak "s#github.com/yourname/blockheads-control-panel#github.com/${owner}/blockheads-control-panel#g" "$f"
    rm -f "$f.bak"
  done
echo "Module is now github.com/${owner}/blockheads-control-panel"
