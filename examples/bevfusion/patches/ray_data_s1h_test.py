"""Contract tests for the minimal BEVFusion S1H Ray Data adapter."""

from __future__ import annotations

import copy
import dataclasses
import importlib
import inspect
from pathlib import Path
import sys
import types
import unittest
from unittest import mock

import numpy as np


PATCH_DIR = Path(__file__).resolve().parent
RUNTIME_PARENT = Path(__file__).resolve().parents[3] / "images" / "workspace"
for path in (PATCH_DIR, RUNTIME_PARENT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))


def _adapter_module():
    try:
        return importlib.import_module("ray_data_s1h")
    except ModuleNotFoundError as error:
        raise AssertionError(
            "the BEVFusion S1H Ray Data patch is not implemented"
        ) from error


def _lidar_only_cbgs_config(*pipeline):
    if not pipeline:
        pipeline = (
            {
                "type": "LoadPointsFromFile",
                "coord_type": "LIDAR",
                "load_dim": 5,
                "use_dim": 5,
            },
            {
                "type": "LoadAnnotations3D",
                "with_bbox_3d": True,
                "with_label_3d": True,
                "with_attr_label": False,
            },
            {"type": "PointShuffle"},
            {
                "type": "DefaultFormatBundle3D",
                "classes": ["car", "pedestrian"],
            },
            {
                "type": "Collect3D",
                "keys": ["points", "gt_bboxes_3d", "gt_labels_3d"],
            },
        )
    return {
        "type": "CBGSDataset",
        "dataset": {
            "type": "NuScenesDataset",
            "pipeline": list(pipeline),
            "object_classes": ["car", "pedestrian"],
            "modality": {
                "use_lidar": True,
                "use_camera": False,
                "use_radar": False,
                "use_map": False,
                "use_external": False,
            },
            "test_mode": False,
            "box_type_3d": "LiDAR",
        },
    }


def _multimodal_cbgs_config():
    return {
        "type": "CBGSDataset",
        "dataset": {
            "type": "NuScenesDataset",
            "pipeline": [
                {"type": "LoadMultiViewImageFromFiles"},
                {"type": "LoadPointsFromFile", "load_dim": 5, "use_dim": 5},
                {"type": "LoadPointsFromMultiSweeps", "sweeps_num": 10},
                {
                    "type": "LoadAnnotations3D",
                    "with_bbox_3d": True,
                    "with_label_3d": True,
                },
            ],
            "object_classes": ["Car", "IGV"],
            "modality": {
                "use_lidar": True,
                "use_camera": True,
                "use_radar": False,
                "use_map": False,
                "use_external": False,
            },
            "camera_names": ["CAM_REAR", "CAM_FRONT"],
            "with_velocity": True,
        },
    }


def _fake_bevfusion_modules(compose_type, points_type, boxes_type, box_mode):
    mmdet3d = types.ModuleType("mmdet3d")
    core = types.ModuleType("mmdet3d.core")
    bbox = types.ModuleType("mmdet3d.core.bbox")
    bbox.get_box_type = mock.Mock(return_value=(boxes_type, box_mode))
    points = types.ModuleType("mmdet3d.core.points")
    points.LiDARPoints = points_type
    datasets = types.ModuleType("mmdet3d.datasets")
    pipelines = types.ModuleType("mmdet3d.datasets.pipelines")
    pipelines.Compose = compose_type
    return {
        "mmdet3d": mmdet3d,
        "mmdet3d.core": core,
        "mmdet3d.core.bbox": bbox,
        "mmdet3d.core.points": points,
        "mmdet3d.datasets": datasets,
        "mmdet3d.datasets.pipelines": pipelines,
    }, bbox.get_box_type


