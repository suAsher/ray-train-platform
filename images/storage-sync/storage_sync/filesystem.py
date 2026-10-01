"""Read-only IDC traversal rooted in directory descriptors, never resolved paths."""
from contextlib import contextmanager
from datetime import datetime, timezone
import base64
import binascii
import hashlib
import heapq
import json
import os
from pathlib import Path
import stat

from .model import SourceEntry, SyncError, canonical_digest, safe_relative


_READ_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK
_DIR_FLAGS = _READ_FLAGS | os.O_DIRECTORY
_HASH_CHUNK = 1024 * 1024


def _identity(value):
    return value.st_dev, value.st_ino, stat.S_IFMT(value.st_mode)


def _metadata(value):
    return (*_identity(value), value.st_size, value.st_mtime_ns, value.st_ctime_ns)


def _supported(value):
    if not (stat.S_ISREG(value.st_mode) or stat.S_ISDIR(value.st_mode)):
        raise SyncError('UNSUPPORTED_SOURCE_TYPE')


def _unchanged(before, after):
    if _metadata(before) != _metadata(after):
        raise SyncError('SOURCE_CHANGED')


def _child(parent, name):
    before = os.stat(name, dir_fd=parent, follow_symlinks=False)
    _supported(before)
    flags = _DIR_FLAGS if stat.S_ISDIR(before.st_mode) else _READ_FLAGS
    descriptor = os.open(name, flags, dir_fd=parent)
    try:
        after = os.fstat(descriptor)
        _supported(after)
        _unchanged(before, after)
        return descriptor, after
    except BaseException:
        os.close(descriptor)
        raise


def _check_chain(root, chain):
    for index, (descriptor, before, name) in enumerate(chain):
        current = (os.stat(root, follow_symlinks=False) if index == 0 else
                   os.stat(name, dir_fd=chain[index - 1][0], follow_symlinks=False))
        if _identity(before) != _identity(current):
            raise SyncError('SOURCE_CHANGED')
        if _identity(before) != _identity(os.fstat(descriptor)):
            raise SyncError('SOURCE_CHANGED')


@contextmanager
def _node(root, path):
    """Keep every parent open so rename/symlink races cannot redirect reads."""
    chain = []
    try:
        descriptor = os.open(root, _DIR_FLAGS)
        chain.append((descriptor, os.fstat(descriptor), ''))
        parts = path.split('/') if path else []
        for index, name in enumerate(parts):
            descriptor, metadata = _child(chain[-1][0], name)
            chain.append((descriptor, metadata, name))
            if index < len(parts) - 1 and not stat.S_ISDIR(metadata.st_mode):
                raise SyncError('INVALID_PATH')
        _check_chain(root, chain)
        yield chain[-1][0], chain[-1][1]
        _unchanged(chain[-1][1], os.fstat(chain[-1][0]))
        _check_chain(root, chain)
    except OSError:
        raise SyncError('SOURCE_UNAVAILABLE') from None
    finally:
        for descriptor, _, _ in reversed(chain):
            os.close(descriptor)


def _timestamp(value):
    return datetime.fromtimestamp(value.st_mtime, timezone.utc).isoformat()


def _file_entry(descriptor, metadata, root, local_path, relative):
    digest = hashlib.sha256()
    with os.fdopen(os.dup(descriptor), 'rb') as stream:
        for block in iter(lambda: stream.read(_HASH_CHUNK), b''):
            digest.update(block)
    _unchanged(metadata, os.fstat(descriptor))
    return SourceEntry(relative_path=relative, kind='IDC', size=metadata.st_size,
                       sha256=digest.hexdigest(), last_modified=_timestamp(metadata),
                       local_root=str(root), local_path=local_path,
                       mtime_ns=metadata.st_mtime_ns, ctime_ns=metadata.st_ctime_ns,
                       inode=metadata.st_ino, device=metadata.st_dev)


def _walk(descriptor, metadata, root, local_path, relative, on_file):
    entries = []
    with os.scandir(descriptor) as listing:
        names = sorted(item.name for item in listing)
    for name in names:
        safe_relative(name)
        child, child_metadata = _child(descriptor, name)
        child_path = f'{local_path}/{name}' if local_path else name
        child_relative = f'{relative}/{name}' if relative else name
        try:
            entry = None
            if stat.S_ISDIR(child_metadata.st_mode):
                entries.extend(_walk(child, child_metadata, root, child_path, child_relative, on_file))
            else:
                entry = _file_entry(child, child_metadata, root, child_path, child_relative)
                entries.append(entry)
            _unchanged(child_metadata, os.stat(name, dir_fd=descriptor, follow_symlinks=False))
            if entry is not None:
                on_file(entry)
        finally:
            os.close(child)
    _unchanged(metadata, os.fstat(descriptor))
    return entries


