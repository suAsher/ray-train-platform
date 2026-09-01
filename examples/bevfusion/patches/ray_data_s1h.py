"""Minimal BEVFusion training-loader adapter for platform S1H Ray Data."""

from __future__ import annotations

import dataclasses
import os
from collections.abc import Callable, Iterator, Mapping, Sequence
from typing import Any


_NATIVE_S1H_MODES = frozenset(("streaming",))
_LEGACY_DATA_MODES = frozenset(("mount", "cache", "ray-data", "ray-data-stage"))
_MAX_PREFETCH_BATCHES = 16
_LIDAR_ONLY_V1_TRANSFORMS = frozenset(
    (
        "LoadAnnotations3D",
        "GlobalRotScaleTrans",
        "RandomFlip3D",
        "PointsRangeFilter",
        "ObjectRangeFilter",
        "ObjectNameFilter",
        "PointShuffle",
        "DefaultFormatBundle3D",
        "Collect3D",
    )
)
_PRE_PIPELINE_LIST_FIELDS = (
    "img_fields",
    "bbox3d_fields",
    "pts_mask_fields",
    "pts_seg_fields",
    "bbox_fields",
    "mask_fields",
    "seg_fields",
)
_MODALITY_FIELDS = frozenset(
    ("use_lidar", "use_camera", "use_radar", "use_map", "use_external")
)
_TRAINING_FORMAT_TAIL = ("DefaultFormatBundle3D", "Collect3D")
_UNSUPPORTED_CONFIG_MESSAGE = (
    "CBGSDataset config is not a supported lidar-only v1 pipeline"
)
_MULTIMODAL_SCHEMA_VERSION = "s1h-multimodal-webdataset-v2"


@dataclasses.dataclass(frozen=True)
class StreamingDatasetProxy:
    """Small immutable dataset surface used by the BEVFusion train runner.

    Ray Data owns indexing, sharding, and length. The proxy only preserves the
    pipeline and class metadata expected by BEVFusion. ``set_epoch`` mirrors
    ``Custom3DDataset`` by forwarding the epoch to stateful transforms without
    changing the proxy itself.
    """

    pipeline: Callable[[dict[str, Any]], Any]
    CLASSES: tuple[str, ...]

    def __post_init__(self) -> None:
        if not callable(self.pipeline):
            raise ValueError("pipeline must be callable")
        if not self.CLASSES or any(
            not isinstance(name, str) or not name for name in self.CLASSES
        ):
            raise ValueError("CLASSES must contain class names")

    def set_epoch(self, epoch: int) -> None:
        _non_negative_integer("epoch", epoch)
        try:
            transforms = getattr(self.pipeline, "transforms", ())
            for transform in transforms:
                setter = getattr(transform, "set_epoch", None)
                if callable(setter):
                    setter(epoch)
        except Exception:
            raise RuntimeError("BEVFusion pipeline epoch update failed") from None


