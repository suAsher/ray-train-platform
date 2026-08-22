export function dataSpaceStorageReady(space) {
  if (space?.provider !== 'tos') return true
  if (space.storageStatus === 'ready') return true
  return typeof space.storageStatus === 'undefined' && space.mountStatus === 'ready'
}

export function dataSpaceReadiness(space) {
  if (!dataSpaceStorageReady(space)) {
    return { ready: false, message: '个人对象空间尚未就绪，请联系平台管理员处理。' }
  }
  if (space?.mountStatus === 'ready') return { ready: true, message: '' }
  if (space?.mountStatus === 'failed') {
    return { ready: false, message: 'GPU 挂载配置失败，请联系平台管理员处理。' }
  }
  if (space?.provider === 'idc') {
    return { ready: false, message: '管理员尚未为此 IDC 数据登记只读挂载。' }
  }
  return { ready: false, message: 'GPU 数据挂载尚未启用；你可以浏览文件，管理员完成存储挂载验收后才可用于调试和训练。' }
}
