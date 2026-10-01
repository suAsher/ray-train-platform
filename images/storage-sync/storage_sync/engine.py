"""Transport-independent scan, planning, verification, and serial transfer."""
from dataclasses import asdict, replace
from contextlib import ExitStack
import hashlib
import math
from pathlib import Path

from .checkpoint import load_json, save_json
from .filesystem import open_verified
from .model import ObjectInfo, Plan, PlanEntry, SourceEntry, StopRequested, SyncError, canonical_digest, safe_relative


def hash_stream(stream):
    digest = hashlib.sha256()
    for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b''):
        digest.update(chunk)
    return digest.hexdigest()


def list_objects(reader, bucket, prefix):
    token = ''
    seen = set()
    while True:
        entries, following = reader.list_page(bucket, prefix, token, 1000)
        for entry in entries:
            if not entry.key.startswith(prefix):
                raise SyncError('LIST_SCOPE_VIOLATION')
            yield entry
        if not following:
            break
        if following in seen:
            raise SyncError('LIST_PAGINATION_STALLED')
        seen.add(following)
        token = following


def scan_tos(reader, bucket, key, is_directory, verification='METADATA', discovered=None):
    key = safe_relative(key, allow_empty=is_directory)
    prefix = key.rstrip('/') + '/' if key else ''
    objects = list_objects(reader, bucket, prefix) if is_directory else [reader.head(bucket, key)]
    result = []
    discovered_bytes = 0
    for listed in objects:
        if listed is None:
            raise SyncError('SOURCE_NOT_FOUND')
        if listed.key.endswith('/') and listed.size == 0:
            continue  # TOS directory marker is not a source file.
        obj = reader.head(bucket, listed.key)
        if obj is None or obj.etag != listed.etag:
            raise SyncError('SOURCE_CHANGED')
        digest = obj.sha256
        if verification == 'CONTENT' or not (obj.sha256 or obj.crc64):
            with reader.read(bucket, obj) as stream:
                digest = hash_stream(stream)
        relative = obj.key[len(prefix):] if is_directory else obj.key.rsplit('/', 1)[-1]
        safe_relative(relative)
        result.append(SourceEntry(relative_path=relative, kind='TOS', bucket=bucket,
                                  **{**asdict(obj), 'sha256': digest}))
        discovered_bytes += obj.size
        if discovered:
            discovered(len(result), discovered_bytes)
    return sorted(result, key=lambda entry: entry.relative_path)


def same_content(source, target):
    if target is None or source.size != target.size:
        return False
    if source.sha256 and target.sha256:
        return source.sha256 == target.sha256
    return bool(source.crc64 and target.crc64 and source.crc64 == target.crc64)


def make_plan(entries, reader, bucket, prefix, mode='INCREMENTAL', baseline=None,
              policy='UPDATE', layout='CONTENTS', source_name='', verification='METADATA', target_is_file=False):
    prefix = safe_relative(prefix, allow_empty=True)
    if layout in ('KEEP_DIRECTORY', 'DIRECTORY'):
        prefix = '/'.join(filter(None, (prefix, safe_relative(source_name))))
    selected = []
    target_keys = set()
    if target_is_file and len(entries) != 1:
        raise SyncError('INVALID_FILE_MAPPING')
    for source in entries:
        key = prefix if target_is_file else '/'.join(filter(None, (prefix, safe_relative(source.relative_path))))
        if key in target_keys:
            raise SyncError('DUPLICATE_TARGET')
        target_keys.add(key)
        target = reader.head(bucket, key)
        previous = (baseline or {}).get(key)
        unchanged = (previous is not None and previous.source.fingerprint == source.fingerprint
                     and target is not None and previous.target is not None
                     and previous.target.fingerprint == target.fingerprint
                     and (not source.sha256 or source.sha256 == previous.source.sha256))
        if target and verification == 'CONTENT':
            with reader.read(bucket, target) as stream:
                target = replace(target, sha256=hash_stream(stream))
            # Actual bytes decide content-mode reuse, even when metadata matches.
            unchanged = False
        elif unchanged and previous.target.sha256:
            target = replace(target, sha256=previous.target.sha256)
        elif target and source.sha256 and not target.sha256 and not (source.crc64 and target.crc64):
            # Establish a trusted baseline for IDC files against existing TOS objects.
            with reader.read(bucket, target) as stream:
                target = replace(target, sha256=hash_stream(stream))
        equal = same_content(source, target)
        if target and not equal and not unchanged and policy in ('FAIL', 'FAIL_IF_DIFFERENT'):
            raise SyncError('TARGET_CONFLICT')
        action = 'REUSE' if mode == 'INCREMENTAL' and (unchanged or equal) else 'COPY'
        selected.append(PlanEntry(source, key, target, action))
    extras = [] if target_is_file else [asdict(obj) for obj in list_objects(reader, bucket, prefix + '/' if prefix else '')
                                      if obj.key not in target_keys and not (obj.key.endswith('/') and obj.size == 0)]
    return Plan(tuple(selected), bucket, len(extras), canonical_digest(extras), verification)


