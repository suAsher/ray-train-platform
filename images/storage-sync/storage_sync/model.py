"""Immutable manifest types. ETags are identity guards, never content hashes."""
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
import hashlib
import json


class SyncError(RuntimeError):
    """A stable, credential-free error safe to return to the controller."""


class StopRequested(SyncError):
    pass


def canonical_digest(value):
    digest = hashlib.sha256()
    encoder = json.JSONEncoder(ensure_ascii=False, sort_keys=True, separators=(',', ':'))
    for piece in encoder.iterencode(value):
        digest.update(piece.encode())
    return digest.hexdigest()


def canonical_timestamp(value):
    if value is None or value == '':
        return ''
    try:
        parsed = value if isinstance(value, datetime) else datetime.fromisoformat(str(value).replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            raise ValueError()
        return parsed.astimezone(timezone.utc).isoformat(timespec='milliseconds').replace('+00:00', 'Z')
    except (TypeError, ValueError):
        raise SyncError('INVALID_OBJECT_METADATA') from None


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
    device: int = 0  # Legacy manifest field; new IDC snapshots use zero across mounts.
    content_type: str = ''
    content_encoding: str = ''
    content_disposition: str = ''
    content_language: str = ''
    cache_control: str = ''

    @property
    def fingerprint(self):
        data = {key: value for key, value in self.portable_snapshot().items()
                if key not in ('sha256', 'crc64')}
        return canonical_digest(data)

    def portable_snapshot(self):
        data = asdict(self)
        # Preserve the field when parsing old manifests; their raw digest and
        # checkpoint binding must remain valid. Portable identities ignore its value.
        return {**data, 'device': 0} if self.kind == 'IDC' else data

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
        data = asdict(self)
        entries = [{**item, 'source': entry.source.portable_snapshot()}
                   for item, entry in zip(data['entries'], self.entries)]
        return canonical_digest({**data, 'entries': entries})

    @property
    def source_fingerprint(self):
        return canonical_digest([entry.source.portable_snapshot() for entry in self.entries])

    @property
    def target_fingerprint(self):
        return canonical_digest({'entries': [(entry.target_key, asdict(entry.target) if entry.target else None) for entry in self.entries], 'extras': self.extra_fingerprint})


def plan_from_dict(data):
    return Plan(entries=tuple(PlanEntry(source=SourceEntry(**item['source']), target_key=item['target_key'], target=ObjectInfo(**item['target']) if item['target'] else None, action=item['action']) for item in data['entries']), bucket=data['bucket'], extra_files=data.get('extra_files', 0), extra_fingerprint=data.get('extra_fingerprint', ''), verification=data.get('verification', 'METADATA'))
