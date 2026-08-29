from __future__ import annotations

import importlib.util
import pathlib
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("train_image_batches.py")


class TrainImageBatchesExampleTest(unittest.TestCase):
    def test_training_loop_consumes_named_ray_data_shard(self):
        spec = importlib.util.spec_from_file_location("train_image_batches", MODULE_PATH)
        module = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        spec.loader.exec_module(module)

        iterator = [{"image": FakeTensor(2), "label": FakeTensor(2)}]
        model = mock.Mock(return_value=FakeTensor(2))
        optimizer = mock.Mock()
        loss = mock.Mock(return_value=FakeLoss())

        result = module.train_one_epoch(
            model,
            optimizer,
            loss,
            iterator_factory=mock.Mock(return_value=iterator),
        )

        self.assertEqual(result, {"batches": 1, "examples": 2})
        optimizer.zero_grad.assert_called_once_with(set_to_none=True)
        optimizer.step.assert_called_once_with()


class FakeTensor:
    def __init__(self, size: int):
        self.shape = (size, 1)

    def float(self):
        return self


class FakeLoss:
    def backward(self):
        return None


if __name__ == "__main__":
    unittest.main()
