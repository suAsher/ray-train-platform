"""Entrypoint: read-only browse/preview and a separately credentialed transfer."""
import argparse
from dataclasses import asdict
import json
import os
from pathlib import Path
import signal

from .checkpoint import load_json, save_json
from .engine import execute, make_plan, scan_tos, validate_plan_capabilities
from .filesystem import scan_idc
from .model import SyncError, StopRequested, canonical_digest, plan_from_dict, safe_relative
from .tos_adapter import TOSStore, from_config
from .transport import ReadGateway, Reporter, _json_request


def _source_root(source):
    return Path('/data/source') / safe_relative(source['spaceId'])


def effective_bandwidth(spec):
    cap = int(os.environ.get('STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND', '0'))
    requested = int(spec.get('config', {}).get('bandwidthBytesPerSecond', 0))
    value = min(cap, requested) if cap and requested else cap or requested
    if value < 0 or (value and value < 102400):
        raise SyncError('INVALID_BANDWIDTH_LIMIT')
    return value


def _scan(spec, reader, reporter):
    effective_bandwidth(spec)
    plans = []
    identities = set()
    discovered = 0
    for index, mapping in enumerate(spec['mappings']):
        source, destination = mapping['source'], mapping['destination']
        if reporter.control():
            raise StopRequested(reporter.control())
        verification = spec['config'].get('verification', 'METADATA')
        def discovered_file(count, size):
            reporter.update(progress={'discoveredFiles': discovered + count, 'scanComplete': False})
            if reporter.control():
                raise StopRequested(reporter.control())
        if source['kind'] == 'IDC':
            entries = scan_idc(_source_root(source), source.get('relativePath', ''), verification, discovered=discovered_file)
            name = source.get('relativePath', '').rstrip('/').rsplit('/', 1)[-1]
            # A single selected file already carries its basename.
            is_directory = not (len(entries) == 1 and entries[0].local_path == source.get('relativePath', '').rstrip('/'))
        else:
            key = source.get('prefix', '').rstrip('/')
            is_directory = reader.head(source['bucket'], key) is None if key else True
            entries = scan_tos(reader, source['bucket'], key, is_directory, verification, discovered=discovered_file)
            name = key.rsplit('/', 1)[-1]
        discovered += len(entries)
        reporter.update(progress={'discoveredFiles': discovered, 'scanComplete': False})
        baseline = None
        if spec.get('baselineRef'):
            baseline_path = Path(spec['baselineRef'])
            if baseline_path.parent != Path('/work') or not baseline_path.name.startswith('ssr-'):
                raise SyncError('INVALID_BASELINE_REFERENCE')
            saved = load_json(baseline_path / ('mapping-' + str(index)) / 'baseline.json')
            if saved:
                baseline_plan = plan_from_dict(saved)
                if baseline_plan.bucket == destination['bucket']:
                    baseline = {item.target_key: item for item in baseline_plan.entries}
        plan = make_plan(entries, reader, destination['bucket'], destination['prefix'],
                         mode=spec['config'].get('mode', 'INCREMENTAL'),
                         baseline=baseline,
                         policy=spec['config'].get('conflictPolicy', 'UPDATE'),
                         layout='CONTENTS',  # Controller already resolved layout into final destination prefix.
                         source_name=name, verification=verification,
                         target_is_file=not is_directory and mapping.get('layout', 'DIRECTORY') == 'DIRECTORY')
        # SDK capabilities are statically known; planning never initializes credentials.
        validate_plan_capabilities(plan, TOSStore)
        for item in plan.entries:
            identity = (plan.bucket, item.target_key)
            if identity in identities:
                raise SyncError('DUPLICATE_TARGET')
            identities.add(identity)
        plans.append(plan)
    return plans


def manifest_summary(plans):
    manifest = {'plans': [asdict(plan) for plan in plans]}
    return manifest, {'manifestDigest': canonical_digest(manifest),
                      'sourceFingerprint': canonical_digest([plan.source_fingerprint for plan in plans]),
                      'targetFingerprint': canonical_digest([plan.target_fingerprint for plan in plans])}


