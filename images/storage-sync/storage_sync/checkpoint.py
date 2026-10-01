"""Fsynced manifests/checkpoints on the run PVC, with atomic replacement."""
import json
import os
from pathlib import Path
import tempfile
from .model import SyncError


def load_json(path):
    try:
        with Path(path).open('r', encoding='utf-8') as stream:
            return json.load(stream)
    except FileNotFoundError:
        return None
    except (ValueError, OSError):
        raise SyncError('CHECKPOINT_UNREADABLE') from None


def save_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    descriptor, temporary = tempfile.mkstemp(dir=path.parent, prefix='.sync-')
    try:
        with os.fdopen(descriptor, 'w', encoding='utf-8') as stream:
            json.dump(value, stream, ensure_ascii=False, sort_keys=True, separators=(',', ':'))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
