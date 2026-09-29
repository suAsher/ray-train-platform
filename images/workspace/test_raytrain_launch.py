"""Run on the Linux builder: real owned process trees plus fake Ray control flow."""

from __future__ import annotations

import argparse
import contextlib
import importlib.util
import io
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import types
import unittest
from unittest import mock


SOURCE = pathlib.Path(__file__).with_name("raytrain-launch.py")
SPEC = importlib.util.spec_from_file_location("raytrain_launch", SOURCE)
launcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launcher)


TREE_SCRIPT = """
import json, os, pathlib, signal, subprocess, sys, time
directory, role, detached, stubborn, exit_code = sys.argv[1:]
directory = pathlib.Path(directory)
if stubborn == 'yes':
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
if role != 'grandchild':
    child_role = 'child' if role == 'root' else 'grandchild'
    subprocess.Popen([sys.executable, __file__, str(directory), child_role,
                      detached, stubborn, exit_code],
                     start_new_session=(detached == 'yes'))
(directory / (role + '.json')).write_text(json.dumps({'pid': os.getpid()}))
if role == 'root':
    while not (directory / 'release-root').exists():
        time.sleep(0.01)
    sys.exit(int(exit_code))
while True:
    time.sleep(0.05)
"""


def identity(pid):
    try:
        fields = pathlib.Path('/proc', str(pid), 'stat').read_text().rsplit(')', 1)[1].split()
        return fields[19], fields[0]
    except (FileNotFoundError, ProcessLookupError):
        return None


def running(pid, birth=None):
    current = identity(pid)
    return current is not None and current[1] != 'Z' and (birth is None or current[0] == birth)


def until(predicate, timeout=5):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.01)
    raise AssertionError('dedicated test process did not reach its expected state')


class ProcessTreeTests(unittest.TestCase):
    def setUp(self):
        self.assertEqual(sys.platform, 'linux', 'process ownership tests require the Linux builder')
        self.directory = tempfile.TemporaryDirectory(prefix='raytrain-process-test-')
        self.path = pathlib.Path(self.directory.name)
        self.script = self.path / 'synthetic_tree.py'
        self.script.write_text(TREE_SCRIPT)
        self.processes = []
        self.identities = {}

    def tearDown(self):
        for process in self.processes:
            process.stop('test_teardown')
        # A failing test may expose a cleanup defect. Kill only recorded test
        # PIDs whose Linux start time still matches, never an entire Pod.
        for pid, birth in self.identities.items():
            if running(pid, birth):
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        self.directory.cleanup()

    def process(self):
        process = launcher.ProcessTree(node_rank=0, term_grace=0.1, kill_grace=0.5)
        self.processes.append(process)
        return process

    def start_tree(self, detached=False, stubborn=False, exit_code=0):
        process = self.process()
        process.start([sys.executable, str(self.script), str(self.path), 'root',
                       'yes' if detached else 'no', 'yes' if stubborn else 'no', str(exit_code)])
        until(lambda: all((self.path / (role + '.json')).exists()
                          for role in ('root', 'child', 'grandchild')))
        for role in ('root', 'child', 'grandchild'):
            pid = json.loads((self.path / (role + '.json')).read_text())['pid']
            self.identities[pid] = identity(pid)[0]
        return process

    def assert_tree_gone(self):
        until(lambda: not any(running(pid, birth) for pid, birth in self.identities.items()))

    def finished(self, process):
        until(lambda: process.poll() is not None)
        return process.poll()

    def test_normal_exit_keeps_code_and_emits_safe_structured_events(self):
        output = io.StringIO()
        secret = 'synthetic-private-command-and-env-value'
        with contextlib.redirect_stdout(output), mock.patch.dict(os.environ, {'TOKEN': secret}):
            process = self.process()
            metadata = process.start([sys.executable, '-c', 'raise SystemExit(23)', secret])
            self.assertEqual(self.finished(process), 23)
            process.stop('finished')
        events = [json.loads(line.split(' ', 1)[1]) for line in output.getvalue().splitlines()
                  if line.startswith('[raytrain-runtime] ')]
        self.assertTrue({'start', 'exit', 'cleanup'}.issubset({event['event'] for event in events}))
        self.assertNotIn(secret, output.getvalue())
        self.assertGreater(metadata['pid'], 0)

    def test_poll_is_nonblocking_and_stop_does_not_kill_unrelated_process(self):
        collateral = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'],
                                      start_new_session=True)
        try:
            process = self.start_tree()
            started = time.monotonic()
            self.assertIsNone(process.poll())
            self.assertLess(time.monotonic() - started, 0.1)
            started = time.monotonic()
            process.stop('cancelled')
            self.assertLess(time.monotonic() - started, 3)
            self.assert_tree_gone()
            self.assertIsNone(collateral.poll())
        finally:
            collateral.terminate()
            collateral.wait(timeout=3)

    def test_term_ignoring_tree_gets_bounded_kill(self):
        process = self.start_tree(stubborn=True)
        started = time.monotonic()
        process.stop('sibling_failed')
        self.assertLess(time.monotonic() - started, 3)
        self.assert_tree_gone()

    def test_parent_exit_cleans_detached_descendants_holding_stdout(self):
        process = self.start_tree(detached=True, stubborn=True, exit_code=31)
        (self.path / 'release-root').touch()
        self.assertEqual(self.finished(process), 31)
        self.assert_tree_gone()
        self.assertEqual(process.stop('already_exited'), 31)

    def test_successful_parent_still_cleans_remaining_descendants(self):
        process = self.start_tree(detached=True)
        (self.path / 'release-root').touch()
        self.assertEqual(self.finished(process), 0)
        self.assert_tree_gone()

    def test_failed_exec_returns_nonzero_and_cleanup_is_idempotent(self):
        process = self.process()
        process.start([str(self.path / 'does-not-exist')])
        self.assertEqual(self.finished(process), 127)
        self.assertEqual(process.stop('launch_failed'), 127)
        self.assertEqual(process.stop('repeated'), 127)

    def test_signal_exit_is_not_reported_as_success(self):
        process = self.process()
        process.start([sys.executable, '-c', 'import os,signal; os.kill(os.getpid(),signal.SIGTERM)'])
        self.assertEqual(self.finished(process), -signal.SIGTERM)

    def test_stop_before_start_is_safe(self):
        process = self.process()
        process.stop('cancelled_before_start')
        process.stop('repeated')


