"""Minimal managed Ray Data image-batch training loop.

The platform passes the registered dataset to ``TorchTrainer`` as ``train``.
This module stays importable without Ray so its batch loop can be unit tested.
"""

from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping
from typing import Any


def _train_iterator() -> Iterable[Mapping[str, Any]]:
    from raytrain_runtime.ray_data import worker_iterator

    return worker_iterator("train")


def train_one_epoch(
    model: Any,
    optimizer: Any,
    loss_function: Callable[[Any, Any], Any],
    *,
    iterator_factory: Callable[[], Iterable[Mapping[str, Any]]] = _train_iterator,
) -> dict[str, int]:
    """Train one epoch from prefetched batches of the named ``train`` shard."""

    batch_count = 0
    example_count = 0
    for batch in iterator_factory():
        images = batch["image"].float()
        targets = batch.get("label", images)
        optimizer.zero_grad(set_to_none=True)
        loss = loss_function(model(images), targets)
        loss.backward()
        optimizer.step()
        batch_count += 1
        example_count += int(images.shape[0])
    return {"batches": batch_count, "examples": example_count}
