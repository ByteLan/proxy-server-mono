#!/usr/bin/env python3
"""Keep recent completed build artifacts belonging to one exact Actions workflow.

Only Actions artifacts are deleted. Releases, Git tags and other workflows are
never modified. --dry-run performs the same ownership checks without DELETE.
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict
from datetime import datetime


class PrivateRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        target = urllib.parse.urlsplit(newurl)
        if target.scheme != "https" or target.hostname != "api.github.com" or target.username or target.password:
            raise urllib.error.URLError("redirect outside GitHub API refused")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class GitHub:
    def __init__(self, repo, token):
        self.base = "https://api.github.com/repos/" + repo
        self.token = token

    def request(self, path, method="GET"):
        request = urllib.request.Request(
            self.base + path,
            method=method,
            headers={
                "Authorization": "Bearer " + self.token,
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "proxy-linux-artifact-cleanup",
            },
        )
        with urllib.request.build_opener(PrivateRedirect()).open(request, timeout=30) as response:
            body = response.read()
            return json.loads(body) if body else None

    def artifacts(self):
        page = 1
        while True:
            batch = self.request("/actions/artifacts?per_page=100&page=" + str(page))["artifacts"]
            yield from batch
            if len(batch) < 100:
                return
            page += 1


def select_deletions(artifacts, runs, workflow_id, current_run_id, keep, prefix):
    """Return old completed artifacts; never infer ownership from name alone."""
    groups = defaultdict(list)
    name_pattern = re.compile(re.escape(prefix) + r"[0-9a-f]{40}\Z")
    for artifact in artifacts:
        if not name_pattern.fullmatch(artifact.get("name", "")):
            continue
        run_id = (artifact.get("workflow_run") or {}).get("id")
        run = runs.get(run_id, {})
        if run.get("workflow_id") != workflow_id or not run.get("status"):
            continue
        # Invalid/missing dates are protected instead of guessed to be old.
        try:
            created = datetime.fromisoformat(artifact["created_at"].replace("Z", "+00:00"))
            if created.tzinfo is None:
                continue
        except (KeyError, TypeError, ValueError):
            continue
        groups[run_id].append((created, artifact))

    newest = sorted(groups, key=lambda run_id: max(t for t, _ in groups[run_id]), reverse=True)
    retained = set(newest[:keep]) | {current_run_id}
    return [artifact for run_id in reversed(newest) if run_id not in retained and runs[run_id]["status"] == "completed" for _, artifact in groups[run_id]]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=os.getenv("GITHUB_REPOSITORY", ""))
    parser.add_argument("--workflow", default="release.yml")
    parser.add_argument("--prefix", default="proxy-linux-")
    parser.add_argument("--keep", type=int, default=3)
    parser.add_argument("--current-run-id", type=int, default=int(os.getenv("GITHUB_RUN_ID", "0")))
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo):
        parser.error("--repo must be owner/repository")
    if args.keep < 1 or args.current_run_id < 1:
        parser.error("--keep and --current-run-id must be positive")
    if args.prefix != "proxy-linux-" or args.workflow != "release.yml":
        parser.error("this cleanup is restricted to release.yml and the proxy-linux- prefix")
    token = os.getenv("GH_TOKEN") or os.getenv("GITHUB_TOKEN")
    if not token:
        parser.error("GH_TOKEN or GITHUB_TOKEN is required")
    if len(token) > 4096 or any(ord(c) < 33 or ord(c) > 126 for c in token):
        parser.error("GitHub token contains invalid characters")
    github = GitHub(args.repo, token)
    workflow = github.request("/actions/workflows/" + urllib.parse.quote(args.workflow, safe=""))
    artifacts = list(github.artifacts())
    runs = {}
    for artifact in artifacts:
        if not re.fullmatch(re.escape(args.prefix) + r"[0-9a-f]{40}", artifact.get("name", "")):
            continue
        run_id = (artifact.get("workflow_run") or {}).get("id")
        if not isinstance(run_id, int) or run_id in runs:
            continue
        try:
            runs[run_id] = github.request("/actions/runs/" + str(run_id))
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            # A missing run cannot prove ownership/status; retain its artifacts.
            runs[run_id] = {}
    deletions = select_deletions(artifacts, runs, workflow["id"], args.current_run_id, args.keep, args.prefix)
    for artifact in deletions:
        run_id = artifact["workflow_run"]["id"]
        if args.dry_run:
            print(f"Would delete Actions artifact {artifact['id']} ({artifact['name']}, run {run_id})")
            continue
        # Recheck immediately before deletion. A concurrent/retried run is protected.
        run = github.request("/actions/runs/" + str(run_id))
        if run.get("workflow_id") != workflow["id"] or run.get("status") != "completed" or run_id == args.current_run_id:
            continue
        try:
            github.request("/actions/artifacts/" + str(artifact["id"]), method="DELETE")
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
        print(f"Deleted Actions artifact {artifact['id']} from completed run {run_id}")
    print(f"Cleanup complete: retain newest {args.keep} groups, always protecting current/active runs; releases unchanged.")


if __name__ == "__main__":
    try:
        main()
    except (urllib.error.URLError, KeyError, TypeError, ValueError) as error:
        print("Artifact cleanup failed; check GitHub API permissions, responses and network access.", file=sys.stderr)
        sys.exit(1)