@dataclasses.dataclass(frozen=True)
class _DecodedS1HPipeline:
    compose: Callable[[dict[str, Any]], Any]
    lidar_points_type: type
    lidar_boxes_type: type
    box_mode_3d: Any
    numpy: Any
    load_dim: int
    use_dim: tuple[int, ...]

    @property
    def transforms(self) -> Any:
        return getattr(self.compose, "transforms", ())

    def __call__(self, sample: dict[str, Any]) -> Any:
        try:
            prepared = self._prepare(sample)
            return self.compose(prepared)
        except Exception:
            raise RuntimeError("BEVFusion lidar-only v1 pipeline failed") from None

    def _prepare(self, sample: object) -> dict[str, Any]:
        if not isinstance(sample, Mapping):
            raise ValueError("decoded sample must be a mapping")
        points = sample.get("points")
        ann_info = sample.get("ann_info")
        if not isinstance(points, self.numpy.ndarray) or points.ndim != 2:
            raise ValueError("decoded points must be a matrix")
        if points.dtype != self.numpy.float32 or points.shape[1] != self.load_dim:
            raise ValueError("decoded points have an unsupported shape")
        if not isinstance(ann_info, Mapping):
            raise ValueError("decoded annotations must be a mapping")
        boxes = ann_info.get("gt_bboxes_3d")
        labels = ann_info.get("gt_labels_3d")
        if (
            not isinstance(boxes, self.numpy.ndarray)
            or boxes.ndim != 2
            or boxes.dtype != self.numpy.float32
            or boxes.shape[1] not in (7, 9)
        ):
            raise ValueError("decoded boxes have an unsupported shape")
        if (
            not isinstance(labels, self.numpy.ndarray)
            or labels.ndim != 1
            or labels.dtype != self.numpy.int64
            or len(labels) != len(boxes)
        ):
            raise ValueError("decoded labels have an unsupported shape")

        selected_points = points
        if self.use_dim != tuple(range(self.load_dim)):
            selected_points = points[:, self.use_dim]
        converted_boxes = self.lidar_boxes_type(
            boxes,
            box_dim=boxes.shape[1],
            origin=(0.5, 0.5, 0),
        ).convert_to(self.box_mode_3d)
        prepared = dict(sample)
        prepared["points"] = self.lidar_points_type(
            selected_points,
            points_dim=selected_points.shape[1],
            attribute_dims=None,
        )
        prepared["ann_info"] = {
            **ann_info,
            "gt_bboxes_3d": converted_boxes,
        }
        for field in _PRE_PIPELINE_LIST_FIELDS:
            prepared[field] = []
        prepared["box_type_3d"] = self.lidar_boxes_type
        prepared["box_mode_3d"] = self.box_mode_3d
        return prepared


def build_streaming_dataset_proxy(
    cbgs_config: Mapping[str, Any],
) -> StreamingDatasetProxy:
    """Build the real post-load BEVFusion pipeline for decoded S1H v1 rows.

    Only the lidar-only subset that can operate entirely on the decoded v1
    payload is admitted. The first ``LoadPointsFromFile`` is validated and
    replaced by the array-to-``LiDARPoints`` conversion in this adapter.
    """

    if (
        os.environ.get("PLATFORM_DATASET_SCHEMA_VERSION", "").strip()
        == _MULTIMODAL_SCHEMA_VERSION
    ):
        return build_multimodal_streaming_dataset_proxy(cbgs_config)
    dataset_config, remaining_pipeline, classes, load_dim, use_dim = (
        _validate_cbgs_lidar_only_v1(cbgs_config)
    )
    del dataset_config
    try:
        import copy
        import numpy as np
        from mmdet3d.core.bbox import get_box_type
        from mmdet3d.core.points import LiDARPoints
        from mmdet3d.datasets.pipelines import Compose

        lidar_boxes_type, box_mode_3d = get_box_type("LiDAR")
    except Exception:
        raise ValueError("BEVFusion lidar-only v1 dependencies are unavailable") from None
    try:
        compose = Compose(copy.deepcopy(remaining_pipeline))
    except Exception:
        raise ValueError("could not construct BEVFusion lidar-only v1 pipeline") from None
    pipeline = _DecodedS1HPipeline(
        compose=compose,
        lidar_points_type=LiDARPoints,
        lidar_boxes_type=lidar_boxes_type,
        box_mode_3d=box_mode_3d,
        numpy=np,
        load_dim=load_dim,
        use_dim=use_dim,
    )
    return StreamingDatasetProxy(pipeline=pipeline, CLASSES=classes)


@dataclasses.dataclass(frozen=True)
class _MultimodalPipeline:
    compose: Callable[[dict[str, Any]], Any]
    boxes_type: type
    box_mode_3d: Any
    numpy: Any
    with_velocity: bool
    class_names: tuple[str, ...]
    camera_names: tuple[str, ...] | None
    camera_indices: tuple[int, ...] | None

    @property
    def transforms(self) -> Any:
        return getattr(self.compose, "transforms", ())

    def __call__(self, sample: dict[str, Any]) -> Any:
        try:
            prepared = _prepare_multimodal_input(
                sample,
                numpy=self.numpy,
                boxes_type=self.boxes_type,
                box_mode_3d=self.box_mode_3d,
                with_velocity=self.with_velocity,
                class_names=self.class_names,
                camera_names=self.camera_names,
                camera_indices=self.camera_indices,
            )
            return self.compose(prepared)
        except Exception:
            raise RuntimeError("BEVFusion multimodal v2 pipeline failed") from None


