"""Validated, opt-in Ray Data adapter for managed Ray Train jobs."""

from __future__ import annotations

import dataclasses
import concurrent.futures
import hashlib
import os
import pathlib
import posixpath
import re
import shutil
import stat
import sys
import time
import unicodedata
import urllib.parse
from collections.abc import Iterator, Mapping, Sequence
from typing import Any


_STABLE_INPUT_ROOT = "/mnt/data/input"
_SUPPORTED_FORMATS = frozenset(("parquet", "images", "files"))
_S1H_REF_COLUMNS = (
    "ordinal",
    "token",
    "class_ids",
    "source_digest",
    "split",
    "shard_path",
    "row_index",
)
_MAX_PREFETCH_BATCHES = 16
_MAX_MANIFEST_BYTES = 512 * 1024 * 1024
_SAFE_DATASET_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_DATASET_CACHE_POLICIES = frozenset(("off", "auto", "bounded"))


@dataclasses.dataclass(frozen=True)
class DatasetConfig:
    """Immutable registered-dataset reference below the governed input mount."""

    format: str
    uri: str

    def __post_init__(self) -> None:
        if self.format not in _SUPPORTED_FORMATS:
            raise ValueError("unsupported Ray Data format")
        if not self.uri or any(
            unicodedata.category(character) == "Cc" for character in self.uri
        ):
            raise ValueError(
                "Ray Data URI must not be empty or contain control characters"
            )
        if "\\" in self.uri:
            raise ValueError("Ray Data URI must use POSIX separators")
        parsed = urllib.parse.urlsplit(self.uri)
        if parsed.scheme or parsed.netloc or parsed.username or parsed.password:
            raise ValueError("Ray Data URI must not contain a scheme or credentials")
        if parsed.query or parsed.fragment:
            raise ValueError("Ray Data URI must not contain a query or fragment")
        prefix = _STABLE_INPUT_ROOT + "/"
        if self.uri != _STABLE_INPUT_ROOT and not self.uri.startswith(prefix):
            raise ValueError(f"Ray Data URI must stay below {_STABLE_INPUT_ROOT}")
        relative = "." if self.uri == _STABLE_INPUT_ROOT else self.uri[len(prefix) :]
        if any(segment == ".." for segment in relative.split("/")) or (
            relative != "." and any(segment == "." for segment in relative.split("/"))
        ):
            raise ValueError("Ray Data URI must not contain traversal segments")
        if (relative == "." and self.format != "files") or posixpath.normpath(self.uri) != self.uri:
            raise ValueError("Ray Data URI must be a canonical mounted path")


@dataclasses.dataclass(frozen=True)
class StreamingDatasetConfig:
    """Pinned immutable manifest selected by the platform before GPU admission."""

    dataset_id: str
    version_id: str
    manifest_path: str
    manifest_sha256: str
    dataset_root: str
    train_samples: int
    cache_policy: str
    prefetch_batches: int = 2
    shuffle_seed: int = 0

    def __post_init__(self) -> None:
        dataset_id = _validated_dataset_identifier("dataset ID", self.dataset_id)
        version_id = _validated_dataset_identifier("dataset version ID", self.version_id)
        if not isinstance(self.manifest_sha256, str) or not _SHA256.fullmatch(
            self.manifest_sha256
        ):
            raise ValueError("dataset manifest SHA-256 is invalid")
        root = _validated_absolute_root(self.dataset_root)
        manifest = pathlib.Path(self.manifest_path)
        if not manifest.is_absolute():
            raise ValueError("dataset manifest path must be absolute")
        resolved_manifest = manifest.resolve(strict=False)
        expected = (
            root / dataset_id / "manifests" / f"{version_id}.parquet"
        ).resolve(strict=False)
        if resolved_manifest != expected or str(manifest) != str(resolved_manifest):
            raise ValueError("dataset manifest path does not match pinned provenance")
        _validate_positive_integer("train_samples", self.train_samples)
        if self.cache_policy not in _DATASET_CACHE_POLICIES:
            raise ValueError("dataset cache policy is invalid")
        if (
            isinstance(self.prefetch_batches, bool)
            or not isinstance(self.prefetch_batches, int)
            or not 0 <= self.prefetch_batches <= _MAX_PREFETCH_BATCHES
        ):
            raise ValueError(
                f"prefetch_batches must be between 0 and {_MAX_PREFETCH_BATCHES}"
            )
        if (
            isinstance(self.shuffle_seed, bool)
            or not isinstance(self.shuffle_seed, int)
            or not 0 <= self.shuffle_seed <= (1 << 32) - 1
        ):
            raise ValueError("dataset shuffle seed must fit unsigned 32-bit")
        object.__setattr__(self, "dataset_root", str(root))
        object.__setattr__(self, "manifest_path", str(resolved_manifest))