class RayDataS1HPatchTest(unittest.TestCase):
    def test_multimodal_proxy_reconstructs_real_s1h_fusion_input(self):
        adapter = _adapter_module()
        compose_instances = []

        class FakeCompose:
            def __init__(self, pipeline):
                self.config = copy.deepcopy(pipeline)
                self.transforms = []
                compose_instances.append(self)

            def __call__(self, sample):
                return sample

        class FakeBoxes:
            def __init__(self, values, *, box_dim, origin):
                self.values = values
                self.box_dim = box_dim
                self.origin = origin

            def convert_to(self, box_mode):
                self.box_mode = box_mode
                return self

        modules, get_box_type = _fake_bevfusion_modules(
            FakeCompose, object, FakeBoxes, "LIDAR_MODE"
        )
        config = _multimodal_cbgs_config()
        original_config = copy.deepcopy(config)
        with mock.patch.dict(sys.modules, modules), mock.patch.dict(
            adapter.os.environ,
            {"PLATFORM_DATASET_SCHEMA_VERSION": "s1h-multimodal-webdataset-v2"},
        ):
            proxy = adapter.build_streaming_dataset_proxy(config)

        self.assertEqual(config, original_config)
        self.assertEqual(proxy.CLASSES, ("Car", "IGV"))
        get_box_type.assert_called_once_with("LiDAR")
        self.assertEqual(
            [item["type"] for item in compose_instances[0].config],
            [
                "LoadMultiViewImageFromFiles",
                "LoadPointsFromFile",
                "LoadPointsFromMultiSweeps",
                "LoadAnnotations3D",
            ],
        )

        identity = np.eye(3, dtype=np.float32).tolist()
        sample = {
            "token": "sample-a",
            "timestamp": 42,
            "payload_paths": {
                "lidar": "/tmp/batch/lidar.bin",
                "sweeps": ["/tmp/batch/sweep.bin"],
                "cameras": {
                    "CAM_FRONT": "/tmp/batch/front.jpg",
                    "CAM_REAR": "/tmp/batch/rear.jpg",
                },
            },
            "info": {
                "boxes": [[1, 2, 3, 4, 5, 6, 0], [7, 8, 9, 1, 2, 3, 0]],
                "labels": [0, 1],
                "num_lidar_pts": [3, 0],
                "gt_velocity": [[1.5, float("inf")], [9, 9]],
                "lidar2ego_rotation": identity,
                "lidar2ego_translation": [1, 2, 3],
                "ego2global_rotation": identity,
                "ego2global_translation": [4, 5, 6],
                "sweeps": [
                    {
                        "timestamp": 41,
                        "sensor2lidar_rotation": identity,
                        "sensor2lidar_translation": [0, 0, 0],
                    }
                ],
                "cams": {
                    name: {
                        "sensor2lidar_rotation": identity,
                        "sensor2lidar_translation": [0, 0, 0],
                        "sensor2ego_rotation": identity,
                        "sensor2ego_translation": [0, 0, 0],
                        "camera_intrinsics": identity,
                    }
                    for name in ("CAM_FRONT", "CAM_REAR")
                },
            },
        }
        prepared = proxy.pipeline(sample)

        self.assertEqual(
            prepared["image_paths"],
            ["/tmp/batch/rear.jpg", "/tmp/batch/front.jpg"],
        )
        self.assertEqual(prepared["lidar_path"], "/tmp/batch/lidar.bin")
        self.assertEqual(prepared["sweeps"][0]["data_path"], "/tmp/batch/sweep.bin")
        np.testing.assert_array_equal(
            prepared["ann_info"]["gt_names"], np.asarray(["Car"], dtype=object)
        )
        np.testing.assert_array_equal(
            prepared["ann_info"]["gt_labels_3d"], np.asarray([0], dtype=np.int64)
        )
        boxes = prepared["ann_info"]["gt_bboxes_3d"]
        self.assertIsInstance(boxes, FakeBoxes)
        self.assertEqual(boxes.box_dim, 9)
        self.assertEqual(boxes.origin, (0.5, 0.5, 0))
        np.testing.assert_array_equal(boxes.values[0, -2:], np.asarray([1.5, 0]))

    def test_multimodal_camera_indices_ignore_missing_views_like_legacy_dataset(self):
        adapter = _adapter_module()

        class FakeBoxes:
            def __init__(self, values, *, box_dim, origin):
                self.values = values

            def convert_to(self, _box_mode):
                return self

        identity = np.eye(3, dtype=np.float32).tolist()
        camera = {
            "sensor2lidar_rotation": identity,
            "sensor2lidar_translation": [0, 0, 0],
            "sensor2ego_rotation": identity,
            "sensor2ego_translation": [0, 0, 0],
            "camera_intrinsics": identity,
        }
        sample = {
            "token": "sample-a",
            "timestamp": 42,
            "payload_paths": {
                "lidar": "/tmp/batch/lidar.bin",
                "sweeps": [],
                "cameras": {
                    "CAM_FRONT": "/tmp/batch/front.jpg",
                    "CAM_REAR": "/tmp/batch/rear.jpg",
                },
            },
            "info": {
                "boxes": [],
                "labels": [],
                "lidar2ego_rotation": identity,
                "lidar2ego_translation": [0, 0, 0],
                "ego2global_rotation": identity,
                "ego2global_translation": [0, 0, 0],
                "sweeps": [],
                "cams": {"CAM_FRONT": camera, "CAM_REAR": camera},
            },
        }

        prepared = adapter._prepare_multimodal_input(
            sample,
            numpy=np,
            boxes_type=FakeBoxes,
            box_mode_3d="LIDAR_MODE",
            with_velocity=False,
            class_names=("Car",),
            camera_names=None,
            camera_indices=(0, 1, 4),
        )

        self.assertEqual(
            prepared["image_paths"],
            ["/tmp/batch/front.jpg", "/tmp/batch/rear.jpg"],
        )
        self.assertEqual(
            prepared["ann_info"]["gt_bboxes_3d"].values.shape,
            (0, 7),
        )

    def test_streaming_proxy_is_frozen_and_forwards_set_epoch_to_transforms(self):
        adapter = _adapter_module()

        class EpochTransform:
            def __init__(self):
                self.epochs = []

            def set_epoch(self, epoch):
                self.epochs.append(epoch)

        epoch_transform = EpochTransform()

        class FakeCompose:
            def __init__(self, _pipeline):
                self.transforms = [epoch_transform, object()]

            def __call__(self, sample):
                return sample

        class FakePoints:
            pass

        class FakeBoxes:
            pass

        modules, _get_box_type = _fake_bevfusion_modules(
            FakeCompose, FakePoints, FakeBoxes, "LIDAR_MODE"
        )
        with mock.patch.dict(sys.modules, modules):
            proxy = adapter.build_streaming_dataset_proxy(
                _lidar_only_cbgs_config()
            )

        self.assertIsInstance(proxy, adapter.StreamingDatasetProxy)
        self.assertEqual(proxy.CLASSES, ("car", "pedestrian"))
        with self.assertRaises(dataclasses.FrozenInstanceError):
            proxy.pipeline = mock.Mock()
        self.assertIsNone(proxy.set_epoch(4))
        self.assertEqual(epoch_transform.epochs, [4])
        for invalid_epoch in (-1, True, 1.5):
            with self.subTest(invalid_epoch=invalid_epoch):
                with self.assertRaisesRegex(ValueError, "epoch"):
                    proxy.set_epoch(invalid_epoch)

    def test_streaming_proxy_adapts_decoded_arrays_then_runs_remaining_compose(self):
        adapter = _adapter_module()
        compose_instances = []

        class FakeCompose:
            def __init__(self, pipeline):
                self.config = copy.deepcopy(pipeline)
                self.transforms = []
                self.received = []
                compose_instances.append(self)

            def __call__(self, sample):
                self.received.append(sample)
                return {"composed": sample}

        class FakePoints:
            def __init__(self, values, *, points_dim, attribute_dims=None):
                self.values = values
                self.points_dim = points_dim
                self.attribute_dims = attribute_dims

        class FakeBoxes:
            def __init__(self, values, *, box_dim, origin):
                self.values = values
                self.box_dim = box_dim
                self.origin = origin
                self.converted_to = None

            def convert_to(self, box_mode):
                self.converted_to = box_mode
                return self

        modules, get_box_type = _fake_bevfusion_modules(
            FakeCompose, FakePoints, FakeBoxes, "LIDAR_MODE"
        )
        config = _lidar_only_cbgs_config()
        original_config = copy.deepcopy(config)
        with mock.patch.dict(sys.modules, modules):
            proxy = adapter.build_streaming_dataset_proxy(config)

        self.assertEqual(config, original_config)
        get_box_type.assert_called_once_with("LiDAR")
        self.assertEqual(
            [transform["type"] for transform in compose_instances[0].config],
            [
                "LoadAnnotations3D",
                "PointShuffle",
                "DefaultFormatBundle3D",
                "Collect3D",
            ],
        )

        points = np.arange(10, dtype=np.float32).reshape(2, 5)
        boxes = np.arange(18, dtype=np.float32).reshape(2, 9)
        labels = np.asarray([0, 1], dtype=np.int64)
        decoded = {
            "token": "sample-a",
            "points": points,
            "ann_info": {
                "gt_bboxes_3d": boxes,
                "gt_labels_3d": labels,
            },
        }

        result = proxy.pipeline(decoded)
        prepared = result["composed"]
        self.assertIs(decoded["points"], points)
        self.assertIs(decoded["ann_info"]["gt_bboxes_3d"], boxes)
        self.assertIsInstance(prepared["points"], FakePoints)
        self.assertIs(prepared["points"].values, points)
        self.assertEqual(prepared["points"].points_dim, 5)
        self.assertIsNone(prepared["points"].attribute_dims)
        converted_boxes = prepared["ann_info"]["gt_bboxes_3d"]
        self.assertIsInstance(converted_boxes, FakeBoxes)
        self.assertIs(converted_boxes.values, boxes)
        self.assertEqual(converted_boxes.box_dim, 9)
        self.assertEqual(converted_boxes.origin, (0.5, 0.5, 0))
        self.assertEqual(converted_boxes.converted_to, "LIDAR_MODE")
        self.assertIs(prepared["ann_info"]["gt_labels_3d"], labels)
        self.assertEqual(prepared["img_fields"], [])
        self.assertEqual(prepared["bbox3d_fields"], [])
        self.assertEqual(prepared["pts_mask_fields"], [])
        self.assertEqual(prepared["pts_seg_fields"], [])
        self.assertEqual(prepared["bbox_fields"], [])
        self.assertEqual(prepared["mask_fields"], [])
        self.assertEqual(prepared["seg_fields"], [])
        self.assertIs(prepared["box_type_3d"], FakeBoxes)
        self.assertEqual(prepared["box_mode_3d"], "LIDAR_MODE")

    def test_streaming_proxy_rejects_non_v1_or_ambiguous_pipelines_redacted(self):
        adapter = _adapter_module()
        private_path = "tos://access-key:secret@internal/private.pkl"
        first = {
            "type": "LoadPointsFromFile",
            "coord_type": "LIDAR",
            "load_dim": 5,
            "use_dim": 5,
        }
        invalid_configs = {
            "not_cbgs": {"type": "NuScenesDataset", "dataset": {}},
            "wrong_inner_dataset": _lidar_only_cbgs_config(),
            "camera": _lidar_only_cbgs_config(
                first,
                {"type": "LoadMultiViewImageFromFiles", "path": private_path},
            ),
            "multisweep": _lidar_only_cbgs_config(
                first,
                {"type": "LoadPointsFromMultiSweeps", "path": private_path},
            ),
            "object_paste": _lidar_only_cbgs_config(
                first,
                {"type": "ObjectPaste", "info_path": private_path},
            ),
            "load_points_not_first": _lidar_only_cbgs_config(
                {"type": "PointShuffle"}, first
            ),
            "second_load_points": _lidar_only_cbgs_config(
                first, {**first, "path": private_path}
            ),
            "missing_training_format": _lidar_only_cbgs_config(
                first,
                {
                    "type": "LoadAnnotations3D",
                    "with_bbox_3d": True,
                    "with_label_3d": True,
                },
                {
                    "type": "Collect3D",
                    "keys": ["points", "gt_bboxes_3d", "gt_labels_3d"],
                },
            ),
        }
        invalid_configs["wrong_inner_dataset"]["dataset"]["type"] = "OtherDataset"
        camera_modality = _lidar_only_cbgs_config()
        camera_modality["dataset"]["modality"]["use_camera"] = True
        camera_modality["dataset"]["dataset_root"] = private_path
        invalid_configs["camera_modality"] = camera_modality
        unknown_modality = _lidar_only_cbgs_config()
        unknown_modality["dataset"]["modality"]["use_depth"] = True
        invalid_configs["unknown_modality"] = unknown_modality

        compose_calls = []

        class UnexpectedCompose:
            def __init__(self, _pipeline):
                compose_calls.append(_pipeline)
                raise AssertionError("invalid config reached Compose")

        modules, _get_box_type = _fake_bevfusion_modules(
            UnexpectedCompose, object, object, "LIDAR_MODE"
        )
        with mock.patch.dict(sys.modules, modules):
            for label, config in invalid_configs.items():
                with self.subTest(label=label):
                    with self.assertRaisesRegex(ValueError, "lidar-only v1") as caught:
                        adapter.build_streaming_dataset_proxy(config)
                    self.assertNotIn(private_path, str(caught.exception))
                    self.assertNotIn("access-key", str(caught.exception))
                    self.assertNotIn("secret", str(caught.exception))
                    self.assertNotIn("internal", str(caught.exception))
        self.assertEqual(compose_calls, [])

    def test_streaming_pipeline_redacts_dependency_and_transform_errors(self):
        adapter = _adapter_module()
        private_path = "tos://access-key:secret@internal/private.bin"

        class BrokenCompose:
            def __init__(self, _pipeline):
                raise OSError(private_path)

        modules, _get_box_type = _fake_bevfusion_modules(
            BrokenCompose, object, object, "LIDAR_MODE"
        )
        with mock.patch.dict(sys.modules, modules):
            with self.assertRaisesRegex(ValueError, "construct") as caught:
                adapter.build_streaming_dataset_proxy(_lidar_only_cbgs_config())
        self.assertNotIn(private_path, str(caught.exception))

        class RuntimeBrokenCompose:
            def __init__(self, _pipeline):
                self.transforms = []

            def __call__(self, _sample):
                raise OSError(private_path)

        class FakePoints:
            def __init__(self, _values, **_kwargs):
                pass

        class FakeBoxes:
            def __init__(self, _values, **_kwargs):
                pass

            def convert_to(self, _box_mode):
                return self

        modules, _get_box_type = _fake_bevfusion_modules(
            RuntimeBrokenCompose, FakePoints, FakeBoxes, "LIDAR_MODE"
        )
        with mock.patch.dict(sys.modules, modules):
            proxy = adapter.build_streaming_dataset_proxy(
                _lidar_only_cbgs_config()
            )
        sample = {
            "points": np.zeros((1, 5), dtype=np.float32),
            "ann_info": {
                "gt_bboxes_3d": np.zeros((0, 9), dtype=np.float32),
                "gt_labels_3d": np.zeros((0,), dtype=np.int64),
            },
        }
        with self.assertRaisesRegex(RuntimeError, "pipeline failed") as caught:
            proxy.pipeline(sample)
        self.assertNotIn(private_path, str(caught.exception))

    def test_streaming_loader_accepts_exact_count_and_keeps_per_gpu_batch(self):
        adapter = _adapter_module()
        pipeline = mock.Mock(side_effect=lambda sample: {**sample, "augmented": True})
        collate = mock.Mock(
            side_effect=lambda samples, samples_per_gpu: {
                "tokens": [sample["token"] for sample in samples],
                "samples_per_gpu": samples_per_gpu,
            }
        )
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}, {"token": "token-b"}],
                    [{"token": "token-c"}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(
                    side_effect=AssertionError("legacy loader used")
                ),
                pipeline=pipeline,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=3,
                prefetch_batches=3,
            )
            produced = list(loader)

        self.assertEqual(len(loader), 2)
        self.assertIsNone(loader.sampler)
        self.assertIsNone(loader.batch_sampler.sampler)
        self.assertEqual(loader.samples_per_gpu, 2)
        self.assertEqual(
            produced,
            [
                {"tokens": ["token-a", "token-b"], "samples_per_gpu": 2},
                {"tokens": ["token-c"], "samples_per_gpu": 2},
            ],
        )
        worker_batches.assert_called_once_with(
            name="train",
            samples_per_gpu=2,
            prefetch_batches=3,
            pipeline=pipeline,
        )
        self.assertEqual(collate.call_count, 2)

    def test_streaming_loader_selects_webdataset_worker_for_exact_v2_schema(self):
        adapter = _adapter_module()
        legacy_worker = mock.Mock(
            side_effect=AssertionError("legacy v1 worker was selected")
        )
        webdataset_worker = mock.Mock(
            return_value=iter(([{"token": "token-a"}],))
        )
        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches", legacy_worker
        ), mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_webdataset_batches",
            webdataset_worker,
        ), mock.patch.dict(
            adapter.os.environ,
            {"PLATFORM_DATASET_SCHEMA_VERSION": "s1h-multimodal-webdataset-v2"},
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=1,
                worker_sample_count=1,
            )
            self.assertEqual(list(loader), [[{"token": "token-a"}]])

        webdataset_worker.assert_called_once_with(
            name="train",
            samples_per_gpu=1,
            prefetch_batches=2,
            pipeline=loader.pipeline,
            expected_sample_count=1,
        )

    def test_streaming_loader_reports_a_redacted_short_shard_at_end(self):
        adapter = _adapter_module()
        private_token = "tos://access-key:secret@internal/private.parquet"
        collate = mock.Mock(side_effect=lambda samples, samples_per_gpu: samples)
        worker_batches = mock.Mock(
            return_value=iter(([{"token": private_token}],))
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=2,
            )
            batches = iter(loader)
            self.assertEqual(next(batches), [{"token": private_token}])
            with self.assertRaisesRegex(
                RuntimeError, "ended before declared sample count"
            ) as caught:
                next(batches)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access-key", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)
        self.assertEqual(collate.call_count, 1)

    def test_streaming_loader_rejects_an_overfull_batch_before_collate_or_yield(self):
        adapter = _adapter_module()
        private_token = "tos://access-key:secret@internal/private.parquet"
        collate = mock.Mock(side_effect=lambda samples, samples_per_gpu: samples)
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}],
                    [{"token": "token-b"}, {"token": private_token}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=2,
            )
            batches = iter(loader)
            self.assertEqual(next(batches), [{"token": "token-a"}])
            with self.assertRaisesRegex(
                RuntimeError, "exceeded declared sample count"
            ) as caught:
                next(batches)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access-key", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)
        self.assertEqual(collate.call_count, 1)

    def test_legacy_modes_including_ray_data_keep_the_existing_loader_unchanged(self):
        adapter = _adapter_module()
        sentinel = object()
        for data_mode in ("mount", "cache", "ray-data", "ray-data-stage"):
            with self.subTest(data_mode=data_mode):
                legacy_builder = mock.Mock(return_value=sentinel)
                with mock.patch(
                    "raytrain_runtime.ray_data.worker_s1h_batches",
                    side_effect=AssertionError("legacy mode touched a Ray shard"),
                ):
                    loader = adapter.build_bevfusion_train_dataloader(
                        data_mode=data_mode,
                        legacy_builder=legacy_builder,
                        pipeline=mock.Mock(),
                        collate_fn=mock.Mock(),
                        samples_per_gpu=4,
                        worker_sample_count=100,
                    )

                self.assertIs(loader, sentinel)
                legacy_builder.assert_called_once_with()

    def test_streaming_loader_never_constructs_a_distributed_sampler(self):
        adapter = _adapter_module()
        loader = adapter.build_bevfusion_train_dataloader(
            data_mode="streaming",
            legacy_builder=mock.Mock(side_effect=AssertionError("legacy loader used")),
            pipeline=lambda sample: sample,
            collate_fn=lambda samples, samples_per_gpu: samples,
            samples_per_gpu=1,
            worker_sample_count=1,
        )

        self.assertIsNone(loader.sampler)
        self.assertIsNone(loader.batch_sampler.sampler)
        self.assertNotIn("DistributedSampler", inspect.getsource(adapter))

    def test_streaming_loader_satisfies_mmcv_sampler_seed_hook_contract(self):
        adapter = _adapter_module()
        loader = adapter.build_bevfusion_train_dataloader(
            data_mode="streaming",
            legacy_builder=mock.Mock(side_effect=AssertionError("legacy loader used")),
            pipeline=lambda sample: sample,
            collate_fn=lambda samples, samples_per_gpu: samples,
            samples_per_gpu=1,
            worker_sample_count=1,
        )

        # MMCV's DistSamplerSeedHook falls through to
        # data_loader.batch_sampler.sampler when the top-level sampler is not
        # distributed. Ray Data owns sharding, so both sampler values remain
        # inert, but the compatibility path must still be safe to traverse.
        if hasattr(loader.sampler, "set_epoch"):
            loader.sampler.set_epoch(3)
        elif hasattr(loader.batch_sampler.sampler, "set_epoch"):
            loader.batch_sampler.sampler.set_epoch(3)

        self.assertIsNone(loader.sampler)
        self.assertIsNone(loader.batch_sampler.sampler)

    def test_streaming_loader_forwards_the_platform_batch_resolver(self):
        adapter = _adapter_module()
        batch_resolver = mock.Mock()
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}, {"token": "token-b"}],
                    [{"token": "token-c"}, {"token": "token-d"}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=2,
                worker_sample_count=4,
                batch_resolver=batch_resolver,
            )
            list(loader)

        worker_batches.assert_called_once_with(
            name="train",
            samples_per_gpu=2,
            prefetch_batches=2,
            pipeline=loader.pipeline,
            batch_resolver=batch_resolver,
        )

    def test_adapter_uses_only_shard_and_non_location_provenance(self):
        adapter = _adapter_module()
        source = inspect.getsource(adapter).lower()

        self.assertIn("worker_s1h_batches", source)
        self.assertNotIn("tos://", source)
        self.assertNotIn("/mnt/storage", source)
        self.assertNotIn("internal manifest", source)
        self.assertNotIn("manifest_uri", source)

    def test_invalid_mode_or_provenance_fails_before_reading_a_shard(self):
        adapter = _adapter_module()
        with self.assertRaisesRegex(ValueError, "data_mode"):
            adapter.build_bevfusion_train_dataloader(
                data_mode="implicit-auto",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=1,
                worker_sample_count=1,
            )
        with self.assertRaisesRegex(ValueError, "worker_sample_count"):
            adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=1,
                worker_sample_count=0,
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