def build_multimodal_streaming_dataset_proxy(
    cbgs_config: Mapping[str, Any],
) -> StreamingDatasetProxy:
    """Reuse the unmodified S1H fusion transforms over batch-local TAR members."""

    try:
        import copy
        import numpy as np
        from mmdet3d.core.bbox import get_box_type
        from mmdet3d.datasets.pipelines import Compose

        if not isinstance(cbgs_config, Mapping) or cbgs_config.get("type") != "CBGSDataset":
            raise ValueError
        dataset = cbgs_config.get("dataset")
        if not isinstance(dataset, Mapping) or dataset.get("type") != "NuScenesDataset":
            raise ValueError
        modality = dataset.get("modality")
        if not isinstance(modality, Mapping) or modality.get("use_lidar") is not True or modality.get("use_camera") is not True:
            raise ValueError
        pipeline_config = dataset.get("pipeline")
        if not isinstance(pipeline_config, Sequence) or isinstance(pipeline_config, (str, bytes)) or not pipeline_config:
            raise ValueError
        classes_value = dataset.get("object_classes", dataset.get("classes"))
        if not isinstance(classes_value, Sequence) or isinstance(classes_value, (str, bytes)) or not classes_value:
            raise ValueError
        classes = tuple(classes_value)
        if any(not isinstance(name, str) or not name for name in classes):
            raise ValueError
        camera_names_value = dataset.get("camera_names")
        camera_names = (
            tuple(camera_names_value) if camera_names_value is not None else None
        )
        if camera_names is not None and (
            not camera_names
            or any(not isinstance(name, str) or not name for name in camera_names)
            or len(set(camera_names)) != len(camera_names)
        ):
            raise ValueError
        camera_indices_value = dataset.get("camera_indices")
        camera_indices = (
            tuple(camera_indices_value) if camera_indices_value is not None else None
        )
        if camera_indices is not None and (
            not camera_indices
            or any(
                isinstance(index, bool) or not isinstance(index, int) or index < 0
                for index in camera_indices
            )
            or len(set(camera_indices)) != len(camera_indices)
        ):
            raise ValueError
        if camera_names is not None and camera_indices is not None:
            raise ValueError
        boxes_type, box_mode_3d = get_box_type("LiDAR")
        compose = Compose(copy.deepcopy(tuple(pipeline_config)))
    except Exception:
        raise ValueError("CBGSDataset config is not a supported multimodal v2 pipeline") from None
    return StreamingDatasetProxy(
        pipeline=_MultimodalPipeline(
            compose=compose,
            boxes_type=boxes_type,
            box_mode_3d=box_mode_3d,
            numpy=np,
            with_velocity=dataset.get("with_velocity", True) is not False,
            class_names=classes,
            camera_names=camera_names,
            camera_indices=camera_indices,
        ),
        CLASSES=classes,
    )


