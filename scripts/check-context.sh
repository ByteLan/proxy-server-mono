#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      [[ $# -ge 2 ]] || { echo "Usage: check-context.sh [--root DIR]" >&2; exit 2; }
      root=$(cd -- "$2" && pwd)
      shift 2 ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 2 ;;
  esac
done

python3 - "$root" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1]).resolve()
errors = []

SKIP_DIRS = {".git", "node_modules", "bin", "dist"}


def walk_files(base: pathlib.Path):
    for p in sorted(base.rglob("*")):
        rel_parts = p.relative_to(root).parts
        if any(part in SKIP_DIRS for part in rel_parts):
            continue
        if rel_parts[:3] == ("internal", "webui", "dist"):
            continue
        if p.is_file() and not p.is_symlink():
            yield p


# CTX-001: AGENTS.md <= 200 lines
for p in walk_files(root):
    if p.name == "AGENTS.md":
        lines = len(p.read_text(encoding="utf-8").splitlines())
        if lines > 200:
            errors.append(f"CTX-001: {p.relative_to(root)} has {lines} lines (limit: 200)")

# CTX-002: product-spec/ max 2 directory levels, no static version tags in filename or H1
spec_dir = root / "product-spec"
ver_re = re.compile(r"(?<![A-Za-z0-9])[vV]\d+(?:\.\d+)*(?![A-Za-z0-9])")
if spec_dir.exists():
    for p in walk_files(spec_dir):
        rel = p.relative_to(spec_dir)
        dir_depth = len(rel.parts) - 1
        if dir_depth < 1 or dir_depth > 2:
            errors.append(f"CTX-002: product-spec/{rel} directory depth is {dir_depth} (must be 1..2)")
        if ver_re.search(p.stem):
            errors.append(f"CTX-002: product-spec/{rel} filename contains static version tag")
        for line in p.read_text(encoding="utf-8").splitlines():
            if line.startswith("# "):
                if ver_re.search(line):
                    errors.append(f"CTX-002: product-spec/{rel} H1 heading contains static version tag: {line}")
                break

# CTX-003: docs/ entries must be YYYY-MM-DD-<topic> directories
docs_dir = root / "docs"
date_dir_re = re.compile(r"^\d{4}-\d{2}-\d{2}-[a-z0-9][a-z0-9-]*$")
if docs_dir.exists():
    for child in sorted(docs_dir.iterdir()):
        if child.name.startswith("."):
            continue
        if not child.is_dir() or not date_dir_re.match(child.name):
            errors.append(
                f"CTX-003: docs/{child.name} must be a dated archive directory matching YYYY-MM-DD-<topic>/"
            )

# CTX-004: Forbid personal machine absolute paths (/Users/<name>/ or /home/<name>/)
home_re = re.compile(r"/(?:Users|home)/[A-Za-z0-9._-]+/")
scan_targets = [
    root / "AGENTS.md",
    root / "README.md",
    root / "product-spec",
    root / "docs",
    root / "skills",
    root / "scripts",
]
for target in scan_targets:
    if not target.exists():
        continue
    files = [target] if target.is_file() else list(walk_files(target))
    for p in files:
        if p.name == "test_context_governance.py":
            continue
        text = p.read_text(encoding="utf-8", errors="replace")
        for lineno, line in enumerate(text.splitlines(), 1):
            if home_re.search(line):
                errors.append(f"CTX-004: {p.relative_to(root)}:{lineno} contains hardcoded personal absolute path")

# CTX-005: Canonical skills discovery symlinks
skills_dir = root / "skills"
if skills_dir.exists() and (root / ".agents" / "skills").exists():
    for skill in sorted(skills_dir.iterdir()):
        if not (skill.is_dir() and (skill / "SKILL.md").is_file()):
            continue
        for mirror in (".agents/skills", ".claude/skills", ".codex/skills"):
            link = root / mirror / skill.name
            if not (link.is_symlink() and link.resolve() == skill.resolve()):
                errors.append(f"CTX-005: missing or mismatched skill symlink {mirror}/{skill.name} -> skills/{skill.name}")

if errors:
    for err in errors:
        print(err, file=sys.stderr)
    sys.exit(1)
PY
