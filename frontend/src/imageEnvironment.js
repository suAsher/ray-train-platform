export const imageEnvironmentFields = Object.freeze([
  { key: 'python', label: 'Python', max: 128 },
  { key: 'cuda', label: 'CUDA', max: 128 },
  { key: 'pytorch', label: 'PyTorch', max: 128 },
  { key: 'mlflow', label: 'MLflow', max: 128 },
  { key: 'dependencies', label: '主要依赖（含版本）', max: 12000 },
  { key: 'useCases', label: '适用模型 / 场景', max: 2000 },
  { key: 'validationNotes', label: '验证记录（命令、日期与结果）', max: 4000 },
])

export function emptyImageEnvironment() {
  return Object.fromEntries(imageEnvironmentFields.map(({ key }) => [key, '']))
}

export function imageEnvironmentRows(image = {}) {
  return [
    { key: 'ray', label: 'Ray', value: image.rayVersion || '未提供' },
    ...imageEnvironmentFields.map(({ key, label }) => ({
      key, label, value: typeof image.environment?.[key] === 'string' && image.environment[key].trim()
        ? image.environment[key] : '未提供',
    })),
  ]
}