def build_s1h_streaming_dataset(
    config: StreamingDatasetConfig,
    *,
    world_size: int,
    ray_data_module: Any | None = None,
) -> tuple[Any, int, int]:
    """Load only the pinned reference manifest and prepare equal worker shards."""

    if not isinstance(config, StreamingDatasetConfig):
        raise ValueError("streaming dataset config is invalid")
    _validate_positive_integer("world_size", world_size)
    _verify_manifest(config.manifest_path, config.manifest_sha256)
    if ray_data_module is None:
        from ray import data as ray_data_module

    dataset = ray_data_module.read_parquet(config.manifest_path)
    try:
        training = dataset.filter(lambda row: row["split"] == "train").sort(
            "ordinal"
        )
    except AttributeError:
        raise ValueError("streaming manifest requires a Ray Dataset-like value") from None
    return prepare_s1h_training_dataset(
        training,
        sample_count=config.train_samples,
        world_size=world_size,
    )


def _validated_dataset_identifier(name: str, value: object) -> str:
    if (
        not isinstance(value, str)
        or not _SAFE_DATASET_ID.fullmatch(value)
        or value in {".", ".."}
    ):
        raise ValueError(f"{name} is invalid")
    return value


def _validated_absolute_root(value: object) -> pathlib.Path:
    if not isinstance(value, str) or not value:
        raise ValueError("dataset root is invalid")
    root = pathlib.Path(value)
    if not root.is_absolute():
        raise ValueError("dataset root must be absolute")
    resolved = root.resolve(strict=False)
    if resolved == pathlib.Path(resolved.anchor) or str(root) != str(resolved):
        raise ValueError("dataset root must be a canonical non-root path")
    return resolved


def _verify_manifest(path_value: str, expected_digest: str) -> None:
    path = pathlib.Path(path_value)
    try:
        size = path.stat().st_size
    except OSError:
        raise ValueError("dataset manifest is unavailable") from None
    if size <= 0 or size > _MAX_MANIFEST_BYTES or not path.is_file():
        raise ValueError("dataset manifest size is invalid")
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
    except OSError:
        raise ValueError("dataset manifest is unavailable") from None
    if digest.hexdigest() != expected_digest:
        raise ValueError("dataset manifest integrity check failed")


def build_dataset(config: DatasetConfig) -> Any:
    """Build the registered Ray Dataset without loading Ray Data at import time."""

    from ray import data

    if config.format == "parquet":
        return data.read_parquet(config.uri)
    if config.format == "images":
        return data.read_images(config.uri, include_paths=True)
    if config.format == "files":
        return data.read_binary_files(config.uri, include_paths=True)
    raise ValueError("unsupported Ray Data format")


def worker_iterator(name: str = "train") -> Any:
    """Return prefetched Torch batches from a named Ray Train dataset shard."""

    from ray import train

    shard = train.get_dataset_shard(name)
    if shard is None:
        raise RuntimeError("Ray Data shard is unavailable")
    return shard.iter_torch_batches(prefetch_batches=2)


def _validate_positive_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise ValueError(f"{name} must be a positive integer")
    return value


def prepare_s1h_training_dataset(
    dataset: Any,
    *,
    sample_count: int,
    world_size: int,
) -> tuple[Any, int, int]:
    """Lazily pad publisher-ordered refs for Ray Train's equal split.

    The trusted publication receipt supplies ``sample_count``. At most
    ``world_size - 1`` refs are read from the beginning and appended, so the
    default equal worker split cannot discard a tail and every rank receives
    the same number of samples.
    """

    _validate_positive_integer("sample_count", sample_count)
    _validate_positive_integer("world_size", world_size)
    if sample_count < world_size:
        raise ValueError("sample_count must be greater than or equal to world_size")

    try:
        refs = dataset.select_columns(list(_S1H_REF_COLUMNS))
    except AttributeError:
        raise ValueError("S1H training requires a Ray Dataset-like value") from None

    padding_count = (-sample_count) % world_size
    if padding_count:
        refs = refs.union(refs.limit(padding_count))
    samples_per_worker = (sample_count + padding_count) // world_size
    return refs, samples_per_worker, padding_count


