"""Require the public main-branch CI run for this exact release commit."""

import json
import os
import time
import urllib.parse
import urllib.request


repository = os.environ["GITHUB_REPOSITORY"]
sha = os.environ["GITHUB_SHA"]
query = urllib.parse.urlencode({"head_sha": sha, "event": "push", "per_page": 10})
url = f"https://api.github.com/repos/{repository}/actions/workflows/ci.yml/runs?{query}"
last = None
for attempt in range(135):
    request = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + os.environ["GH_TOKEN"],
        "Accept": "application/vnd.github+json",
        "User-Agent": "Emby-In-One-release-gate",
    })
    with urllib.request.urlopen(request, timeout=30) as response:
        runs = json.load(response)["workflow_runs"]
    runs = [run for run in runs if run["head_sha"] == sha
            and run["head_branch"] == "main" and run["event"] == "push"]
    if runs:
        run = max(runs, key=lambda item: item["id"])
        state = (run["id"], run["run_attempt"], run["status"], run["conclusion"])
        if state != last:
            print("Exact-source CI:", state, flush=True)
            last = state
        if run["status"] == "completed":
            if run["conclusion"] != "success":
                raise RuntimeError("release refused: exact-source CI did not succeed")
            print("Accepted CI:", run["html_url"], flush=True)
            break
    time.sleep(20)
else:
    raise RuntimeError("release refused: exact-source CI did not finish within 45 minutes")
