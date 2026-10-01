"""Immutable manifest types. ETags are identity guards, never content hashes."""
from dataclasses import asdict, dataclass
import hashlib
import json


class SyncError(RuntimeError):
    """A stable, credential-free error safe to return to the controller."""


class StopRequested(SyncError):
    pass


def canonical_digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def safe_relative(value, allow_empty=False):
    if not isinstance(value, str) or '\x00' in value or '\\' in value or value.startswith('/'):
        raise SyncError('INVALID_PATH')
    value = value.rstrip('/')
    if not value and allow_empty:
        return ''
    if not value or any(part in ('', '.', '..') for part in value.split('/')):
        raise SyncError('INVALID_PATH')
    return value


@dataclass(frozen=True)
class ObjectInfo:
    key: str
    size: int
    etag: str = ''
    version_id: str = ''
    sha256: str = ''
    crc64: str = ''
    last_modified: str = ''
    content_type: str = ''
    content_encoding: str = ''
    content_disposition: str = ''
    content_language: str = ''
    cache_control: str = ''

    @property
    def fingerprint(self):
        identity = asdict(self)
        identity.pop('sha256')
        return canonical_digest(identity)


@dataclass(frozen=True)
class SourceEntry:
    relative_path: str
    kind: str
    bucket: str = ''
    key: str = ''
    size: int = 0
    etag: str = ''
    version_id: str = ''
    sha256: str = ''
    crc64: str = ''
    last_modified: str = ''
    local_root: str = ''
    local_path: str = ''
    mtime_ns: int = 0
    ctime_ns: int = 0
    inode: int = 0
    device: int = 0
    content_type: str = ''
    content_encoding: str = ''
    content_disposition: str = ''
    content_language: str = ''
    cache_control: str = ''

    @property
    def fingerprint(self):
        data = asdict(self)
        data.pop('sha256')
        data.pop('crc64')
        return canonical_digest(data)

    def object_info(self):
        return ObjectInfo(**{key: getattr(self, key) for key in ObjectInfo.__dataclass_fields__})


@dataclass(frozen=True)
class PlanEntry:
    source: SourceEntry
    target_key: str
    target: ObjectInfo | None
    action: str = 'COPY'


@dataclass(frozen=True)
class Plan:
    entries: tuple[PlanEntry, ...]
    bucket: str
    extra_files: int = 0
    extra_fingerprint: str = ''
    verification: str = 'METADATA'

    @property
    def pending_bytes(self):
        return sum(item.source.size for item in self.entries if item.action == 'COPY')

    @property
    def reused_files(self):
        return sum(item.action == 'REUSE' for item in self.entries)

    @property
    def digest(self):
        return canonical_digest(asdict(self))

    @property
    def source_fingerprint(self):
        return canonical_digest([asdict(entry.source) for entry in self.entries])

    @property
    def target_fingerprint(self):
        return canonical_digest({'entries': [(entry.target_key, asdict(entry.target) if entry.target else None) for entry in self.entries], 'extras': self.extra_fingerprint})


def plan_from_dict(data):
    return Plan(entries=tuple(PlanEntry(source=SourceEntry(**item['source']), target_key=item['target_key'], target=ObjectInfo(**item['target']) if item['target'] else None, action=item['action']) for item in data['entries']), bucket=data['bucket'], extra_files=data.get('extra_files', 0), extra_fingerprint=data.get('extra_fingerprint', ''), verification=data.get('verification', 'METADATA'))
