// All administrator actions are intentionally reachable from the Portal. The
// API remains protected, but day-to-day user, image, data and queue management
// never depends on an undocumented curl command or direct database access.
export const adminNavigation = Object.freeze([
  Object.freeze({ id: 'console', to: '/admin', label: '管理员控制台' }),
  Object.freeze({ id: 'cluster', to: '/control-center', label: '集群算力' }),
  Object.freeze({ id: 'nodes', to: '/devices', label: 'GPU 节点' }),
])