def plan_progress(plan):
    return {'discoveredFiles': len(plan.entries), 'sourceFiles': len(plan.entries),
            'sourceBytes': sum(item.source.size for item in plan.entries),
            'transferFiles': len(plan.entries) - plan.reused_files, 'transferBytes': plan.pending_bytes,
            'reusedFiles': plan.reused_files,
            'reusedBytes': sum(item.source.size for item in plan.entries if item.action == 'REUSE'),
            'targetExtraFiles': plan.extra_files, 'scanComplete': True}


def _preview(spec, reader, reporter, work_dir):
    plans = _scan(spec, reader, reporter)
    manifest, summary = manifest_summary(plans)
    for field in ('manifestDigest', 'sourceFingerprint', 'targetFingerprint'):
        if spec.get(field) and spec[field] != summary[field]:
            raise SyncError('PREVIEW_SNAPSHOT_CHANGED')
    save_json(work_dir / 'manifest.json', manifest)
    progress = {'sourceFiles': sum(len(plan.entries) for plan in plans),
                'sourceBytes': sum(item.source.size for plan in plans for item in plan.entries),
                'transferFiles': sum(len(plan.entries) - plan.reused_files for plan in plans),
                'transferBytes': sum(plan.pending_bytes for plan in plans),
                'reusedFiles': sum(plan.reused_files for plan in plans),
                'reusedBytes': sum(item.source.size for plan in plans for item in plan.entries if item.action == 'REUSE'),
                'targetExtraFiles': sum(plan.extra_files for plan in plans), 'scanComplete': True}
    progress['discoveredFiles'] = progress['sourceFiles']
    reporter.update(**summary, progress=progress,
                    mappingProgress=[{'mappingIndex': index, 'progress': plan_progress(plan)} for index, plan in enumerate(plans)],
                    files={'path': str(work_dir / 'manifest.json'), 'digest': summary['manifestDigest'], 'count': progress['sourceFiles']})


def _browse(spec, reader, reporter):
    from .filesystem import browse_idc
    source = spec['mappings'][0]['source']
    limit = min(max(int(spec.get('limit', 200)), 1), 1000)
    if source['kind'] == 'IDC':
        entries, cursor = browse_idc(_source_root(source), source.get('relativePath', ''), spec.get('cursor', ''), limit)
    else:
        # Root gateway scopes prefix and supports one-level list for browser only.
        result = reader.request('browse', bucket=source['bucket'], prefix=source['prefix'], continuationToken=spec.get('cursor', ''), limit=limit)
        entries, cursor = result['entries'], result.get('nextToken', '')
    reporter.update(browseEntries=entries, nextCursor=cursor)


def _transfer(spec, reporter, work_dir, config_path):
    manifest = load_json(work_dir / 'manifest.json')
    if not manifest or canonical_digest(manifest) != spec.get('manifestDigest'):
        raise SyncError('MANIFEST_MISMATCH')
    plans = [plan_from_dict(item) for item in manifest['plans']]
    bandwidth = effective_bandwidth(spec)
    store = from_config(config_path, os.environ.get('STORAGE_SYNC_TOS_ENDPOINT', ''), os.environ.get('STORAGE_SYNC_TOS_REGION', ''), bandwidth)
    reporter.writer = store
    fixed = {'sourceFiles': sum(len(plan.entries) for plan in plans),
             'sourceBytes': sum(item.source.size for plan in plans for item in plan.entries),
             'transferFiles': sum(len(plan.entries) - plan.reused_files for plan in plans),
             'transferBytes': sum(plan.pending_bytes for plan in plans),
             'reusedFiles': sum(plan.reused_files for plan in plans),
             'reusedBytes': sum(item.source.size for plan in plans for item in plan.entries if item.action == 'REUSE'),
             'targetExtraFiles': sum(plan.extra_files for plan in plans)}
    aggregate = {}
    mapping_progress = [{'mappingIndex': index, 'progress': plan_progress(plan)} for index, plan in enumerate(plans)]
    fields = ('completedFiles', 'completedBytes', 'verifiedFiles', 'verifiedBytes', 'failedFiles', 'networkBytes')
    for index, plan in enumerate(plans):
        prior = dict(aggregate)
        def progress(value):
            nonlocal aggregate, mapping_progress
            aggregate = {field: prior.get(field, 0) + value.get(field, 0) for field in fields}
            mapping_progress = [({'mappingIndex': index, 'progress': {**plan_progress(plan), **{field: value.get(field, 0) for field in fields}, 'inFlightBytes': value.get('inFlightBytes', 0)}}
                                 if item['mappingIndex'] == index else item) for item in mapping_progress]
            reporter.update(phase=value['phase'], progress={**fixed, **aggregate, 'scanComplete': True, 'inFlightBytes': value.get('inFlightBytes', 0)},
                            mappingProgress=mapping_progress)
        execute(plan, store, store, work_dir / ('mapping-' + str(index)), spec['runId'], spec['manifestDigest'],
                control=reporter.control, report=progress,
                file_result=lambda value: reporter.add_file({'mappingIndex': index, **value}))
    reporter.update(manifestDigest=spec['manifestDigest'], files={'path': str(work_dir / 'manifest.json'),
                    'digest': spec['manifestDigest'], 'count': fixed['sourceFiles']})