def worker_s1h_batches(
    *,
    samples_per_gpu: int,
    name: str = "train",
    prefetch_batches: int = 2,
    pipeline: Any | None = None,
    batch_resolver: Any | None = None,
) -> Iterator[list[Any]]:
    """Stream bounded, decoded S1H batches from this worker's Ray shard."""

    if (
        isinstance(samples_per_gpu, bool)
        or not isinstance(samples_per_gpu, int)
        or samples_per_gpu < 1
    ):
        raise ValueError("samples_per_gpu must be a positive integer")
    if (
        isinstance(prefetch_batches, bool)
        or not isinstance(prefetch_batches, int)
        or not 0 <= prefetch_batches <= _MAX_PREFETCH_BATCHES
    ):
        raise ValueError(
            f"prefetch_batches must be between 0 and {_MAX_PREFETCH_BATCHES}"
        )
    if pipeline is not None and not callable(pipeline):
        raise ValueError("pipeline must be callable")
    if batch_resolver is not None and not callable(batch_resolver):
        raise ValueError("batch_resolver must be callable")

    from ray import train
    from .s1h_dataset import iter_decoded_samples

    shard = train.get_dataset_shard(name)
    if shard is None:
        raise RuntimeError("Ray Data shard is unavailable")
    from .data_metrics import observe_data_metric

    raw_batches = iter(
        shard.iter_batches(
            batch_size=samples_per_gpu,
            batch_format="numpy",
            prefetch_batches=prefetch_batches,
        )
    )
    resolved_batch_resolver = batch_resolver
    while True:
        waiting_started = time.perf_counter()
        try:
            raw_batch = next(raw_batches)
        except StopIteration:
            break
        observe_data_metric(
            "dataset_prefetch_wait_seconds_total",
            max(0.0, time.perf_counter() - waiting_started),
        )
        if (
            resolved_batch_resolver is None
            and isinstance(raw_batch, Mapping)
            and set(raw_batch) == set(_S1H_REF_COLUMNS)
        ):
            from .s1h_parquet import resolver_from_environment

            try:
                resolved_batch_resolver = resolver_from_environment()
            except RuntimeError:
                raise RuntimeError(
                    "lightweight S1H shard requires an available platform batch resolver"
                ) from None
        decoded = list(
            iter_decoded_samples(
                _iter_s1h_batch_rows(
                    raw_batch,
                    batch_resolver=resolved_batch_resolver,
                ),
                pipeline=pipeline,
            )
        )
        if not decoded:
            continue
        if len(decoded) > samples_per_gpu:
            raise ValueError("Ray Data returned a batch larger than samples_per_gpu")
        observe_data_metric("dataset_batches_total", 1)
        observe_data_metric("dataset_samples_total", len(decoded))
        yield decoded


def worker_s1h_samples(
    *,
    samples_per_gpu: int,
    name: str = "train",
    prefetch_batches: int = 2,
    pipeline: Any | None = None,
    batch_resolver: Any | None = None,
) -> Iterator[Any]:
    """Flatten ``worker_s1h_batches`` while retaining its bounded buffering."""

    for batch in worker_s1h_batches(
        name=name,
        samples_per_gpu=samples_per_gpu,
        prefetch_batches=prefetch_batches,
        pipeline=pipeline,
        batch_resolver=batch_resolver,
    ):
        yield from batch


def _iter_s1h_batch_rows(
    batch: object,
    *,
    batch_resolver: Any | None,
) -> Iterator[dict[str, Any]]:
    from .s1h_dataset import ROW_FIELD_NAMES

    if not isinstance(batch, Mapping):
        raise ValueError("Ray Data batch fields do not match S1H Parquet v1")
    fields = set(batch)
    if fields == set(ROW_FIELD_NAMES):
        yield from _iter_numpy_batch_rows(batch, ROW_FIELD_NAMES)
        return
    if fields != set(_S1H_REF_COLUMNS):
        raise ValueError("Ray Data batch fields do not match S1H Parquet v1")
    if batch_resolver is None:
        from .s1h_parquet import resolver_from_environment

        try:
            batch_resolver = resolver_from_environment()
        except RuntimeError:
            raise RuntimeError(
                "lightweight S1H shard requires an available platform batch resolver"
            ) from None

    refs = tuple(_iter_numpy_batch_rows(batch, _S1H_REF_COLUMNS))
    yield from _resolve_s1h_ref_batch(refs, batch_resolver)


