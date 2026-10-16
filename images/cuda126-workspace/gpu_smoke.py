#!/usr/bin/env python3
"""Explicit GPU-only smoke; never skips an absent/broken GPU as success.

Run only in a newly authorized test Pod, never inside a user's debug instance.
RTX 4090 uses sm_89 SASS to exercise CUDA 12.x minor driver compatibility.
"""
import json
import os
import subprocess
import tempfile
from pathlib import Path

import torch
from torch.utils.cpp_extension import load_inline


def main():
    assert os.geteuid() == 1000
    assert torch.__version__ == '2.6.0+cu126'
    assert torch.version.cuda == '12.6'
    assert torch.cuda.is_available(), 'GPU is required for this acceptance'
    capability = torch.cuda.get_device_capability()
    assert capability == (8, 9), capability
    with tempfile.TemporaryDirectory(prefix='raytrain-cu126-gpu-') as directory:
        source = Path(directory) / 'nvcc.cu'
        source.write_text('''#include <cuda_runtime.h>
__global__ void increment(int *value) { *value += 1; }
int main() { int *value; if (cudaMallocManaged(&value, sizeof(int))) return 1;
*value=41; increment<<<1,1>>>(value); if(cudaDeviceSynchronize()) return 2;
int result=*value; cudaFree(value); return result == 42 ? 0 : 3; }
''')
        binary = Path(directory) / 'nvcc-smoke'
        subprocess.run(['nvcc', '-arch=sm_89', str(source), '-o', str(binary)],
                       check=True, timeout=180)
        subprocess.run([str(binary)], check=True, timeout=30)
        # Explicitly compile SASS, not only PTX that could need a newer driver.
        os.environ['TORCH_CUDA_ARCH_LIST'] = '8.9'
        os.environ['MAX_JOBS'] = '1'
        extension = load_inline(
            name='raytrain_cuda126_smoke', build_directory=directory,
            cpp_sources='torch::Tensor cuda_increment(torch::Tensor input);',
            cuda_sources='''#include <torch/extension.h>
__global__ void increment(float *data) { data[0] += 1.0f; }
torch::Tensor cuda_increment(torch::Tensor input) {
  auto output = input.clone(); increment<<<1,1>>>(output.data_ptr<float>());
  return output;
}
''', functions=['cuda_increment'], with_cuda=True, verbose=False)
        output = extension.cuda_increment(torch.tensor([41.0], device='cuda'))
        torch.cuda.synchronize()
        assert output.item() == 42.0
        model = torch.nn.Linear(16, 4).cuda()
        loss = model(torch.randn(8, 16, device='cuda')).square().mean()
        loss.backward()
        torch.cuda.synchronize()
        assert torch.isfinite(loss).item()
        assert model.weight.grad is not None
        print(json.dumps({'gpuValidation': 'passed', 'nvccSASS': 'sm_89',
                          'torchExtension': 'passed', 'forwardBackward': 'passed',
                          'gpu': torch.cuda.get_device_name(), 'cudaRuntime': '12.6',
                          'multiNodeValidation': 'not_run'}))


if __name__ == '__main__':
    main()
