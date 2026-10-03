#!/usr/bin/env python3
"""显式运行的隔离 Docker 运维回归；不连接云端、现存项目或真实模型。"""

import fcntl
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
import uuid

ROOT = Path(__file__).resolve().parents[1]


def command(*args, env=None, check=True, timeout=120):
    result = subprocess.run([str(a) for a in args], capture_output=True, env=env, timeout=timeout)
    if check and result.returncode:
        raise AssertionError(f'{args[0]} failed ({result.returncode}): {result.stderr.decode()}')
    return result


def docker_json(*args):
    return json.loads(command('docker', *args).stdout)


def wait_for(predicate, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(.1)
    raise AssertionError('Condition did not become ready')


class DockerOperations(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='hexclaw-ops-')
        cls.root = Path(cls.temporary.name)
        cls.prefix = 'hexclaw-ops-' + uuid.uuid4().hex[:10]
        cls.registry = cls.prefix + '-registry'
        cls.tags = []
        cls.images = {}
        cls.projects = []
        cls.volumes = []
        cls.addClassCleanup(cls.cleanup)
        command('docker', 'version', '--format', '{{.Server.Version}}')
        command('docker', 'run', '-d', '--name', cls.registry, '-p', '127.0.0.1::5000', 'registry:2')
        port = docker_json('inspect', cls.registry)[0]['NetworkSettings']['Ports']['5000/tcp'][0]['HostPort']
        cls.repository = f'127.0.0.1:{port}/{cls.prefix}'
        git = cls.root / 'repository'
        command('git', 'init', '--bare', git)
        env = dict(os.environ, GIT_AUTHOR_NAME='HexClaw test', GIT_COMMITTER_NAME='HexClaw test',
                   GIT_AUTHOR_EMAIL='test@example.invalid', GIT_COMMITTER_EMAIL='test@example.invalid')
        tree = subprocess.run(['git', '-C', str(git), 'mktree'], input=b'', capture_output=True, check=True).stdout.decode().strip()
        cls.first = command('git', '-C', git, 'commit-tree', tree, '-m', 'baseline', env=env).stdout.decode().strip()
        cls.next = command('git', '-C', git, 'commit-tree', tree, '-p', cls.first, '-m', 'candidate', env=env).stdout.decode().strip()
        cls.git = git
        command('git', '-C', git, 'update-ref', 'refs/heads/deploy', cls.next)
        build = cls.root / 'build'
        build.mkdir()
        shutil.copyfile(Path(__file__).with_name('fixture_service.py'), build / 'service.py')
        (build / 'Dockerfile').write_text('''FROM python:3.13-alpine
RUN pip install --no-cache-dir PyYAML==6.0.3
ARG REVISION
ARG CRASH=0
ARG ARCHIVE_FAIL=0
LABEL org.opencontainers.image.revision=$REVISION
ENV HOME=/data FIXTURE_CRASH=$CRASH
COPY service.py /service.py
RUN if [ "$ARCHIVE_FAIL" = "1" ]; then mv /bin/tar /bin/real-tar; printf '#!/bin/sh\\nexit 42\\n' > /bin/tar; chmod +x /bin/tar; fi
ENTRYPOINT ["python3", "/service.py"]
CMD ["--config", "/data/.hexclaw/hexclaw.yaml"]
''')
        for name, revision, crash, fail in [('old', cls.first, '0', '0'), ('next', cls.next, '0', '0'),
                                             ('crash', cls.next, '1', '0'), ('archive-fail', cls.first, '0', '1')]:
            tag = cls.repository + ':' + name
            cls.tags.append(tag)
            command('docker', 'build', '--quiet', '--tag', tag, '--build-arg', 'REVISION=' + revision,
                    '--build-arg', 'CRASH=' + crash, '--build-arg', 'ARCHIVE_FAIL=' + fail, build, timeout=180)
            command('docker', 'push', tag)
            image = docker_json('image', 'inspect', tag)[0]
            cls.images[name] = {'id': image['Id'], 'digest': image['RepoDigests'][0]}

    @classmethod
    def cleanup(cls):
        for project in cls.projects:
            command('docker', 'compose', '--project-directory', project, 'down', '--volumes', '--remove-orphans', check=False)
        for volume in cls.volumes:
            command('docker', 'volume', 'rm', volume, check=False)
        command('docker', 'rm', '-f', cls.registry, check=False)
        for tag in cls.tags:
            command('docker', 'image', 'rm', tag, check=False)
        for image in cls.images.values():
            command('docker', 'image', 'rm', image['digest'], check=False)
        cls.temporary.cleanup()

    def setUp(self):
        command('git', '-C', self.git, 'update-ref', 'refs/heads/deploy', self.next)
        self.project = self.root / self._testMethodName
        self.project.mkdir()
        self.projects.append(self.project)
        self.name = self.prefix + '-' + str(len(self.projects))
        self.volume = self.name + '-data'
        self.volumes.append(self.volume)
        self.settings = self.project / 'target.json'
        self.settings.write_text(json.dumps({'repository': str(self.git), 'ref': 'refs/heads/deploy',
                                             'compose_files': ['compose.yaml'], 'project_name': self.name}))
        self.compose = ['docker', 'compose', '--project-directory', str(self.project)]

    def start(self, image='old'):
        configuration = {'name': self.name, 'services': {'hexclaw': {
            'image': '${HEXCLAW_IMAGE:-' + self.images[image]['digest'] + '}',
            'volumes': ['data:/data'], 'stop_grace_period': '10s'}},
            'volumes': {'data': {'name': self.volume}}}
        (self.project / 'compose.yaml').write_text(json.dumps(configuration))
        command('docker', 'volume', 'create', self.volume)
        seed = """from pathlib import Path
import json
p=Path('/data/.hexclaw');p.mkdir(exist_ok=True)
(p/'hexclaw.yaml').write_text(json.dumps({'server':{'port':16060,'api_token':'fixture-only'},'storage':{'sqlite':{'path':str(p/'data.db')}}}))
for name,content in [('objects/original.txt','original attachment'),('memory/entries.json','[{"id":"memory-1","content":"original memory"}]'),('memory/events.json','{"event-1":"applied"}')]:
 f=p/name;f.parent.mkdir(exist_ok=True);f.write_text(content)
"""
        command('docker', 'run', '--rm', '--network', 'none', '-v', self.volume + ':/data',
                '--entrypoint', 'python3', self.images['old']['id'], '-c', seed)
        command(*self.compose, 'up', '-d', '--no-build', '--pull', 'never')
        wait_for(lambda: self.ready())

    def container(self):
        identity = command(*self.compose, 'ps', '-a', '-q', 'hexclaw').stdout.decode().strip()
        return docker_json('inspect', identity)[0]

    def ready(self):
        c = self.container()
        return c['State']['Running'] and command('docker', 'exec', c['Id'], 'python3', '-c',
            "import urllib.request;urllib.request.urlopen('http://127.0.0.1:16060/health')", check=False).returncode == 0

    def backup_command(self, *extra):
        return [sys.executable, str(ROOT / 'backup.py'), 'backup', '--project-dir', str(self.project),
                '--output', str(self.project / 'backups'), *extra]

    def deploy_command(self, image='next'):
        return [sys.executable, str(ROOT / 'deploy.py'), '--project-dir', str(self.project),
                '--target-config', str(self.settings), '--image', self.images[image]['digest'],
                '--commit', self.next, '--run-id', 'isolated-test']

    def receipt(self):
        files = [p for p in (self.project / '.hexclaw-deploy').glob('*.json') if not p.name.endswith('.compose.json') and p.name != 'current.json']
        return json.loads(max(files, key=lambda p: p.stat().st_mtime_ns).read_text())

    def read_data(self, volume=None):
        script = """import json,sqlite3,pathlib
p=pathlib.Path('/data/.hexclaw')
db=sqlite3.connect(str(p/'data.db'))
print(json.dumps({'receipts':db.execute('SELECT id,state FROM receipts ORDER BY id').fetchall(),'schema':db.execute('SELECT MAX(version) FROM schema_migrations').fetchone()[0],'attachment':(p/'objects/original.txt').read_text(),'memory':(p/'memory/entries.json').read_text(),'events':(p/'memory/events.json').read_text()}))
"""
        return json.loads(command('docker', 'run', '--rm', '--network', 'none', '-v', (volume or self.volume) + ':/data',
                         '--entrypoint', 'python3', self.images['old']['id'], '-c', script).stdout)

    def test_backup_restores_wal_assets_and_memory_without_starting_service(self):
        self.start()
        original = self.container()['Id']
        result = json.loads(command(*self.backup_command()).stdout)
        self.assertTrue(result['local_complete'] and result['service_restored'])
        self.assertEqual(original, self.container()['Id'])
        self.assertTrue(self.ready())
        archive = self.project / 'backups' / result['archive']
        with tarfile.open(archive) as outer:
            with tarfile.open(fileobj=io.BytesIO(outer.extractfile('home.tar.gz').read()), mode='r:gz') as inner:
                self.assertIn('./.hexclaw/data.db-wal', inner.getnames())
                self.assertGreater(inner.getmember('./.hexclaw/data.db-wal').size, 0)
        restored = self.name + '-restored'
        self.volumes.append(restored)
        args = [sys.executable, ROOT / 'backup.py', 'restore', '--archive', archive, '--sha256', result['sha256'], '--volume', restored]
        value = json.loads(command(*args).stdout)
        self.assertFalse(value['service_started'])
        self.assertEqual([], command('docker', 'ps', '-aq', '--filter', 'volume=' + restored).stdout.decode().split())
        self.assertEqual(self.read_data(), self.read_data(restored))
        self.assertEqual([['original-unknown', 'unknown']], self.read_data(restored)['receipts'])
        self.assertNotEqual(0, command(*args, check=False).returncode)
        self.assertEqual(self.read_data(), self.read_data(restored))

    def test_archive_failure_restarts_original_container(self):
        self.start('archive-fail')
        original = self.container()['Id']
        self.assertNotEqual(0, command(*self.backup_command(), check=False).returncode)
        report = json.loads(next((self.project / 'backups').glob('*.json')).read_text())
        self.assertFalse(report['local_complete'])
        self.assertTrue(report['service_restored'])
        self.assertEqual(original, self.container()['Id'])
        self.assertTrue(wait_for(self.ready))

    def test_remote_failure_does_not_claim_replication(self):
        self.start()
        boundary = self.project / 'boundary'
        boundary.mkdir()
        (boundary / 'ssh').write_text('#!/bin/sh\nexit 23\n')
        (boundary / 'ssh').chmod(0o755)
        env = dict(os.environ, PATH=str(boundary) + os.pathsep + os.environ['PATH'])
        self.assertNotEqual(0, command(*self.backup_command('--remote-host', 'fixture.invalid', '--remote-dir', '/backup'), env=env, check=False).returncode)
        report = json.loads(next((self.project / 'backups').glob('*.json')).read_text())
        self.assertTrue(report['local_complete'] and report['service_restored'])
        self.assertFalse(report['remote_complete'])
        self.assertTrue(wait_for(self.ready))

    def test_interrupt_during_stop_restores_original(self):
        self.start()
        original = self.container()['Id']
        process = subprocess.Popen(self.backup_command(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            wait_for(lambda: command('docker', 'exec', original, 'test', '-f', '/data/.hexclaw/stopping', check=False).returncode == 0)
            process.send_signal(signal.SIGTERM)
            _, stderr = process.communicate(timeout=20)
            self.assertEqual(130, process.returncode, stderr.decode())
            self.assertEqual(original, self.container()['Id'])
            self.assertTrue(wait_for(self.ready))
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()

    def test_lock_discards_superseded_commit_without_stopping(self):
        self.start()
        original = self.container()['Id']
        with (self.project / '.hexclaw-maintenance.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            process = subprocess.Popen(self.deploy_command(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                wait_for(lambda: list((self.project / '.hexclaw-deploy').glob('*.json')))
                command('git', '-C', self.git, 'update-ref', 'refs/heads/deploy', self.first)
                fcntl.flock(lock, fcntl.LOCK_UN)
                stdout, stderr = process.communicate(timeout=30)
                self.assertEqual(0, process.returncode, stderr.decode())
                self.assertEqual('superseded', json.loads(stdout)['status'])
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
        self.assertEqual(original, self.container()['Id'])
        self.assertEqual(self.images['old']['id'], self.container()['Image'])
        self.assertTrue(self.ready())

    def test_successful_deployment_retains_identity_and_data(self):
        self.start()
        before = self.read_data()
        value = json.loads(command(*self.deploy_command()).stdout)
        self.assertEqual('deployed', value['status'])
        self.assertEqual(self.images['next']['id'], self.container()['Image'])
        self.assertEqual(before, self.read_data())
        self.assertEqual(value['baseline'], value['verification'])
        self.assertTrue(self.ready())

    def test_prestart_failure_restores_old_program_on_original_data(self):
        self.start()
        before = self.read_data()
        boundary = self.project / 'boundary'
        boundary.mkdir()
        # 仓库边界第二次查询时模拟候选镜像被清理，后续 Compose create 真实失败。
        script = ('#!' + sys.executable + '\nimport pathlib,subprocess,sys\n'
                  'p=pathlib.Path(__file__).with_name("calls")\n'
                  'n=int(p.read_text())+1 if p.exists() else 1\np.write_text(str(n))\n'
                  'if n==2:\n subprocess.run(' + repr([shutil.which('docker'), 'image', 'rm',
                   self.repository + ':next', self.images['next']['digest']]) + ',stdout=subprocess.DEVNULL,check=True)\n'
                  'sys.exit(subprocess.call(' + repr([shutil.which('git')]) + '+sys.argv[1:]))\n')
        (boundary / 'git').write_text(script)
        (boundary / 'git').chmod(0o755)
        env = dict(os.environ, PATH=str(boundary) + os.pathsep + os.environ['PATH'])
        try:
            result = command(*self.deploy_command(), env=env, check=False)
            self.assertNotEqual(0, result.returncode)
            report = self.receipt()
            self.assertEqual('failed_before_start', report['status'])
            self.assertTrue(report['original_service_restored'])
            self.assertEqual(self.images['old']['id'], self.container()['Image'])
            self.assertEqual(before, self.read_data())
            self.assertTrue(self.ready())
        finally:
            command('docker', 'pull', self.images['next']['digest'])

    def test_started_failure_retains_migrated_data_and_receipt(self):
        self.start()
        self.assertNotEqual(0, command(*self.deploy_command('crash'), check=False).returncode)
        report = self.receipt()
        self.assertEqual('recovery_required', report['status'])
        self.assertEqual(self.images['crash']['id'], self.container()['Image'])
        data = self.read_data()
        self.assertEqual(2, data['schema'])
        self.assertEqual([['new-process', 'unknown'], ['original-unknown', 'unknown']], data['receipts'])
        self.assertEqual('original attachment', data['attachment'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
