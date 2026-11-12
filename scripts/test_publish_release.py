"""Publisher lifecycle tests use only a temporary Git repo and a fake gh executable."""
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


BUILD = r'''#!/usr/bin/env bash
set -euo pipefail
exec python3 - "$@" <<'PY'
import hashlib,json,os,pathlib,sys
state_path=pathlib.Path(os.environ['MOCK_STATE']); state=json.loads(state_path.read_text())
state['builds'].append({'args':sys.argv[1:],'version':os.environ['VERSION'],'commit':os.environ['COMMIT'],'time':os.environ['BUILD_TIME']})
state_path.write_text(json.dumps(state))
output=pathlib.Path(sys.argv[sys.argv.index('--output')+1]); output.mkdir(parents=True,exist_ok=True)
names=[f'{product}_linux_{arch}' for arch in ('amd64','arm64','armv7') for product in ('proxy-server','proxy-manage-server')]+['install-bundle.tar.gz','install.sh']
sums=[]
for name in names:
    payload=('fixture '+name).encode(); (output/name).write_bytes(payload)
    sums.append(hashlib.sha256(payload).hexdigest()+'  '+name+'\n')
(output/'SHA256SUMS').write_text(''.join(sums))
if state['scenario']=='dirty-after-build': pathlib.Path('new-source.go').write_text('package main\n')
if state['scenario']=='bad-checksum': (output/names[0]).write_bytes(b'corrupted')
PY
'''

GH = r'''#!/usr/bin/env python3
import json,os,pathlib,sys
path=pathlib.Path(os.environ['MOCK_STATE']); state=json.loads(path.read_text()); args=sys.argv[1:]
state['calls'].append(args)
def save(): path.write_text(json.dumps(state))
def fail(status=403):
    save(); print('gh: fixture failure (HTTP '+str(status)+')',file=sys.stderr); sys.exit(1)
scenario=state['scenario']; commit=state['commit']
if args[:2]==['auth','status']:
    if scenario=='no-auth': fail(401)
elif args[0]=='api':
    endpoint=args[1]
    if '/commits/' in endpoint:
        if scenario=='missing-commit' and not endpoint.endswith('/main'): fail(404)
        print(json.dumps({'sha':'0'*40 if scenario=='stale-main' and endpoint.endswith('/main') else commit}))
    elif '/git/ref/tags/' in endpoint:
        if not state['tag_sha']: fail(404)
        print(json.dumps({'object':{'type':'tag' if scenario=='annotated-tag' else 'commit','sha':state['tag_sha']}}))
    elif '/git/tags/' in endpoint:
        print(json.dumps({'object':{'type':'commit','sha':commit}}))
    elif '/releases?' in endpoint:
        if scenario=='metadata-failure': fail(403)
        print(json.dumps([state['release']] if state['release'] else []))
    else: fail()
elif args[:2]==['release','create']:
    state['release']={'tag_name':args[2],'draft':True,'target_commitish':commit,'assets':[]}
elif args[:2]==['release','upload']:
    if scenario=='upload-fails': fail()
    state['release']['assets']=[{'name':pathlib.Path(arg).name,'state':'uploaded'} for arg in args[3:] if pathlib.Path(arg).is_file()]
elif args[:2]==['release','edit']:
    state['release']['draft']=False
    if scenario!='annotated-tag': state['tag_sha']=commit
else: fail()
save()
'''