def _resolve_s1h_ref_batch(
    refs: tuple[dict[str, Any], ...],
    batch_resolver: Any,
) -> tuple[dict[str, Any], ...]:
    try:
        resolved_rows = batch_resolver(tuple(dict(ref) for ref in refs))
    except Exception:
        raise RuntimeError("platform S1H batch resolver failed") from None
    if not isinstance(resolved_rows, Sequence) or isinstance(
        resolved_rows,
        (str, bytes, bytearray),
    ):
        raise ValueError("platform S1H batch resolver must return a row sequence")
    try:
        resolved_count = len(resolved_rows)
    except Exception:
        raise ValueError(
            "platform S1H batch resolver returned an invalid row sequence"
        ) from None
    if resolved_count != len(refs):
        raise ValueError("platform S1H batch resolver returned an invalid row count")

    normalized = []
    for index, ref in enumerate(refs):
        try:
            resolved = resolved_rows[index]
            matches_ref = isinstance(resolved, Mapping) and _resolved_row_matches_ref(
                resolved,
                ref,
            )
        except Exception:
            raise ValueError(
                "platform S1H batch resolver returned an invalid row sequence"
            ) from None
        if not matches_ref:
            raise ValueError("resolved S1H payload does not match its sample ref")
        try:
            normalized.append(dict(resolved))
        except Exception:
            raise ValueError(
                "platform S1H batch resolver returned an invalid row sequence"
            ) from None
    return tuple(normalized)


def _resolved_row_matches_ref(
    row: Mapping[str, Any],
    ref: Mapping[str, Any],
) -> bool:
    try:
        row_token = row["token"]
        ref_token = ref["token"]
        row_digest = row["source_digest"]
        ref_digest = ref["source_digest"]
        if not all(
            isinstance(value, str)
            for value in (row_token, ref_token, row_digest, ref_digest)
        ):
            return False
        row_classes = tuple(row["class_ids"])
        ref_classes = tuple(ref["class_ids"])
        return (
            row_token == ref_token
            and row_digest == ref_digest
            and row_classes == ref_classes
        )
    except (KeyError, TypeError, ValueError):
        return False


def _iter_numpy_batch_rows(
    batch: Mapping[str, Any],
    field_names: tuple[str, ...],
) -> Iterator[dict[str, Any]]:
    lengths = []
    for name in field_names:
        try:
            lengths.append(len(batch[name]))
        except (KeyError, TypeError):
            raise ValueError("Ray Data batch columns must be row sequences") from None
    if len(set(lengths)) != 1:
        raise ValueError("Ray Data batch columns have inconsistent lengths")
    for index in range(lengths[0]):
        yield {name: batch[name][index] for name in field_names}


@dataclasses.dataclass(frozen=True)
class StageSummary:
    path: pathlib.Path
    files: int
    bytes: int
    seconds: float
    reused: bool


def _relative_file(path_value: str, source_root: pathlib.Path) -> pathlib.Path:
    candidate = pathlib.Path(path_value).resolve(strict=False)
    try:
        relative = candidate.relative_to(source_root)
    except ValueError:
        raise ValueError("Ray Data file escaped selected input") from None
    if not relative.parts or relative.is_absolute() or ".." in relative.parts:
        raise ValueError("Ray Data file path is unsafe")
    return relative


def _cache_index(relative: pathlib.Path, root_count: int) -> int:
    digest = hashlib.sha256(relative.as_posix().encode("utf-8")).digest()
    return int.from_bytes(digest[:8], "big") % root_count