def _prepare_multimodal_input(
    sample: Mapping[str, Any],
    *,
    numpy: Any,
    boxes_type: type,
    box_mode_3d: Any,
    with_velocity: bool,
    class_names: tuple[str, ...],
    camera_names: tuple[str, ...] | None,
    camera_indices: tuple[int, ...] | None,
) -> dict[str, Any]:
    if not isinstance(sample, Mapping):
        raise ValueError("multimodal sample must be a mapping")
    info, payloads = sample.get("info"), sample.get("payload_paths")
    if not isinstance(info, Mapping) or not isinstance(payloads, Mapping):
        raise ValueError("multimodal sample payload is invalid")
    sweeps = info.get("sweeps")
    sweep_paths = payloads.get("sweeps")
    cameras = info.get("cams")
    camera_paths = payloads.get("cameras")
    if type(sweeps) is not list or type(sweep_paths) is not list or len(sweeps) != len(sweep_paths):
        raise ValueError("multimodal sweep payload is invalid")
    if type(cameras) is not dict or type(camera_paths) is not dict or set(cameras) != set(camera_paths):
        raise ValueError("multimodal camera payload is invalid")

    data: dict[str, Any] = {
        "token": sample["token"],
        "sample_idx": sample["token"],
        "lidar_path": payloads["lidar"],
        "sweeps": [
            {**dict(metadata), "data_path": path}
            for metadata, path in zip(sweeps, sweep_paths)
        ],
        "timestamp": sample["timestamp"],
        "location": "",
    }
    data["ego2global"] = _homogeneous_transform(
        info["ego2global_rotation"], info["ego2global_translation"], numpy=numpy
    )
    data["lidar2ego"] = _homogeneous_transform(
        info["lidar2ego_rotation"], info["lidar2ego_translation"], numpy=numpy
    )
    ordered_camera_names = tuple(sorted(cameras))
    if camera_names is not None:
        if any(name not in cameras for name in camera_names):
            raise ValueError("configured multimodal camera is unavailable")
        ordered_camera_names = camera_names
    elif camera_indices is not None:
        ordered_camera_names = tuple(
            ordered_camera_names[index]
            for index in camera_indices
            if index < len(ordered_camera_names)
        )
        if not ordered_camera_names:
            raise ValueError("configured multimodal cameras are unavailable")
    camera_items = [(name, cameras[name]) for name in ordered_camera_names]
    data.update(
        image_paths=[],
        lidar2camera=[],
        lidar2image=[],
        camera2ego=[],
        camera_intrinsics=[],
        camera2lidar=[],
    )
    for camera_name, camera in camera_items:
        data["image_paths"].append(camera_paths[camera_name])
        sensor2lidar_rotation = numpy.asarray(camera["sensor2lidar_rotation"], dtype=numpy.float32)
        sensor2lidar_translation = numpy.asarray(camera["sensor2lidar_translation"], dtype=numpy.float32)
        lidar2camera_rotation = numpy.linalg.inv(sensor2lidar_rotation)
        lidar2camera_translation = sensor2lidar_translation @ lidar2camera_rotation.T
        lidar2camera = numpy.eye(4, dtype=numpy.float32)
        lidar2camera[:3, :3] = lidar2camera_rotation.T
        lidar2camera[3, :3] = -lidar2camera_translation
        lidar2camera = lidar2camera.T
        intrinsics = numpy.eye(4, dtype=numpy.float32)
        intrinsics[:3, :3] = numpy.asarray(camera["camera_intrinsics"], dtype=numpy.float32)
        data["lidar2camera"].append(lidar2camera)
        data["camera_intrinsics"].append(intrinsics)
        data["lidar2image"].append(intrinsics @ lidar2camera)
        data["camera2ego"].append(
            _homogeneous_transform(camera["sensor2ego_rotation"], camera["sensor2ego_translation"], numpy=numpy)
        )
        data["camera2lidar"].append(
            _homogeneous_transform(camera["sensor2lidar_rotation"], camera["sensor2lidar_translation"], numpy=numpy)
        )

    boxes = numpy.asarray(info["boxes"], dtype=numpy.float32)
    labels = numpy.asarray(info["labels"], dtype=numpy.int64)
    if boxes.size == 0:
        boxes = numpy.empty((0, 7), dtype=numpy.float32)
    if boxes.ndim != 2 or boxes.shape[1] not in (7, 9) or labels.ndim != 1 or len(boxes) != len(labels):
        raise ValueError("multimodal annotations are invalid")
    point_counts = numpy.asarray(info.get("num_lidar_pts", [1] * len(boxes)), dtype=numpy.float32)
    if point_counts.shape != (len(boxes),):
        raise ValueError("multimodal point counts are invalid")
    mask = point_counts > 0
    boxes, labels = boxes[mask], labels[mask]
    if len(labels) and (labels.min() < 0 or labels.max() >= len(class_names)):
        raise ValueError("multimodal annotation label is outside configured classes")
    if with_velocity:
        velocities = numpy.asarray(info.get("gt_velocity", [[0.0, 0.0]] * len(point_counts)), dtype=numpy.float32)[mask]
        if velocities.shape != (len(boxes), 2):
            raise ValueError("multimodal velocities are invalid")
        velocities = numpy.nan_to_num(
            velocities,
            copy=True,
            nan=0.0,
            posinf=0.0,
            neginf=0.0,
        )
        if boxes.shape[1] == 7:
            boxes = numpy.concatenate([boxes, velocities], axis=-1)
    data["ann_info"] = {
        "gt_bboxes_3d": boxes_type(
            boxes, box_dim=boxes.shape[-1], origin=(0.5, 0.5, 0)
        ).convert_to(box_mode_3d),
        "gt_labels_3d": labels,
        "gt_names": numpy.asarray(
            [class_names[int(label)] for label in labels], dtype=object
        ),
    }
    for field in _PRE_PIPELINE_LIST_FIELDS:
        data[field] = []
    data["box_type_3d"] = boxes_type
    data["box_mode_3d"] = box_mode_3d
    return data


