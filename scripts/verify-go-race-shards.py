"""Reject missing, duplicate or unsuccessful backend race partitions."""

import collections
import json
import pathlib
import sys


root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "race-reports")
folders = sorted(root.glob("race-shard-*"))
if not folders:
    raise RuntimeError("no race reports")
reports = [json.loads((folder / "inventory.json").read_text()) for folder in folders]
total = reports[0]["total_shards"]
if total != 6 or len(reports) != total or {r["shard"] for r in reports} != set(range(total)):
    raise RuntimeError("incomplete or duplicate race shards")
inventory = reports[0]["inventory"]
if not inventory or any(r["inventory"] != inventory or r["total_shards"] != total for r in reports):
    raise RuntimeError("race shards used different inventories")
counts = collections.Counter(name for report in reports for name in report["selected"])
if set(counts) != set(inventory) or any(count != 1 for count in counts.values()):
    raise RuntimeError("race inventory has missing or duplicate tests")
passed, skipped = 0, []
for folder, report in zip(folders, reports):
    summary = json.loads((folder / "summary.json").read_text())
    selected = len(report["selected"])
    if (summary["shard"] != report["shard"] or summary["selected"] != selected
            or summary["started"] != selected or summary["exit"] or summary["failures"]
            or summary["passed"] + len(summary["skipped"]) != selected):
        raise RuntimeError("unsuccessful or incomplete race shard")
    passed += summary["passed"]
    skipped.extend(summary["skipped"])
print(json.dumps({"inventory": len(inventory), "passed": passed,
                  "skipped": sorted(skipped), "exact_once_partition": True}))
