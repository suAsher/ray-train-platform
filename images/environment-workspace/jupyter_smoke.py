#!/usr/bin/env python3
"""Real non-root Jupyter HTTP and managed-kernel gate in a disposable container.

Use the image's default HOME and Jupyter runtime path. Do not supply a writable
runtime-directory override: it would hide broken image directory ownership.
"""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

from jupyter_client import BlockingKernelClient
from jupyter_core.paths import jupyter_runtime_dir

EXPECTED_PREFIX = '/opt/raytrain/environment'


def request(base, path, method='GET', value=None):
    data = json.dumps(value).encode() if value is not None else None
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=5) as response:
        body = response.read(1024 * 1024)
        return response.status, json.loads(body) if body else None


def main():
    assert os.geteuid() != 0, 'Jupyter acceptance must run as the image non-root user'
    assert os.environ.get('JUPYTER_RUNTIME_DIR') is None, 'Do not mask default runtime-directory ownership'
    runtime = Path(jupyter_runtime_dir())
    assert runtime == Path('/home/ray/.local/share/jupyter/runtime'), str(runtime)
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    command = ['jupyter', 'lab', '--ip=127.0.0.1', f'--port={port}', '--no-browser',
               '--ServerApp.port_retries=0', '--IdentityProvider.token=',
               '--ServerApp.password=', '--ServerApp.disable_check_xsrf=True',
               '--ServerApp.root_dir=/workspace']
    kernel_id = None
    client = None
    with tempfile.TemporaryFile(mode='w+') as log:
        process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 60
            while True:
                if process.poll() is not None:
                    log.seek(0)
                    raise RuntimeError('Jupyter exited before becoming ready:\n' + log.read()[-6000:])
                try:
                    status, _ = request(base, '/api/status')
                    assert status == 200
                    break
                except (urllib.error.URLError, OSError):
                    if time.monotonic() >= deadline:
                        raise TimeoutError('Default Jupyter HTTP endpoint did not become ready')
                    time.sleep(0.2)
            status, kernel = request(base, '/api/kernels', 'POST', {'name': 'raytrain-environment'})
            assert status == 201, status
            kernel_id = kernel['id']
            connection_file = runtime / f'kernel-{kernel_id}.json'
            assert connection_file.is_file(), 'Kernel connection file was not written to the default runtime directory'
            client = BlockingKernelClient(connection_file=str(connection_file))
            client.load_connection_file()
            client.start_channels()
            client.wait_for_ready(timeout=30)
            message_id = client.execute('import sys; print(sys.prefix)')
            output = ''
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                message = client.get_iopub_msg(timeout=10)
                if message.get('parent_header', {}).get('msg_id') != message_id:
                    continue
                content = message['content']
                if message['msg_type'] == 'error':
                    raise RuntimeError('Managed notebook kernel execution failed')
                if message['msg_type'] == 'stream':
                    output += content['text']
                if message['msg_type'] == 'status' and content['execution_state'] == 'idle':
                    break
            assert output.strip() == EXPECTED_PREFIX, output
            assert runtime.stat().st_uid == os.geteuid(), 'Default runtime directory is not owned by the runtime user'
            print(json.dumps({'jupyterHTTP': 'passed', 'kernelHTTPCreate': 'passed',
                              'kernelExecution': 'passed', 'pythonPrefix': output.strip(),
                              'defaultRuntimeDirectory': str(runtime), 'uid': os.geteuid()}))
        finally:
            if client:
                client.stop_channels()
            if kernel_id and process.poll() is None:
                request(base, '/api/kernels/' + kernel_id, 'DELETE')
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


if __name__ == '__main__':
    main()
