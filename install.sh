#!/bin/bash
# Shim. The installer itself lives at internal/installer/install.sh, where the
# binary can embed it: `agentpane install` runs exactly this script, so someone
# who downloaded a release binary needs no clone and no second file.
#
# This path is kept because it is the one in every doc and in muscle memory.
exec /bin/bash "$(cd "$(dirname "$0")" && pwd)/internal/installer/install.sh" "$@"
