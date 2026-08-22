export const interactiveWorkspaceProfiles = Object.freeze([
  Object.freeze({ id: 'gpu-1', gpuCount: 1, label: '单卡调试', topology: '1 节点 × 1 GPU', description: '验证代码、数据挂载和依赖。' }),
  Object.freeze({ id: 'gpu-2', gpuCount: 2, label: '双卡 DDP 调试', topology: '1 节点 × 2 GPU', description: '在终端直接运行 torchrun 做最小 DDP 验证。' }),
  Object.freeze({ id: 'gpu-4', gpuCount: 4, label: '4 卡调试', topology: '1 节点 × 4 GPU', description: '检查 batch、显存和单机通信。' }),
  Object.freeze({ id: 'gpu-8', gpuCount: 8, label: '8 卡调试', topology: '1 节点 × 8 GPU', description: '在一台完整训练节点复现单机训练。' }),
])

export function workspaceProfileForGPUCount(value) {
  return interactiveWorkspaceProfiles.find((profile) => profile.gpuCount === Number(value)) || interactiveWorkspaceProfiles[0]
}
