#!/usr/bin/env python3
"""Keep all vet analyzers enabled while recognizing unchanged upstream findings."""

import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[2]
BASELINE = Path(__file__).with_name("dm_driver_vet_baseline.json")


def main():
    baseline = json.loads(BASELINE.read_text())
    expected = {(item["path"], item["line"], item["message"])
                for item in baseline["diagnostics"]}
    result = subprocess.run(["go", "vet", "-mod=readonly", "./..."], cwd=ROOT / "dm",
                            text=True, capture_output=True)
    observed = []
    for line in (result.stdout + result.stderr).splitlines():
        if not line.strip() or line.startswith("# "):
            continue
        match = re.fullmatch(r"([^:]+):(\d+):\d+: (.+)", line)
        if not match:
            raise ValueError("unexpected vet output: " + line)
        observed.append((match[1].removeprefix("./"), int(match[2]), match[3]))
    unexpected = set(observed) - expected
    if result.returncode not in (0, 1) or unexpected or (result.returncode == 1 and not observed):
        print(result.stdout + result.stderr, end="", file=sys.stderr)
        raise ValueError("driver vet diagnostics differ from the reviewed upstream baseline")
    print("All vet analyzers ran; %d findings match the %d known upstream findings; no new findings."
          % (len(observed), len(expected)))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError) as error:
        raise SystemExit("DM driver vet check failed: %s" % error)