class Ref:
    def __init__(self, value=None, error=None, kind='', rank=-1, ready=0):
        self.value, self.error, self.kind, self.rank, self.ready = value, error, kind, rank, ready


class RemoteMethod:
    def __init__(self, function):
        self.remote = function


class Actor:
    def __init__(self, ray, rank):
        self.ray, self.rank = ray, rank
        self.endpoint = RemoteMethod(lambda: Ref({'host': 'synthetic', 'ip': '127.0.0.1'}))
        self.start = RemoteMethod(self.start_process)
        self.poll = RemoteMethod(self.poll_process)
        self.stop = RemoteMethod(self.stop_process)
        self.wait = RemoteMethod(lambda: Ref(error=AssertionError('blocking actor wait prevents sibling cleanup')))

    def start_process(self, *args, **kwargs):
        self.ray.started.append(self.rank)
        error = RuntimeError('synthetic startup failure') if self.rank == self.ray.start_failure else None
        return Ref({'host': 'synthetic', 'ip': '127.0.0.1', 'nodeRank': self.rank,
                    'pid': 123 + self.rank}, error, 'start', self.rank)

    def poll_process(self):
        if self.ray.cancel and not self.ray.cancelled:
            self.ray.cancelled = True
            return Ref(error=KeyboardInterrupt(), kind='poll', rank=self.rank)
        codes = self.ray.codes[self.rank]
        code = codes.pop(0) if len(codes) > 1 else codes[0]
        ready = self.ray.poll_counts[self.rank] * len(self.ray.codes) + self.ray.order.index(self.rank)
        self.ray.poll_counts[self.rank] += 1
        return Ref(code, kind='poll', rank=self.rank, ready=ready)

    def stop_process(self, *args, **kwargs):
        self.ray.stopped.append(self.rank)
        return Ref(0, kind='stop', rank=self.rank)


