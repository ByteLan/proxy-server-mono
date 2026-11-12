#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 - "$script_dir/.." "$@" <<'PY'
"""Explicit local build/publish; never starts Actions, pushes Git or updates hosts."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.parse


class PublishError(Exception):
    pass


ROOT = pathlib.Path(sys.argv[1]).resolve()
ASSETS = [f"{product}_linux_{arch}" for arch in ("amd64", "arm64", "armv7")
          for product in ("proxy-server", "proxy-manage-server")]
ASSETS += ["install-bundle.tar.gz", "install.sh"]
FILES = ASSETS + ["SHA256SUMS"]


def git(*arguments):
    return subprocess.check_output(["git", *arguments], cwd=ROOT, text=True).strip()


def require_clean(commit):
    if git("rev-parse", "HEAD") != commit or git("status", "--porcelain", "--untracked-files=all"):
        raise PublishError("Publishing requires an unchanged, clean Git worktree (including untracked files). Commit and push changes, then retry.")


def gh(*arguments, missing_ok=False):
    environment = os.environ.copy()
    environment["GH_HOST"] = "github.com"
    result = subprocess.run(["gh", *arguments], cwd=ROOT, env=environment, text=True, capture_output=True)
    if result.returncode:
        if missing_ok and re.search(r"\(HTTP 404\)", result.stderr):
            return None
        raise PublishError("GitHub command failed: gh " + " ".join(arguments[:2]) + ". Check local gh authentication and repository permissions.")
    return result.stdout


def api(endpoint, missing_ok=False):
    response = gh("api", endpoint, missing_ok=missing_ok)
    if response is None:
        return None
    try:
        return json.loads(response)
    except json.JSONDecodeError as error:
        raise PublishError("GitHub returned invalid JSON; refusing to publish.") from error


def find_release(repo, tag):
    # Listing includes authenticated drafts, which may not have a tag ref yet.
    page = 1
    while True:
        releases = api(f"repos/{repo}/releases?per_page=100&page={page}")
        if not isinstance(releases, list):
            raise PublishError("GitHub release metadata was not a list.")
        for release in releases:
            if release.get("tag_name") == tag:
                return release
        if len(releases) < 100:
            return None
        page += 1


def check_tag(repo, tag, commit):
    quoted = urllib.parse.quote(tag, safe="")
    reference = api(f"repos/{repo}/git/ref/tags/{quoted}", missing_ok=True)
    if reference is None:
        return False
    obj = reference.get("object", {})
    seen = set()
    for _ in range(8):
        sha = obj.get("sha", "")
        if not re.fullmatch(r"[0-9a-f]{40}", sha) or sha in seen:
            break
        seen.add(sha)
        if obj.get("type") == "commit":
            if sha != commit:
                raise PublishError("Existing remote tag points to a different commit; tags are never moved.")
            return True
        if obj.get("type") != "tag":
            break
        obj = api(f"repos/{repo}/git/tags/{sha}").get("object", {})
    raise PublishError("Cannot verify the existing remote tag's commit.")


def check_draft(release, tag_exists, commit):
    if release is not None and release.get("draft") is True and not tag_exists and release.get("target_commitish") != commit:
        raise PublishError("Draft without a tag must target the exact build commit; refusing to reuse it.")


def check_main(repo, commit):
    if api(f"repos/{repo}/commits/main").get("sha") != commit:
        raise PublishError("Default main release must match GitHub main HEAD. Push/checkout current main first, or use a stable v* tag.")


def verify_assets(output):
    if {path.name for path in output.iterdir()} != set(FILES):
        raise PublishError("Release output must contain exactly the eight known assets and SHA256SUMS; use a clean output directory.")
    for name in FILES:
        path = output / name
        if path.is_symlink() or not path.is_file():
            raise PublishError("Release assets must be regular files: " + name)
    sums = {}
    for line in (output / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) != 2 or not re.fullmatch(r"[0-9a-fA-F]{64}", fields[0]) or fields[1] not in ASSETS or fields[1] in sums:
            raise PublishError("SHA256SUMS must list each expected asset exactly once.")
        sums[fields[1]] = fields[0].lower()
    if set(sums) != set(ASSETS):
        raise PublishError("SHA256SUMS is missing an expected asset.")
    for name in ASSETS:
        digest = hashlib.sha256()
        with (output / name).open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        if digest.hexdigest() != sums[name]:
            raise PublishError("Checksum mismatch: " + name)


def main():
    parser = argparse.ArgumentParser(prog="publish-release.sh", description="Build locally and explicitly publish GitHub Release assets. Never pushes Git or runs Actions.")
    parser.add_argument("--repo", default="ByteLan/proxy-server-mono")
    parser.add_argument("--tag", help="Default main-<current SHA12> prerelease; v* creates a stable release")
    parser.add_argument("--skip-web", action="store_true", help="Use committed embedded frontend instead of rebuilding it")
    parser.add_argument("--output", default=str(ROOT / "bin/releases"))
    parser.add_argument("--dry-run", action="store_true", help="Build/check only; no gh login or remote operations required")
    latest = parser.add_mutually_exclusive_group()
    latest.add_argument("--latest", dest="latest", action="store_true", help="Mark a stable release as latest (default for v*)")
    latest.add_argument("--no-latest", dest="latest", action="store_false", help="Do not mark a stable release as latest")
    parser.set_defaults(latest=None)
    args = parser.parse_args(sys.argv[2:])
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo):
        parser.error("--repo must be owner/repository")
    commit = git("rev-parse", "HEAD")
    default_tag = "main-" + commit[:12]
    tag = args.tag or default_tag
    if not re.fullmatch(r"v[A-Za-z0-9][A-Za-z0-9._+-]*", tag) and tag != default_tag:
        parser.error("--tag must be v* or the current main-<SHA12>")
    prerelease = tag.startswith("main-")
    if prerelease and args.latest:
        parser.error("A main prerelease cannot be marked latest")
    mark_latest = not prerelease if args.latest is None else args.latest
    output = pathlib.Path(args.output).expanduser().resolve()
    if output == ROOT or ROOT in output.parents:
        relative = str(output.relative_to(ROOT))
        ignored = subprocess.run(["git", "check-ignore", "--quiet", "--no-index", relative], cwd=ROOT).returncode == 0
        if relative == "." or ".git" in output.relative_to(ROOT).parts or not ignored:
            parser.error("Output inside the repository must be gitignored (e.g. bin/releases or release); otherwise choose an external directory")

    if not args.dry_run:
        require_clean(commit)
        if shutil.which("gh") is None:
            raise PublishError("Install GitHub CLI and run gh auth login before publishing.")
        gh("auth", "status", "--hostname", "github.com")
        if api(f"repos/{args.repo}/commits/{commit}").get("sha") != commit:
            raise PublishError("The exact local commit is not readable on GitHub. Push it before publishing.")
        tag_exists = check_tag(args.repo, tag, commit)
        release = find_release(args.repo, tag)
        if release is not None and release.get("draft") is False:
            print(f"Release {tag} is already published; no build, upload or overwrite performed.")
            return
        check_draft(release, tag_exists, commit)
        if prerelease:
            check_main(args.repo, commit)

    environment = os.environ.copy()
    environment.update(VERSION=tag, COMMIT=commit, BUILD_TIME=git("show", "-s", "--format=%cI", commit))
    command = ["bash", str(ROOT / "scripts/build-release.sh"), "--output", str(output)]
    if args.skip_web:
        command.append("--skip-web")
    subprocess.run(command, cwd=ROOT, env=environment, check=True)
    verify_assets(output)
    if args.dry_run:
        print(f"Dry run complete: {tag}, eight checksums / nine files verified in {output}. Nothing uploaded.")
        if git("status", "--porcelain", "--untracked-files=all"):
            print("Working tree has changes; commit and push them before a real publish.")
        return

    require_clean(commit)
    tag_exists = check_tag(args.repo, tag, commit)
    release = find_release(args.repo, tag)
    if release is not None and release.get("draft") is False:
        print(f"Release {tag} was published during the build; leaving it unchanged.")
        return
    check_draft(release, tag_exists, commit)
    if prerelease:
        check_main(args.repo, commit)
    if release is None:
        with tempfile.TemporaryDirectory(prefix="proxy-publish-notes-") as directory:
            notes = pathlib.Path(directory) / "notes.md"
            notes.write_text(f"Local build of commit {commit}.\n\nLinux amd64, arm64 and ARMv7 raw binaries, bootstrap installer and SHA256SUMS.\n", encoding="utf-8")
            gh("release", "create", tag, "--repo", args.repo, "--target", commit, "--title", tag,
               "--notes-file", str(notes), "--draft", "--prerelease=" + str(prerelease).lower(), "--latest=false")
    # Recheck before allowing --clobber: published releases are never updated.
    release = find_release(args.repo, tag)
    if release is None or release.get("draft") is not True:
        raise PublishError("Release is no longer a draft; refusing to upload.")
    gh("release", "upload", tag, "--repo", args.repo, *[str(output / name) for name in FILES], "--clobber")
    release = find_release(args.repo, tag)
    if release is None or release.get("draft") is not True:
        raise PublishError("Release is no longer a draft; refusing to publish.")
    assets = release.get("assets", [])
    if len(assets) != len(FILES) or {asset.get("name") for asset in assets} != set(FILES) or any(asset.get("state") != "uploaded" for asset in assets):
        raise PublishError("Draft asset set is incomplete or unexpected; it remains unpublished.")
    tag_exists = check_tag(args.repo, tag, commit)
    check_draft(release, tag_exists, commit)
    if prerelease:
        check_main(args.repo, commit)
    gh("release", "edit", tag, "--repo", args.repo, "--draft=false",
       "--prerelease=" + str(prerelease).lower(), "--latest=" + str(mark_latest).lower())
    published = find_release(args.repo, tag)
    if published is None or published.get("draft") is not False or not check_tag(args.repo, tag, commit):
        raise PublishError("Cannot confirm published release state and exact tag commit.")
    print(f"Published https://github.com/{args.repo}/releases/tag/{tag}")


try:
    main()
except (PublishError, subprocess.CalledProcessError, OSError, ValueError, KeyError) as error:
    print("Publish failed: " + str(error).rstrip(".") + ". Inspect the release state before retrying.", file=sys.stderr)
    sys.exit(1)
except KeyboardInterrupt:
    print("Publishing cancelled. Inspect the release state before retrying.", file=sys.stderr)
    sys.exit(130)
PY