def _homogeneous_transform(rotation: Any, translation: Any, *, numpy: Any) -> Any:
    matrix = numpy.eye(4, dtype=numpy.float32)
    normalized_rotation = numpy.asarray(rotation, dtype=numpy.float32)
    normalized_translation = numpy.asarray(translation, dtype=numpy.float32)
    if normalized_rotation.shape != (3, 3) or normalized_translation.shape != (3,):
        raise ValueError("multimodal transform is invalid")
    matrix[:3, :3] = normalized_rotation
    matrix[:3, 3] = normalized_translation
    return matrix


def _validate_cbgs_lidar_only_v1(
    config: object,
) -> tuple[
    Mapping[str, Any],
    tuple[Mapping[str, Any], ...],
    tuple[str, ...],
    int,
    tuple[int, ...],
]:
    try:
        if not isinstance(config, Mapping) or config.get("type") != "CBGSDataset":
            raise ValueError
        dataset = config.get("dataset")
        if (
            not isinstance(dataset, Mapping)
            or dataset.get("type") != "NuScenesDataset"
        ):
            raise ValueError
        if dataset.get("test_mode", False) is not False:
            raise ValueError
        if dataset.get("box_type_3d", "LiDAR") != "LiDAR":
            raise ValueError
        modality = dataset.get("modality")
        if (
            not isinstance(modality, Mapping)
            or set(modality) != _MODALITY_FIELDS
            or modality.get("use_lidar") is not True
        ):
            raise ValueError
        if any(
            modality.get(field, False) is not False
            for field in ("use_camera", "use_radar", "use_map", "use_external")
        ):
            raise ValueError

        raw_classes = dataset.get("object_classes", dataset.get("classes"))
        if (
            not isinstance(raw_classes, Sequence)
            or isinstance(raw_classes, (str, bytes, bytearray))
            or not raw_classes
            or any(not isinstance(name, str) or not name for name in raw_classes)
        ):
            raise ValueError
        classes = tuple(raw_classes)

        raw_pipeline = dataset.get("pipeline")
        if (
            not isinstance(raw_pipeline, Sequence)
            or isinstance(raw_pipeline, (str, bytes, bytearray))
            or not raw_pipeline
            or any(not isinstance(transform, Mapping) for transform in raw_pipeline)
        ):
            raise ValueError
        transform_names = tuple(transform.get("type") for transform in raw_pipeline)
        if (
            transform_names[0] != "LoadPointsFromFile"
            or transform_names.count("LoadPointsFromFile") != 1
            or transform_names.count("LoadAnnotations3D") != 1
            or transform_names[1] != "LoadAnnotations3D"
            or transform_names[-2:] != _TRAINING_FORMAT_TAIL
            or any(
                not isinstance(name, str)
                or name not in _LIDAR_ONLY_V1_TRANSFORMS
                for name in transform_names[1:]
            )
        ):
            raise ValueError
        load_dim, use_dim = _validate_load_points_config(raw_pipeline[0])
        for transform in raw_pipeline[1:]:
            _validate_v1_transform_config(transform)
    except Exception:
        raise ValueError(_UNSUPPORTED_CONFIG_MESSAGE) from None
    return dataset, tuple(raw_pipeline[1:]), classes, load_dim, use_dim