def _write_staged_file(
    relative: pathlib.Path,
    payload: bytes,
    cache_roots: tuple[pathlib.Path, ...],
    temporary_view: pathlib.Path,
) -> int:
    root = cache_roots[_cache_index(relative, len(cache_roots))]
    destination = root / "data" / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.{os.getpid()}.{time.time_ns()}.tmp")
    try:
        with temporary.open("xb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)
    link = temporary_view / relative
    link.parent.mkdir(parents=True, exist_ok=True)
    link.symlink_to(destination)
    return len(payload)


def _validated_stage_roots(cache_paths: str) -> tuple[pathlib.Path, ...]:
    supplied = tuple(
        pathlib.Path(value)
        for value in cache_paths.split(":")
        if value.strip()
    )
    if not supplied or any(not root.is_absolute() for root in supplied):
        raise ValueError("Ray Data staging requires absolute cache paths")
    normalized = []
    for root in supplied:
        try:
            root.mkdir(parents=True, exist_ok=True)
            metadata = root.lstat()
            if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
                raise ValueError
            resolved = root.resolve(strict=True)
            if resolved == pathlib.Path(resolved.anchor):
                raise ValueError
        except (OSError, ValueError):
            raise ValueError("Ray Data staging cache path is unsafe") from None
        normalized.append(resolved)
    roots = tuple(normalized)
    if len(set(roots)) != len(roots):
        raise ValueError("Ray Data staging requires distinct cache paths")
    return roots


def _prepare_stage_data_root(root: pathlib.Path) -> None:
    data = root / "data"
    try:
        data.mkdir(parents=True, exist_ok=True)
        metadata = data.lstat()
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
            raise ValueError
        if data.resolve(strict=True).parent != root:
            raise ValueError
    except (OSError, ValueError):
        raise ValueError("Ray Data staging cache path is unsafe") from None


def _remove_stage_directory(path: pathlib.Path, root: pathlib.Path) -> None:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        return
    except OSError:
        raise ValueError("Ray Data staging path is unsafe") from None
    if (
        stat.S_ISLNK(metadata.st_mode)
        or not stat.S_ISDIR(metadata.st_mode)
        or path.parent.resolve(strict=True) != root
    ):
        raise ValueError("Ray Data staging path is unsafe")
    try:
        shutil.rmtree(path)
    except OSError:
        raise ValueError("Ray Data staging path is unsafe") from None


def _validate_stage_reuse_entries(
    marker: pathlib.Path,
    view: pathlib.Path,
    root: pathlib.Path,
) -> bool:
    for candidate, expected_mode in (
        (marker, stat.S_ISREG),
        (view, stat.S_ISDIR),
    ):
        try:
            metadata = candidate.lstat()
        except FileNotFoundError:
            return False
        except OSError:
            raise ValueError("Ray Data staging path is unsafe") from None
        if (
            stat.S_ISLNK(metadata.st_mode)
            or not expected_mode(metadata.st_mode)
            or candidate.parent.resolve(strict=True) != root
        ):
            raise ValueError("Ray Data staging path is unsafe")
    return True


def stage_binary_dataset(
    iterator: Any,
    *,
    source_root: str,
    cache_paths: str,
    copy_workers: int = 64,
) -> StageSummary:
    """Stream one full Ray Dataset into a node's two ephemeral NVMe volumes."""

    source = pathlib.Path(source_root).resolve(strict=False)
    roots = _validated_stage_roots(cache_paths)
    if copy_workers < 1 or copy_workers > 128:
        raise ValueError("Ray Data staging workers must be between 1 and 128")
    for root in roots:
        _prepare_stage_data_root(root)

    view = roots[0] / "dataset-view"
    marker = roots[0] / ".ray-data-stage.ready"
    if _validate_stage_reuse_entries(marker, view, roots[0]):
        values = marker.read_text(encoding="utf-8").strip().split("\t")
        if len(values) == 2:
            return StageSummary(view, int(values[0]), int(values[1]), 0.0, True)

    temporary_view = roots[0] / f".dataset-view.ray-data.{os.getpid()}.tmp"
    _remove_stage_directory(temporary_view, roots[0])
    temporary_view.mkdir(parents=True)
    started = time.perf_counter()
    files = 0
    byte_count = 0
    pending: set[concurrent.futures.Future[int]] = set()

    def collect(completed: set[concurrent.futures.Future[int]]) -> None:
        nonlocal files, byte_count
        for future in completed:
            byte_count += future.result()
            files += 1
            if files % 5000 == 0:
                print(
                    f"RAYTRAIN_RAY_DATA_STAGE_PROGRESS files={files} bytes={byte_count}",
                    file=sys.stderr,
                    flush=True,
                )

    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=copy_workers) as executor:
            for row in iterator.iter_rows():
                path_value = str(row["path"])
                payload = row["bytes"]
                if not isinstance(payload, (bytes, bytearray, memoryview)):
                    raise ValueError("Ray Data file payload is not binary")
                relative = _relative_file(path_value, source)
                pending.add(
                    executor.submit(
                        _write_staged_file,
                        relative,
                        bytes(payload),
                        roots,
                        temporary_view,
                    )
                )
                if len(pending) >= copy_workers * 4:
                    completed, pending = concurrent.futures.wait(
                        pending, return_when=concurrent.futures.FIRST_COMPLETED
                    )
                    collect(completed)
            if pending:
                completed, _ = concurrent.futures.wait(pending)
                collect(completed)
        if files == 0:
            raise ValueError("selected Ray Data staging dataset is empty")
        _remove_stage_directory(view, roots[0])
        os.replace(temporary_view, view)
        marker.write_text(f"{files}\t{byte_count}\n", encoding="utf-8")
    finally:
        _remove_stage_directory(temporary_view, roots[0])
    seconds = max(time.perf_counter() - started, 1e-9)
    print(
        f"RAYTRAIN_RAY_DATA_STAGE_COMPLETE files={files} bytes={byte_count} seconds={seconds:.6f}",
        file=sys.stderr,
        flush=True,
    )
    return StageSummary(view, files, byte_count, seconds, False)
