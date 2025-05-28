#!/bin/bash
set -euo pipefail

cd /path/to/actions-runner
./run.sh --once

# After the job, clean up the workspace
rm -rf _work/*