def scan_idc(root: Path, relative_path: str, verification: str, discovered=None) -> list[SourceEntry]:
    """Scan files under a selection; file selections retain their basename."""
    relative_path = safe_relative(relative_path, allow_empty=True)
    if verification not in ('CONTENT', 'METADATA'):
        raise SyncError('INVALID_VERIFICATION')
    root = Path(os.path.abspath(root))
    totals = (0, 0)

    def on_file(entry):
        nonlocal totals
        totals = (totals[0] + 1, totals[1] + entry.size)
        if discovered is not None:
            discovered(*totals)

    with _node(root, relative_path) as (descriptor, metadata):
        if stat.S_ISDIR(metadata.st_mode):
            return _walk(descriptor, metadata, root, relative_path, '', on_file)
        entry = _file_entry(descriptor, metadata, root, relative_path,
                            relative_path.rsplit('/', 1)[-1])
        on_file(entry)
        return [entry]


def _entry_matches(entry, metadata):
    expected = (entry.device, entry.inode, stat.S_IFREG, entry.size,
                entry.mtime_ns, entry.ctime_ns)
    if expected != _metadata(metadata):
        raise SyncError('SOURCE_CHANGED')


def _verify_content(entry, descriptor):
    # Some mounted filesystems preserve coarse timestamps across rewrites.
    # pread leaves the caller's stream offset unchanged, including after seeks.
    _entry_matches(entry, os.fstat(descriptor))
    digest = hashlib.sha256()
    offset = 0
    while True:
        block = os.pread(descriptor, _HASH_CHUNK, offset)
        if not block:
            break
        digest.update(block)
        offset += len(block)
    _entry_matches(entry, os.fstat(descriptor))
    if offset != entry.size or digest.hexdigest() != entry.sha256:
        raise SyncError('SOURCE_CHANGED')


@contextmanager
def open_verified(entry: SourceEntry, verify_content: bool = True):
    """Verify a file around use; metadata-only mode is for transfer prechecks."""
    if entry.kind != 'IDC' or not entry.local_root:
        raise SyncError('INVALID_SOURCE')
    path = safe_relative(entry.local_path)
    with _node(Path(entry.local_root), path) as (descriptor, metadata):
        _entry_matches(entry, metadata)
        if verify_content:
            _verify_content(entry, descriptor)
        with os.fdopen(os.dup(descriptor), 'rb') as stream:
            yield stream
            if verify_content:
                _verify_content(entry, descriptor)
            else:
                _entry_matches(entry, os.fstat(descriptor))


def _cursor_after(token, binding):
    if not token:
        return ''
    if not isinstance(token, str) or len(token) > 2048:
        raise SyncError('INVALID_CURSOR')
    try:
        payload = json.loads(base64.b64decode(token.encode('ascii'), altchars=b'-_', validate=True))
        if not isinstance(payload, dict) or set(payload) != {'after', 'binding'}:
            raise ValueError()
        if payload['binding'] != binding:
            raise ValueError()
        after = safe_relative(payload['after'])
        if '/' in after:
            raise ValueError()
        return after
    except (ValueError, TypeError, KeyError, binascii.Error, SyncError):
        raise SyncError('INVALID_CURSOR') from None


def _browse_names(descriptor, after):
    with os.scandir(descriptor) as listing:
        for item in listing:
            safe_relative(item.name)
            _supported(os.stat(item.name, dir_fd=descriptor, follow_symlinks=False))
            if item.name > after:
                yield item.name


def _browse_entry(descriptor, path, name):
    metadata = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
    _supported(metadata)
    directory = stat.S_ISDIR(metadata.st_mode)
    return {'name': name, 'relativePath': f'{path}/{name}' if path else name,
            'kind': 'directory' if directory else 'file',
            'sizeBytes': 0 if directory else metadata.st_size,
            'modifiedAt': _timestamp(metadata)}


def browse_idc(root: Path, path: str, token: str = '', limit: int = 500):
    """Return one directory page with bounded memory and a snapshot-bound cursor."""
    path = safe_relative(path, allow_empty=True)
    if type(limit) is not int or not 1 <= limit <= 1000:
        raise SyncError('INVALID_LIMIT')
    root = Path(os.path.abspath(root))
    with _node(root, path) as (descriptor, metadata):
        if not stat.S_ISDIR(metadata.st_mode):
            raise SyncError('NOT_A_DIRECTORY')
        binding = canonical_digest({'root': str(root), 'path': path, 'stat': _metadata(metadata)})
        after = _cursor_after(token, binding)
        names = heapq.nsmallest(limit + 1, _browse_names(descriptor, after))
        entries = [_browse_entry(descriptor, path, name) for name in names[:limit]]
        cursor = ''
        if len(names) > limit:
            payload = json.dumps({'after': names[limit - 1], 'binding': binding},
                                 ensure_ascii=False, separators=(',', ':')).encode()
            cursor = base64.urlsafe_b64encode(payload).decode('ascii')
        return entries, cursor
