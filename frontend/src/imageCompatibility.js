export const LEGACY_RAY_VERSION = '2.35.0'
export const RAY_DDP_ENGINE = 'ray-ddp'
export const RAY_TRAIN_ENGINE = 'ray-train'

export function defaultImageCompatibilityState() {
  return {
    rayVersion: LEGACY_RAY_VERSION,
    supportedEngines: [RAY_DDP_ENGINE],
  }
}

export function reconcileImageCompatibility(form, rayVersion) {
  const currentEngines = Array.isArray(form.supportedEngines) ? [...form.supportedEngines] : []
  if (rayVersion !== LEGACY_RAY_VERSION) {
    return { ...form, rayVersion, supportedEngines: currentEngines }
  }

  const legacyEngines = currentEngines.filter((engine) => engine !== RAY_TRAIN_ENGINE)
  return {
    ...form,
    rayVersion,
    supportedEngines: legacyEngines.length > 0 ? legacyEngines : [RAY_DDP_ENGINE],
  }
}

export function buildCreateImageRequest(form) {
  return {
    name: form.name,
    reference: form.reference,
    kind: form.kind,
    rayVersion: form.rayVersion,
    supportedEngines: Array.isArray(form.supportedEngines) ? [...form.supportedEngines] : [],
    shared: Boolean(form.shared),
    framework: form.framework,
    isDefault: form.isDefault,
  }
}
