import assert from "node:assert/strict"
import test from "node:test"

import Keycloak from "keycloak-js"

test("configured optional auth skips Keycloak initialization", async t => {
  const hadWindow = Object.hasOwn(globalThis, "window")
  const previousWindow = globalThis.window
  const originalInit = Keycloak.prototype.init
  let initCalls = 0

  globalThis.window = {
    __RAY_PLATFORM_CONFIG__: {
      authRequired: false,
      keycloakURL: "https://keycloak.example.test",
      keycloakRealm: "demo",
      keycloakClientID: "ray-platform-demo"
    },
    location: { origin: "http://localhost" },
    setInterval
  }
  Keycloak.prototype.init = async () => {
    initCalls += 1
    return false
  }
  t.after(() => {
    Keycloak.prototype.init = originalInit
    if (hadWindow) {
      globalThis.window = previousWindow
    } else {
      delete globalThis.window
    }
  })

  const auth = await import(`./keycloak.js?optional-auth=${Date.now()}`)
  const authenticated = await auth.initAuth()

  assert.equal(authenticated, false)
  assert.equal(auth.isAuthenticated(), false)
  assert.equal(initCalls, 0)
})
