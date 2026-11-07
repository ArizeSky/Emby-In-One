"""Run one deterministic, complete partition of backend race tests."""

import argparse
import json
import pathlib
import re
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int, required=True)
    parser.add_argument("--total", type=int, default=6)
    parser.add_argument("--output", type=pathlib.Path, default=pathlib.Path("race-report"))
    args = parser.parse_args()
    if not 0 <= args.shard < args.total:
        parser.error("shard must be between zero and total - 1")

    listing = subprocess.run(
        ["go", "test", "-race", "-p", "1", "-list", ".", "./internal/backend"],
        capture_output=True, text=True, timeout=600,
    )
    if listing.returncode:
        print(listing.stdout)
        print(listing.stderr, file=sys.stderr)
        return listing.returncode
    names = sorted(line.strip() for line in listing.stdout.splitlines()
                   if re.fullmatch(r"(?:Test\w+|Fuzz\w+|Example\w*)", line.strip()))
    if not names or len(names) != len(set(names)):
        raise RuntimeError("backend test inventory is empty or contains duplicates")
    selected = names[args.shard::args.total]
    if not selected:
        raise RuntimeError("shard has no tests")
    args.output.mkdir(parents=True, exist_ok=True)
    inventory = {"shard": args.shard, "total_shards": args.total,
                 "inventory": names, "selected": selected}
    (args.output / "inventory.json").write_text(json.dumps(inventory, indent=2))
    pattern = "^(?:" + "|".join(re.escape(name) for name in selected) + ")$"
    with (args.output / "results.jsonl").open("w") as log:
        result = subprocess.run(
            ["nice", "-n", "19", "go", "test", "-race", "-p", "1", "-parallel", "1",
             "-timeout", "20m", "-json", "./internal/backend", "-run", pattern, "-count=1"],
            stdout=log, stderr=subprocess.STDOUT,
        )
    started, passed, skipped, failures = set(), set(), set(), []
    for line in (args.output / "results.jsonl").read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        name, action = event.get("Test", ""), event.get("Action")
        if name in selected:
            if action == "run":
                started.add(name)
            elif action == "pass":
                passed.add(name)
            elif action == "skip":
                skipped.add(name)
        if action == "fail":
            failures.append(name or event.get("Package", "unknown"))
    summary = {"shard": args.shard, "selected": len(selected), "started": len(started),
               "passed": len(passed), "skipped": sorted(skipped),
               "failures": failures, "exit": result.returncode}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary))
    if result.returncode or failures or started != set(selected) or passed | skipped != started:
        raise RuntimeError("race shard failed or did not finish every selected test")
    return 0


if __name__ == "__main__":
    sys.exit(main())
