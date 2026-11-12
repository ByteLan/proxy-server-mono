import importlib.util
import pathlib
import unittest


spec = importlib.util.spec_from_file_location("cleanup_artifacts", pathlib.Path(__file__).with_name("cleanup-artifacts.py"))
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)


def artifact(number, run_id=None, **overrides):
    value = {
        "id": number,
        "name": "proxy-linux-" + str(number).zfill(40),
        "created_at": f"2026-11-{number:02d}T00:00:00Z",
        "workflow_run": {"id": run_id if run_id is not None else number},
    }
    value.update(overrides)
    return value


class CleanupSelection(unittest.TestCase):
    def setUp(self):
        self.runs = {number: {"workflow_id": 42, "status": "completed"} for number in range(1, 10)}

    def select(self, artifacts, current=99, keep=3):
        return cleanup.select_deletions(artifacts, self.runs, 42, current, keep, "proxy-linux-")

    def test_retains_three_newest_by_created_date(self):
        selected = self.select([artifact(4), artifact(2), artifact(5), artifact(1), artifact(3)])
        self.assertEqual([value["id"] for value in selected], [1, 2])

    def test_never_deletes_current_or_active_runs(self):
        self.runs[1]["status"] = "in_progress"
        self.assertEqual(self.select([artifact(i) for i in range(1, 6)], current=2), [])

    def test_new_current_run_counts_toward_retention(self):
        self.runs[6]["status"] = "in_progress"
        selected = self.select([artifact(i) for i in range(1, 7)], current=6)
        self.assertEqual([value["id"] for value in selected], [1, 2, 3])

    def test_never_deletes_other_workflow_or_prefix(self):
        self.runs[1]["workflow_id"] = 7
        self.assertEqual(self.select([artifact(1), artifact(2, name="other-" + "a" * 40), artifact(3)], keep=1), [])

    def test_multiple_artifacts_in_one_run_count_as_one_group(self):
        selected = self.select([artifact(1), artifact(2, run_id=1), artifact(3), artifact(4)], keep=2)
        self.assertEqual([value["id"] for value in selected], [1, 2])

    def test_missing_run_and_invalid_date_are_protected(self):
        self.assertEqual(self.select([artifact(1, run_id=500), artifact(2, created_at="invalid"), artifact(3)], keep=1), [])

    def test_paginates_all_artifacts(self):
        class FakeGitHub(cleanup.GitHub):
            def __init__(self):
                self.paths = []

            def request(self, path, method="GET"):
                self.paths.append(path)
                return {"artifacts": list(range(100)) if path.endswith("&page=1") else [100]}

        client = FakeGitHub()
        self.assertEqual(list(client.artifacts()), list(range(101)))
        self.assertEqual(client.paths, ["/actions/artifacts?per_page=100&page=1", "/actions/artifacts?per_page=100&page=2"])


if __name__ == "__main__":
    unittest.main()