def validate_plan_capabilities(plan, writer):
    for item in plan.entries:
        if item.action == 'COPY' and item.target and not getattr(writer, 'atomic_multipart_replace', True):
            if item.source.size > getattr(writer, 'max_single_size', 5 * 1024 ** 3):
                raise SyncError('UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE')


def verify_object(source, reader, bucket, target, verification='METADATA'):
    if target is None or source.size != target.size:
        raise SyncError('CHECKSUM_MISMATCH')
    if verification != 'CONTENT' and same_content(source, target):
        return target
    if not source.sha256:
        raise SyncError('CHECKSUM_UNAVAILABLE')
    with reader.read(bucket, target) as stream:
        if hash_stream(stream) != source.sha256:
            raise SyncError('CHECKSUM_MISMATCH')
    return replace(target, sha256=source.sha256)


def verify_source(source, reader):
    if source.kind == 'IDC':
        with open_verified(source, verify_content=False):
            return
    current = reader.head(source.bucket, source.key)
    if current is None or current.etag != source.etag or (source.version_id and current.version_id != source.version_id):
        raise SyncError('SOURCE_CHANGED')


def _binding(item, bucket, run_id, config_digest, part_size):
    return canonical_digest({'run': run_id, 'config': config_digest, 'entry': asdict(item), 'bucket': bucket, 'partSize': part_size})


def _check_control(control, writer, bucket, item, path, checkpoint):
    command = control()
    if command not in ('PAUSE', 'CANCEL'):
        return
    if command == 'CANCEL' and checkpoint and checkpoint.get('uploadId') and not checkpoint.get('completed'):
        writer.abort_upload(bucket, item.target_key, checkpoint['uploadId'])
        save_json(path, {**checkpoint, 'uploadId': '', 'parts': {}, 'cancelled': True})
    raise StopRequested(command)


def _multipart(item, reader, writer, bucket, path, checkpoint, part_size, control, part_done):
    source = item.source
    upload_id = checkpoint.get('uploadId')
    if not upload_id:
        upload_id = writer.create_upload(bucket, item.target_key, item.target, {'sha256': source.sha256, 'source': source.object_info()})
        checkpoint = {**checkpoint, 'uploadId': upload_id, 'parts': {}}
        save_json(path, checkpoint)
    try:
        remote = writer.list_parts(bucket, item.target_key, upload_id)
    except SyncError as error:
        if str(error) != 'MULTIPART_NOT_FOUND':
            raise
        checkpoint = {**checkpoint, 'uploadId': '', 'parts': {}}
        save_json(path, checkpoint)
        return _multipart(item, reader, writer, bucket, path, checkpoint, part_size, control, part_done)
    parts = dict(checkpoint.get('parts', {}))
    with ExitStack() as opened:
        stream = opened.enter_context(open_verified(source)) if source.kind == 'IDC' else None
        for index in range(math.ceil(source.size / part_size)):
            _check_control(control, writer, bucket, item, path, checkpoint)
            number, offset = index + 1, index * part_size
            length = min(part_size, source.size - offset)
            known = parts.get(str(number))
            if known and remote.get(number) == known and known['size'] == length:
                part_done(length, False)
                continue
            verify_source(source, reader)
            if source.kind == 'TOS':
                part = writer.copy_part(source.bucket, source.object_info(), bucket, item.target_key, upload_id, number, offset, length)
            else:
                stream.seek(offset)
                part = writer.upload_part(bucket, item.target_key, upload_id, number, stream, length)
            parts = {**parts, str(number): part}
            checkpoint = {**checkpoint, 'parts': parts}
            save_json(path, checkpoint)
            part_done(length, source.kind == 'IDC')
    _check_control(control, writer, bucket, item, path, checkpoint)
    verify_source(source, reader)
    result = writer.complete_upload(bucket, item.target_key, upload_id, {int(k): v for k, v in parts.items()}, item.target)
    return result, checkpoint