class FakeRay(types.ModuleType):
    """Deterministic scheduling only; real cleanup is tested with subprocesses above."""
    def __init__(self, codes, start_failure=None, cancel=False, order=None):
        super().__init__('ray')
        self.codes = [list(values) for values in codes]
        self.poll_counts = [0 for _ in codes]
        self.order = order or list(range(len(codes)))
        self.start_failure, self.cancel, self.cancelled = start_failure, cancel, False
        self.started, self.stopped, self.actors, self.removed = [], [], [], []
        self.util = types.ModuleType('ray.util')
        self.util.get_node_ip_address = lambda: '127.0.0.1'
        self.placement = types.ModuleType('ray.util.placement_group')
        self.placement.placement_group = lambda *args, **kwargs: types.SimpleNamespace(ready=lambda: Ref(True))
        self.placement.remove_placement_group = lambda group: self.removed.append(group)

    def init(self, **kwargs):
        return None

    def remote(self, target=None, **options):
        if target is None:
            return lambda decorated: self.remote(decorated, **options)
        if not isinstance(target, type):
            return types.SimpleNamespace(remote=lambda *args: Ref(error=AssertionError(
                'single worker must remain cancellable through a launcher actor')))
        ray = self

        class RemoteActor:
            @classmethod
            def options(cls, **kwargs):
                return cls

            @classmethod
            def remote(cls, *args, **kwargs):
                actor = Actor(ray, len(ray.actors))
                ray.actors.append(actor)
                return actor

        return RemoteActor

    def get(self, refs, **kwargs):
        if isinstance(refs, list):
            if any(ref.kind == 'poll' for ref in refs):
                raise AssertionError('must consume ready worker results without blocking on every worker')
            return [self.get(ref) for ref in refs]
        if refs.error:
            raise refs.error
        return refs.value

    def wait(self, refs, num_returns=1, timeout=None, **kwargs):
        ready = sorted(refs, key=lambda ref: ref.ready)[:num_returns]
        return ready, [ref for ref in refs if ref not in ready]

    def kill(self, actor, **kwargs):
        if actor.rank not in self.stopped:
            raise AssertionError('Ray actor killed before owned process cleanup')

    def modules(self):
        return {'ray': self, 'ray.util': self.util, 'ray.util.placement_group': self.placement}


class OrchestrationTests(unittest.TestCase):
    def run_with(self, ray, mode='ray_train'):
        parsed = argparse.Namespace(mode=mode, workers=len(ray.codes), gpus_per_worker=1,
                                    command=['python', 'synthetic.py'], print_plan=False)
        with mock.patch.dict(sys.modules, ray.modules()), mock.patch.object(launcher, 'write_topology'), \
                mock.patch.object(launcher.time, 'sleep'):
            function = launcher.run_distributed if mode == 'ray_train' else launcher.run_single_or_torchrun
            result = function(parsed)
        self.assertEqual(set(ray.stopped), set(range(len(ray.actors))))
        if mode == 'ray_train':
            self.assertEqual(len(ray.removed), 1)
        return result

    def test_first_observed_failure_stops_running_siblings_and_preserves_code(self):
        ray = FakeRay([[None], [23]])
        self.assertEqual(self.run_with(ray), 23)

    def test_failure_order_is_completion_order_not_rank_order(self):
        ray = FakeRay([[19], [23]], order=[1, 0])
        self.assertEqual(self.run_with(ray), 23)

    def test_all_successful_workers_return_zero(self):
        self.assertEqual(self.run_with(FakeRay([[0], [None, 0]])), 0)

    def test_partial_start_failure_cleans_every_created_actor(self):
        ray = FakeRay([[None], [None]], start_failure=1)
        self.assertNotEqual(self.run_with(ray), 0)

    def test_distributed_cancellation_cleans_every_actor(self):
        self.assertEqual(self.run_with(FakeRay([[None], [None]], cancel=True)), 130)

    def test_single_worker_cancellation_cleans_process_tree(self):
        self.assertEqual(self.run_with(FakeRay([[None]], cancel=True), 'single_gpu'), 130)

    def test_single_worker_preserves_failure_code(self):
        self.assertEqual(self.run_with(FakeRay([[37]]), 'single_gpu'), 37)


if __name__ == '__main__':
    unittest.main()