def _validate_load_points_config(config: Mapping[str, Any]) -> tuple[int, tuple[int, ...]]:
    if config.get("coord_type") != "LIDAR":
        raise ValueError
    load_dim = config.get("load_dim")
    if isinstance(load_dim, bool) or not isinstance(load_dim, int) or load_dim <= 0:
        raise ValueError
    raw_use_dim = config.get("use_dim", load_dim)
    if isinstance(raw_use_dim, bool):
        raise ValueError
    if isinstance(raw_use_dim, int):
        use_dim = tuple(range(raw_use_dim))
    elif isinstance(raw_use_dim, Sequence) and not isinstance(
        raw_use_dim, (str, bytes, bytearray)
    ):
        use_dim = tuple(raw_use_dim)
    else:
        raise ValueError
    if (
        not use_dim
        or any(isinstance(index, bool) or not isinstance(index, int) for index in use_dim)
        or len(set(use_dim)) != len(use_dim)
        or min(use_dim) < 0
        or max(use_dim) >= load_dim
    ):
        raise ValueError
    if config.get("reduce_beams", 32) not in (None, 32):
        raise ValueError
    if config.get("load_augmented") not in (None, False):
        raise ValueError
    if config.get("shift_height", False) is not False:
        raise ValueError
    if config.get("use_color", False) is not False:
        raise ValueError
    return load_dim, use_dim


def _validate_v1_transform_config(config: Mapping[str, Any]) -> None:
    transform_type = config["type"]
    if transform_type == "LoadAnnotations3D":
        if config.get("with_bbox_3d", True) is not True:
            raise ValueError
        if config.get("with_label_3d", True) is not True:
            raise ValueError
        unsupported_flags = (
            "with_bbox",
            "with_label",
            "with_mask",
            "with_seg",
            "with_attr_label",
            "with_pts_instance_mask",
            "with_pts_semantic_mask",
        )
        if any(config.get(field, False) is not False for field in unsupported_flags):
            raise ValueError
    if transform_type == "Collect3D":
        keys = config.get("keys")
        if (
            not isinstance(keys, Sequence)
            or isinstance(keys, (str, bytes, bytearray))
            or set(keys)
            != {"points", "gt_bboxes_3d", "gt_labels_3d"}
        ):
            raise ValueError


@dataclasses.dataclass(frozen=True)
class _MMCVBatchSamplerCompatibility:
    """Inert sampler shape required by MMCV's sampler seed hook."""

    sampler: None = dataclasses.field(default=None, init=False)


