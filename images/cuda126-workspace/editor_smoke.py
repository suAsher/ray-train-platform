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
    settings = Path('/home/ray/.local/share/code-server/User/settings.json')
    assert json.loads(settings.read_text())['python.defaultInterpreterPath'] == '/opt/raytrain/environment/bin/python'
    version = subprocess.check_output(['code-server', '--version'], text=True, timeout=30)
    assert version.startswith('4.99.4 '), version
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    with tempfile.TemporaryDirectory() as config_directory, tempfile.TemporaryFile(mode='w+') as log:
        # Keep the real default editor state path. A temporary explicit auth-none
        # config prevents code-server generating a password in the image layer.
        config = Path(config_directory) / 'config.yaml'
        config.write_text('auth: none\n')
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
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=5) as response:
                        assert response.status == 200
                        assert json.load(response)['status'] == 'alive'
                        break
                except (urllib.error.URLError, OSError):
                    if time.monotonic() >= deadline:
                        raise TimeoutError('code-server did not become ready')
                    time.sleep(0.2)
            print(json.dumps({'codeServerHTTP': 'passed', 'version': '4.99.4',
                              'defaultUserDataDirectory': '/home/ray/.local/share/code-server',
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