def _recover(spec, token, work_dir):
    expected = Path(spec.get('checkpointRef', ''))
    run_id = spec['runId']
    allowed = (Path('/work') / run_id, Path('/work/previews') / run_id)
    if expected not in allowed or work_dir != expected or '/' in run_id or not run_id.startswith(('ssr-', 'ssv-')):
        raise SyncError('INVALID_RECOVERY_REFERENCE')
    receipt = load_json(work_dir / 'result.json')
    if (not isinstance(receipt, dict) or any(receipt.get(field) != spec[field] for field in ('runId', 'attempt', 'generation'))
            or receipt.get('state') not in ('SUCCEEDED', 'FAILED', 'PAUSED', 'CANCELLED')
            or not receipt.get('workerId') or type(receipt.get('sequence')) is not int or receipt['sequence'] < 1
            or type(receipt.get('requestsDrained')) is not bool):
        raise SyncError('RECOVERY_RECEIPT_INVALID')
    _json_request(spec['callbackUrl'], token, receipt)


def run(spec, token, work_dir, config_path=None, reporter_factory=Reporter):
    work_dir = Path(work_dir)
    if spec.get('phase') == 'RECOVER':
        try:
            _recover(spec, token, work_dir)
            return 0
        except (SyncError, KeyError):
            return 2
    reporter = reporter_factory(spec, token, work_dir)
    reporter.writer = None
    # Duplicate Pods must not create even checkpoint files or read write credentials.
    try:
        reporter.claim()
    except SyncError:
        return 2
    old_handlers = {}
    def stop_handler(*_):
        reporter.command = reporter.command or 'PAUSE'
    for signum in (signal.SIGTERM, signal.SIGINT):
        old_handlers[signum] = signal.signal(signum, stop_handler)
    state, reason = 'SUCCEEDED', ''
    try:
        reporter.start()
        phase = spec['phase']
        if phase in ('PREVIEW', 'BROWSE'):
            reader = ReadGateway(spec['metadataUrl'], reporter)
            if phase == 'PREVIEW':
                _preview(spec, reader, reporter, work_dir)
            else:
                _browse(spec, reader, reporter)
        elif phase == 'TRANSFER' and config_path:
            _transfer(spec, reporter, work_dir, config_path)
        else:
            raise SyncError('INVALID_WORK_PHASE')
    except StopRequested as error:
        state = 'CANCELLED' if str(error) == 'CANCEL' else 'PAUSED'
    except SyncError as error:
        state, reason = 'FAILED', str(error)
    except Exception:
        state, reason = 'FAILED', 'WORKER_FAILED'
    finally:
        drained = not (reporter.writer and reporter.writer.uncertain_write)
        try:
            reporter.finish(state=state, failureReason=reason, requestsDrained=drained)
        except SyncError:
            # The final receipt was fsynced before callback; absence of ACK is not success.
            state = 'FAILED'
        for signum, handler in old_handlers.items():
            signal.signal(signum, handler)
    return 0 if state == 'SUCCEEDED' else 2


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--request', required=True, type=Path)
    parser.add_argument('--work-dir', required=True, type=Path)
    parser.add_argument('--callback-token-file', required=True, type=Path)
    parser.add_argument('--tos-config', type=Path)
    args = parser.parse_args()
    try:
        spec = json.loads(args.request.read_text())
        token = args.callback_token_file.read_text().strip()
        return run(spec, token, args.work_dir, args.tos_config)
    except Exception:
        # Never log arbitrary SDK exceptions, URLs, JSON config, or credentials.
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
