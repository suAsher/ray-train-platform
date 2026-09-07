const stages = { prepare: '准备缓存目录', probe: '挂载与读写验收中', cleanup: '等待验收卷回收' }

export function nodeOnboardingRows(nodes = []) {
  return nodes.map(node => ({
    name: node.nodeName || '未知节点',
    gpus: node.capacity ?? '未知',
    ready: node.nodeReady === true ? 'Ready' : node.nodeReady === false ? '未就绪' : '未知',
    scheduling: node.cordoned === true ? '已暂停调度' : node.cordoned === false ? '允许调度' : '未知',
    stage: node.onboardingStage === 'ready'
      ? (node.cacheReady === true ? '存储验收通过' : '待重新验收')
      : stages[node.onboardingStage] || '未启用自动验收 / 未验收',
    reason: typeof node.onboardingReason === 'string' ? [...node.onboardingReason].slice(0, 400).join('') : '',
  }))
}

export async function refreshNodeTopology(apiGet) {
  try {
    const topology = await apiGet('/api/v1/cluster/topology')
    if (!topology || !Array.isArray(topology.nodes)) throw new Error('invalid topology')
    return { nodes: topology.nodes, available: true, error: '', physicalGPUs: topology.totalGpus ?? null, allocatedGPUs: Number(topology.usedGpus || 0) }
  } catch {
    return { nodes: [], available: false, error: '节点状态未知，读取失败，请刷新重试。', physicalGPUs: null, allocatedGPUs: 0 }
  }
}
