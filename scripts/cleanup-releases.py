#!/usr/bin/env python3
"""Optional retention for CI main prereleases. Stable releases and tags are kept."""
import argparse
import datetime
import json
import os
import re
import subprocess
import sys

MAIN_TAG = re.compile(r"main-[0-9a-f]{12,40}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*\Z")


def date(value):
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return parsed.timestamp()
    except (ValueError, TypeError, AttributeError, OverflowError):
        return None


def plan(releases, keep=0, max_mib=0, protect_tag=""):
    """Return oldest deletions first, while retaining latest two and protected tag."""
    eligible = []
    for release in releases:
        tag = release.get("tag_name", "")
        timestamp = date(release.get("published_at"))
        if not (isinstance(tag, str) and MAIN_TAG.fullmatch(tag) and
                release.get("prerelease") is True and release.get("draft") is False and
                timestamp is not None and isinstance(release.get("id"), int) and release["id"] > 0):
            continue
        assets = release.get("assets")
        if not isinstance(assets, list) or any(not isinstance(a, dict) or not isinstance(a.get("size"), int) or isinstance(a.get("size"), bool) or a["size"] < 0 for a in assets):
            continue
        eligible.append((timestamp, release, sum(a["size"] for a in assets)))
    eligible.sort(key=lambda item: (item[0], item[1]["id"]), reverse=True)
    protected = {r[1]["id"] for r in eligible[:2]}
    protected.update(r[1]["id"] for r in eligible if r[1]["tag_name"] == protect_tag)
    removed = set()
    if keep:
        removed.update(r[1]["id"] for r in eligible[keep:] if r[1]["id"] not in protected)
    remaining_bytes = sum(size for _, r, size in eligible if r["id"] not in removed)
    # This is an operator-selected housekeeping target, not a GitHub billing quota.
    target_bytes = max_mib * 1024 * 1024 * 90 // 100 if max_mib else 0
    if max_mib:
        for _, release, size in reversed(eligible):
            if remaining_bytes <= target_bytes:
                break
            if release["id"] in removed or release["id"] in protected:
                continue
            removed.add(release["id"])
            remaining_bytes -= size
    deletions = [r for _, r, _ in reversed(eligible) if r["id"] in removed]
    return deletions, remaining_bytes, bool(max_mib and remaining_bytes > target_bytes)


def gh_api(*arguments):
    try:
        result = subprocess.run(["gh", "api", *arguments], check=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, text=True)
        return json.loads(result.stdout) if result.stdout.strip() else None
    except (subprocess.CalledProcessError, FileNotFoundError, json.JSONDecodeError) as exc:
        raise RuntimeError("GitHub API request failed; verify repository permissions and network access") from exc


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", ""))
    parser.add_argument("--keep", type=int, default=0, help="main prereleases to retain; 0 disables count limit, minimum 2")
    parser.add_argument("--max-mib", type=int, default=0, help="optional main asset budget; prune to 90%%; 0 disables")
    parser.add_argument("--protect-tag", default="")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if args.keep < 0 or args.keep == 1 or args.max_mib < 0:
        parser.error("--keep must be 0 or at least 2; --max-mib must be nonnegative")
    if not args.keep and not args.max_mib:
        print("Release cleanup disabled. GitHub Release assets are separate from Actions artifact storage.")
        return
    if not REPOSITORY.fullmatch(args.repo):
        parser.error("--repo must be owner/repository")
    pages = gh_api("--paginate", "--slurp", f"repos/{args.repo}/releases?per_page=100")
    if not isinstance(pages, list) or any(not isinstance(page, list) for page in pages):
        raise RuntimeError("Unexpected GitHub releases response")
    releases = [release for page in pages for release in page if isinstance(release, dict)]
    deletions, remaining, constrained = plan(releases, args.keep, args.max_mib, args.protect_tag)
    for release in deletions:
        print(f"{'Would delete' if args.dry_run else 'Deleting'} CI prerelease {release['tag_name']}")
        if not args.dry_run:
            # DELETE release removes its attachments, but never the Git tag.
            gh_api("--method", "DELETE", f"repos/{args.repo}/releases/{release['id']}")
    print(f"Planned remaining main prerelease assets: {remaining / (1024 * 1024):.1f} MiB; stable versions are untouched.")
    if constrained:
        print("::warning::The newest two/current releases exceed the selected housekeeping target; protected releases were retained.")


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