@dataclasses.dataclass(frozen=True)
class S1HWorkerDataLoader:
    """MMCV-compatible iterable over the current Ray Train worker shard.

    Ray Data already owns worker partitioning. This adapter therefore exposes
    no sampler and batches decoded rows with the config's per-GPU batch size.
    The platform supplies the worker sample count as provenance so an
    epoch-based runner can determine its number of iterations. Iteration also
    enforces that exact count before accepting each complete batch.
    """

    pipeline: Callable[[dict[str, Any]], Any]
    collate_fn: Callable[..., Any]
    samples_per_gpu: int
    worker_sample_count: int
    prefetch_batches: int = 2
    dataset_name: str = "train"
    batch_resolver: Callable[
        [Sequence[Mapping[str, Any]]], Sequence[Mapping[str, Any]]
    ] | None = None
    sampler: None = dataclasses.field(default=None, init=False)
    batch_sampler: _MMCVBatchSamplerCompatibility = dataclasses.field(
        default_factory=_MMCVBatchSamplerCompatibility,
        init=False,
    )

    def __post_init__(self) -> None:
        if not callable(self.pipeline):
            raise ValueError("pipeline must be callable")
        if not callable(self.collate_fn):
            raise ValueError("collate_fn must be callable")
        _positive_integer("samples_per_gpu", self.samples_per_gpu)
        _positive_integer("worker_sample_count", self.worker_sample_count)
        if (
            isinstance(self.prefetch_batches, bool)
            or not isinstance(self.prefetch_batches, int)
            or not 0 <= self.prefetch_batches <= _MAX_PREFETCH_BATCHES
        ):
            raise ValueError(
                f"prefetch_batches must be between 0 and {_MAX_PREFETCH_BATCHES}"
            )
        if self.dataset_name != "train":
            raise ValueError("dataset_name must be train for the S1H training loader")
        if self.batch_resolver is not None and not callable(self.batch_resolver):
            raise ValueError("batch_resolver must be callable")

    def __len__(self) -> int:
        return (
            self.worker_sample_count + self.samples_per_gpu - 1
        ) // self.samples_per_gpu

    def __iter__(self) -> Iterator[Any]:
        from raytrain_runtime import ray_data

        worker_arguments = {
            "name": self.dataset_name,
            "samples_per_gpu": self.samples_per_gpu,
            "prefetch_batches": self.prefetch_batches,
            "pipeline": self.pipeline,
        }
        if self.batch_resolver is not None:
            worker_arguments["batch_resolver"] = self.batch_resolver
        observed_sample_count = 0
        worker_batches = ray_data.worker_s1h_batches
        if (
            os.environ.get("PLATFORM_DATASET_SCHEMA_VERSION", "").strip()
            == _MULTIMODAL_SCHEMA_VERSION
        ):
            worker_batches = ray_data.worker_s1h_webdataset_batches
            worker_arguments["expected_sample_count"] = self.worker_sample_count
        for samples in worker_batches(
            **worker_arguments,
        ):
            try:
                batch_sample_count = len(samples)
            except Exception:
                raise RuntimeError(
                    "S1H worker shard returned an invalid batch"
                ) from None
            next_sample_count = observed_sample_count + batch_sample_count
            if next_sample_count > self.worker_sample_count:
                raise RuntimeError(
                    "S1H worker shard exceeded declared sample count"
                )
            observed_sample_count = next_sample_count
            yield self.collate_fn(
                samples,
                samples_per_gpu=self.samples_per_gpu,
            )
        if observed_sample_count != self.worker_sample_count:
            raise RuntimeError(
                "S1H worker shard ended before declared sample count"
            )


def build_bevfusion_train_dataloader(
    *,
    data_mode: str,
    legacy_builder: Callable[[], Any],
    pipeline: Callable[[dict[str, Any]], Any],
    collate_fn: Callable[..., Any],
    samples_per_gpu: int,
    worker_sample_count: int,
    prefetch_batches: int = 2,
    batch_resolver: Callable[
        [Sequence[Mapping[str, Any]]], Sequence[Mapping[str, Any]]
    ] | None = None,
) -> Any:
    """Select the S1H shard loader only for an explicit native data mode.

    ``legacy_builder`` remains the sole path for mount, cache, existing
    ``ray-data``, and staging modes, preserving their existing behavior.
    """

    if not callable(legacy_builder):
        raise ValueError("legacy_builder must be callable")
    if data_mode in _LEGACY_DATA_MODES:
        return legacy_builder()
    if data_mode not in _NATIVE_S1H_MODES:
        raise ValueError("data_mode is not supported by the S1H adapter")
    return S1HWorkerDataLoader(
        pipeline=pipeline,
        collate_fn=collate_fn,
        samples_per_gpu=samples_per_gpu,
        worker_sample_count=worker_sample_count,
        prefetch_batches=prefetch_batches,
        batch_resolver=batch_resolver,
    )


def _positive_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{name} must be a positive integer")
    return value


def _non_negative_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{name} must be a non-negative integer")
    return value
