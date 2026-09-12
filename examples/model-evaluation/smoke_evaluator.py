"""Model transport/protocol smoke check, never a model accuracy benchmark."""
import tempfile
from pathlib import Path

from evaluation_sdk import EvaluationClient


def main():
    # Ray is already provided by the selected trusted runtime. The helper itself
    # stays dependency-free; never pretend an arbitrary .pth has a known model.
    from ray import train
    rank = train.get_context().get_world_rank()
    evaluation = EvaluationClient.from_environment()
    with tempfile.TemporaryDirectory(prefix='evaluation-worker-') as directory:
        model = evaluation.download_model(Path(directory) / 'model.bin')
        if rank == 0:
            evaluation.report([
                {'name': 'protocol/model_bytes_verified', 'value': model.stat().st_size,
                 'unit': 'bytes', 'direction': 'neutral'},
            ], world_rank=rank)


if __name__ == '__main__':
    main()
