import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("cleanup_releases", Path(__file__).with_name("cleanup-releases.py"))
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)


def release(number, **changes):
    data = dict(id=number, tag_name=f"main-{number:012x}", prerelease=True, draft=False,
                published_at=f"2026-11-{number:02d}T00:00:00Z", assets=[dict(size=100 * 1024 * 1024)])
    data.update(changes)
    return data


class Retention(unittest.TestCase):
    def test_count_and_stable_protection(self):
        versions = [release(i) for i in range(1, 7)] + [release(8, tag_name="v1.0.0", prerelease=False)]
        deleted, size, constrained = cleanup.plan(versions, keep=3)
        self.assertEqual([v["id"] for v in deleted], [1, 2, 3])
        self.assertEqual(size, 300 * 1024 * 1024)
        self.assertFalse(constrained)

    def test_capacity_and_rollback_protection(self):
        deleted, size, constrained = cleanup.plan([release(i) for i in range(1, 7)], max_mib=300)
        self.assertEqual([v["id"] for v in deleted], [1, 2, 3, 4])
        self.assertEqual(size, 200 * 1024 * 1024)
        self.assertFalse(constrained)
        deleted, size, constrained = cleanup.plan([release(1), release(2)], max_mib=100)
        self.assertEqual(deleted, [])
        self.assertTrue(constrained)

    def test_explicit_current_and_malformed_are_preserved(self):
        versions = [release(i) for i in range(1, 7)]
        versions += [release(8, draft=True), release(9, published_at="bad"), release(10, assets=[dict(size=-1)])]
        deleted, _, _ = cleanup.plan(versions, keep=2, protect_tag=versions[0]["tag_name"])
        self.assertEqual([v["id"] for v in deleted], [2, 3, 4])


if __name__ == "__main__":
    unittest.main()
