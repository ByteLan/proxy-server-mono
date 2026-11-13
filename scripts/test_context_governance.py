"""Tests for repository context governance gates (CTX-001..CTX-005)."""

from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
CHECKER = ROOT / "scripts" / "check-context.sh"


class ContextGovernanceTest(unittest.TestCase):
    def run_check(self, target: Path):
        return subprocess.run(
            ["bash", str(CHECKER), "--root", str(target)],
            capture_output=True,
            text=True,
            timeout=15,
        )

    def test_repository_passes_all_context_gates(self):
        res = self.run_check(ROOT)
        self.assertEqual(res.returncode, 0, res.stderr)

    def test_gate_violations_are_detected(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            # CTX-001: > 200 lines AGENTS.md
            (base / "AGENTS.md").write_text("# Giant\n" + ("- rule\n" * 205), encoding="utf-8")
            # CTX-002: depth > 2 and static version tag
            deep = base / "product-spec" / "a" / "b" / "c"
            deep.mkdir(parents=True)
            (deep / "design-v1.0.md").write_text("# Design (v1.0)\n", encoding="utf-8")
            # CTX-003: undated flat file in docs/
            (base / "docs").mkdir()
            (base / "docs" / "undated.md").write_text("# Old\n", encoding="utf-8")
            # CTX-004: hardcoded personal home path
            (base / "README.md").write_text("cd /" + "Users/alice/work/repo\n", encoding="utf-8")
            # CTX-006: web/src/onboarding.ts without sync references in AGENTS.md / skills
            (base / "web" / "src").mkdir(parents=True)
            (base / "web" / "src" / "onboarding.ts").write_text("// guide\n", encoding="utf-8")

            res = self.run_check(base)
            self.assertNotEqual(res.returncode, 0)
            for code in ("CTX-001", "CTX-002", "CTX-003", "CTX-004", "CTX-006"):
                self.assertIn(code, res.stderr)


if __name__ == "__main__":
    unittest.main()