class PublishRelease(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="proxy-publish-test-")
        self.root = pathlib.Path(self.temporary.name)
        self.repo = self.root / "repo"
        (self.repo / "scripts").mkdir(parents=True)
        shutil.copyfile(pathlib.Path(__file__).with_name("publish-release.sh"), self.repo / "scripts/publish-release.sh")
        (self.repo / "scripts/build-release.sh").write_text(BUILD)
        (self.repo / ".gitignore").write_text("/bin/\n/release/\n/data/\n")
        self.git("init", "-q")
        self.git("config", "user.name", "Publisher Test")
        self.git("config", "user.email", "publisher@example.invalid")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        self.commit = self.git("rev-parse", "HEAD").strip()
        self.tag = "main-" + self.commit[:12]
        self.fakebin = self.root / "fakebin"
        self.fakebin.mkdir()
        (self.fakebin / "gh").write_text(GH)
        (self.fakebin / "gh").chmod(0o755)
        self.state = self.root / "state.json"
        self.state.write_text(json.dumps({"scenario":"new", "commit":self.commit, "calls":[], "builds":[], "tag_sha":None, "release":None}))

    def tearDown(self):
        self.temporary.cleanup()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True)

    def data(self):
        return json.loads(self.state.read_text())

    def change(self, **changes):
        state = self.data()
        state.update(changes)
        self.state.write_text(json.dumps(state))

    def run_publish(self, *args):
        environment = os.environ.copy()
        environment.update(PATH=str(self.fakebin) + os.pathsep + environment["PATH"], MOCK_STATE=str(self.state))
        return subprocess.run(["bash", str(self.repo / "scripts/publish-release.sh"), *args], cwd=self.repo,
                              env=environment, text=True, capture_output=True, timeout=15)

    def mutations(self):
        return [call for call in self.data()["calls"] if call[:2] in (["release","create"], ["release","upload"], ["release","edit"])]

    def test_dry_run_builds_and_checks_without_authentication_or_remote_calls(self):
        self.change(scenario="no-auth")
        result = self.run_publish("--dry-run", "--skip-web")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.data()["calls"], [])
        self.assertEqual(self.data()["builds"][0]["version"], self.tag)
        self.assertEqual(self.data()["builds"][0]["commit"], self.commit)
        self.assertIn("--skip-web", self.data()["builds"][0]["args"])
        self.assertIn("Nothing uploaded", result.stdout)

    def test_missing_credentials_refuses_before_build(self):
        self.change(scenario="no-auth")
        self.assertNotEqual(self.run_publish().returncode, 0)
        self.assertEqual(self.data()["builds"], [])
        self.assertEqual(self.mutations(), [])

    def test_published_release_is_not_built_or_overwritten(self):
        self.change(release={"tag_name":self.tag,"draft":False}, tag_sha=self.commit)
        result = self.run_publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.data()["builds"], [])
        self.assertEqual(self.mutations(), [])

    def test_new_release_is_drafted_uploaded_then_published(self):
        result = self.run_publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.mutations()
        self.assertEqual([call[1] for call in calls], ["create","upload","edit"])
        self.assertIn("--draft", calls[0])
        self.assertIn("--target", calls[0])
        self.assertIn("--prerelease=true", calls[-1])
        self.assertIn("--latest=false", calls[-1])
        self.assertEqual(len(self.data()["release"]["assets"]), 9)
        self.assertFalse(self.data()["release"]["draft"])

    def test_existing_draft_resumes_without_creating_another_release(self):
        self.change(release={"tag_name":self.tag,"draft":True,"target_commitish":self.commit,"assets":[]})
        result = self.run_publish("--skip-web")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call[1] for call in self.mutations()], ["upload","edit"])

    def test_missing_remote_commit_wrong_tag_and_metadata_failure_never_create(self):
        for scenario in ("missing-commit", "wrong-tag", "metadata-failure", "stale-main"):
            with self.subTest(scenario=scenario):
                self.change(scenario=scenario, calls=[], builds=[], tag_sha="0"*40 if scenario=="wrong-tag" else None)
                result = self.run_publish()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.data()["builds"], [])
                self.assertEqual(self.mutations(), [])

    def test_upload_failure_keeps_draft_unpublished(self):
        self.change(scenario="upload-fails")
        result = self.run_publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.data()["release"]["draft"])
        self.assertNotIn("edit", [call[1] for call in self.mutations()])

    def test_stable_annotated_tag_resolves_and_is_latest_by_default(self):
        self.change(scenario="annotated-tag", tag_sha="f"*40)
        result = self.run_publish("--tag", "v1.2.3")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--prerelease=false", self.mutations()[-1])
        self.assertIn("--latest=true", self.mutations()[-1])
        self.assertFalse(any(call[0]=="api" and call[1].endswith("/commits/main") for call in self.data()["calls"]))

    def test_dirty_after_build_and_bad_checksum_refuse_before_mutation(self):
        for scenario in ("bad-checksum", "dirty-after-build"):
            with self.subTest(scenario=scenario):
                self.change(scenario=scenario, calls=[], builds=[])
                self.assertNotEqual(self.run_publish().returncode, 0)
                self.assertEqual(self.mutations(), [])

    def test_dirty_initial_worktree_and_unsafe_output_are_rejected(self):
        (self.repo / "untracked.txt").write_text("uncommitted")
        self.assertNotEqual(self.run_publish().returncode, 0)
        self.assertEqual(self.data()["calls"], [])
        self.assertNotEqual(self.run_publish("--dry-run", "--output", "unignored-assets").returncode, 0)
        self.assertEqual(self.data()["builds"], [])


if __name__ == "__main__":
    unittest.main()