def _transfer(item, reader, writer, bucket, path, checkpoint, part_size, control, part_done):
    source = item.source
    multipart = source.size > part_size
    if item.target and not getattr(writer, 'atomic_multipart_replace', True):
        multipart = False
    if multipart:
        return _multipart(item, reader, writer, bucket, path, checkpoint, part_size, control, part_done)
    verify_source(source, reader)
    if source.kind == 'TOS':
        result = writer.copy(source.bucket, source.object_info(), bucket, item.target_key, item.target)
    else:
        with open_verified(source) as stream:
            result = writer.put(bucket, item.target_key, stream, item.target, source.sha256)
        part_done(source.size, True)
    return result, checkpoint


def execute(plan, reader, writer, work_dir, run_id, config_digest, part_size=64 * 1024 * 1024,
            control=lambda: '', report=lambda value: None, file_result=lambda value: None):
    validate_plan_capabilities(plan, writer)
    work_dir = Path(work_dir)
    totals = {'sourceFiles': len(plan.entries), 'sourceBytes': sum(item.source.size for item in plan.entries),
              'pendingBytes': plan.pending_bytes, 'transferBytes': plan.pending_bytes,
              'transferFiles': len(plan.entries) - plan.reused_files, 'reusedFiles': plan.reused_files,
              'reusedBytes': sum(item.source.size for item in plan.entries if item.action == 'REUSE'),
              'completedFiles': 0, 'completedBytes': 0, 'verifiedFiles': 0, 'verifiedBytes': 0,
              'targetExtraFiles': plan.extra_files, 'failedFiles': 0, 'networkBytes': 0, 'inFlightBytes': 0,
              'sequence': 0, 'status': 'RUNNING', 'phase': 'TRANSFERRING', 'scanComplete': True}
    def emit(**changes):
        nonlocal totals
        totals = {**totals, **changes, 'sequence': totals['sequence'] + 1}
        report(dict(totals))
    def part_done(length, network=True):
        emit(networkBytes=totals['networkBytes'] + (length if network else 0),
             inFlightBytes=totals['inFlightBytes'] + length)
    emit()
    completed = []
    for item in plan.entries:
        source = item.source
        path = work_dir / 'objects' / (canonical_digest({'bucket': plan.bucket, 'key': item.target_key}) + '.json')
        binding = _binding(item, plan.bucket, run_id, config_digest, part_size)
        checkpoint = load_json(path) or {'binding': binding, 'runId': run_id, 'parts': {}}
        if checkpoint['binding'] != binding:
            raise SyncError('CHECKPOINT_MISMATCH')
        _check_control(control, writer, plan.bucket, item, path, checkpoint)
        verify_source(source, reader)
        if source.kind == 'IDC' and (checkpoint.get('completed') or item.action == 'REUSE'):
            with open_verified(source):
                pass
        if checkpoint.get('completed'):
            current = reader.head(plan.bucket, item.target_key)
            if current is None or current.etag != checkpoint['target']['etag']:
                raise SyncError('TARGET_CHANGED')
            result = verify_object(source, reader, plan.bucket, current, plan.verification)
        elif item.action == 'REUSE':
            current = reader.head(plan.bucket, item.target_key)
            if current is None or current.etag != item.target.etag:
                raise SyncError('TARGET_CHANGED')
            if current.fingerprint == item.target.fingerprint and item.target.sha256:
                current = replace(current, sha256=item.target.sha256)
            result = verify_object(source, reader, plan.bucket, current, plan.verification)
        else:
            save_json(path, checkpoint)
            try:
                result, checkpoint = _transfer(item, reader, writer, plan.bucket, path, checkpoint, part_size, control, part_done)
                verify_source(source, reader)
                emit(phase='VERIFYING')
                result = verify_object(source, reader, plan.bucket, result, plan.verification)
            except StopRequested:
                raise
            except SyncError as error:
                file_result({'relativePath': source.relative_path, 'state': 'FAILED', 'sizeBytes': source.size, 'errorCode': str(error)})
                emit(failedFiles=totals['failedFiles'] + 1)
                raise
        save_json(path, {**checkpoint, 'completed': True, 'target': asdict(result)})
        completed.append(replace(item, target=result))
        file_result({'relativePath': source.relative_path, 'state': 'REUSED' if item.action == 'REUSE' else 'VERIFIED', 'sizeBytes': source.size})
        emit(completedFiles=totals['completedFiles'] + (item.action == 'COPY'),
             completedBytes=totals['completedBytes'] + (source.size if item.action == 'COPY' else 0),
             verifiedFiles=totals['verifiedFiles'] + 1, verifiedBytes=totals['verifiedBytes'] + source.size,
             phase='TRANSFERRING', inFlightBytes=0)
        _check_control(control, writer, plan.bucket, item, path, {**checkpoint, 'completed': True})
    save_json(work_dir / 'baseline.json', asdict(replace(plan, entries=tuple(completed))))
    emit(status='SUCCEEDED', phase='VERIFYING')
    return totals
