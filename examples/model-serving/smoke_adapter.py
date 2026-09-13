"""Transport/protocol acceptance only; this is not a model accuracy benchmark."""
import tempfile
from pathlib import Path
from serving_sdk import ServingClient


def main():
    client = ServingClient.from_environment()
    with tempfile.TemporaryDirectory(prefix='serving-worker-') as directory:
        model = client.download_model(Path(directory) / 'model.bin')
        size = model.stat().st_size

        def predict(payload):
            return {'protocolOnly': True, 'modelBytesVerified': size,
                    'modelSha256': client.model_sha256, 'inputs': payload.get('inputs')}

        client.serve(predict)


if __name__ == '__main__':
    main()
