#!/usr/bin/env python3
"""Non-root code-server HTTP smoke in a disposable offline container."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def check_editor_http(base_url):
    # code-server intentionally excludes /healthz from its activity heartbeat.
    # Exercise the actual workbench first, then require a live initialized heart.
    with urllib.request.urlopen(base_url + '/', timeout=30) as response:
        assert response.status == 200
        assert response.headers['Content-Type'].startswith('text/html')
        assert b'vscode-workbench-web-configuration' in response.read()
    with urllib.request.urlopen(base_url + '/healthz', timeout=5) as response:
        assert response.status == 200
        health = json.load(response)
        assert health['status'] == 'alive'
        assert isinstance(health['lastHeartbeat'], int) and health['lastHeartbeat'] > 0


def main():
    assert os.geteuid() == 1000
    for directory in ('/home/ray/.local', '/home/ray/.local/share',
                      '/home/ray/.local/share/code-server'):
        assert Path(directory).stat().st_uid == os.geteuid(), directory
    # Default editor extensions/state must work without a writable-directory
    # override hiding broken ownership in the shipped image.
    with tempfile.NamedTemporaryFile(dir='/home/ray/.local/share/code-server',
                                     prefix='raytrain-editor-write-'):
        pass
    subprocess.run(['/opt/raytrain/environment/bin/python', '-I', '-c',
                    "import importlib.util; assert importlib.util.find_spec('jupyterlab') is None"],
                   check=True, timeout=30)
    settings = Path('/home/ray/.local/share/code-server/User/settings.json')
    assert json.loads(settings.read_text())['python.defaultInterpreterPath'] == '/opt/raytrain/environment/bin/python'
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    with tempfile.TemporaryDirectory() as config_directory, tempfile.TemporaryFile(mode='w+') as log:
        # Keep the real default editor state path. A temporary explicit auth-none
        # config prevents code-server generating a password in the image layer.
        config = Path(config_directory) / 'config.yaml'
        config.write_text('auth: none\n')
        version = subprocess.check_output(
            ['code-server', '--version', '--config', str(config)], text=True, timeout=30)
        assert version.startswith('4.99.4 '), version
        assert not Path('/home/ray/.config/code-server/config.yaml').exists()
        process = subprocess.Popen(
            ['code-server', '--bind-addr', f'127.0.0.1:{port}', '--auth', 'none',
             '--disable-telemetry', '--disable-update-check', '--config',
             str(config), '/workspace'], stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 60
            while True:
                if process.poll() is not None:
                    log.seek(0)
                    raise RuntimeError('code-server exited:\n' + log.read()[-6000:])
                try:
                    check_editor_http(f'http://127.0.0.1:{port}')
                    break
                except (urllib.error.URLError, OSError):
                    if time.monotonic() >= deadline:
                        raise TimeoutError('code-server did not become ready')
                    time.sleep(0.2)
            assert not Path('/home/ray/.config/code-server/config.yaml').exists()
            print(json.dumps({'codeServerHTTP': 'passed', 'version': '4.99.4',
                              'defaultUserDataDirectory': '/home/ray/.local/share/code-server',
                              'managedCanImportJupyterLab': False,
                              'pythonInterpreter': '/opt/raytrain/environment/bin/python'}))
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


if __name__ == '__main__':
    main()
