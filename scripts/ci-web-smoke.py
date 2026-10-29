"""HTTP assertions for the disposable GitHub kind control-plane smoke."""
import json
import pathlib
import secrets
import sys
import urllib.error
import urllib.request

ORIGIN = "http://127.0.0.1:18080"


def request(path, token=None, data=None):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        ORIGIN + path, headers=headers,
        data=None if data is None else json.dumps(data).encode(),
    )
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


for path in ("/", "/job", "/job/detail/example"):
    status, headers, body = request(path)
    assert status == 200 and "text/html" in headers.get("Content-Type", ""), path
    assert b'<div id="root">' in body, path
assert request("/runtime-config.js")[0] == 200
assert request("/api/v1/me")[0] == 401
assert request("/api/v1/me", token=secrets.token_urlsafe(32))[0] == 401

session_file = pathlib.Path(sys.argv[1])
if "--reuse-session" in sys.argv:
    credentials = json.loads(session_file.read_text())
else:
    status, _, body = request("/api/v1/auth/login", data={
        "username": "admin", "password": "ci-admin-password-12345",
    })
    assert status == 200, f"login status {status}"
    credentials = {"token": json.loads(body)["data"]["token"]}

token = credentials["token"]
status, _, body = request("/api/v1/me", token=token)
assert status == 200, f"session status {status}"
identity = json.loads(body)
assert identity.get("success") is True, "invalid API response envelope"
assert request("/api/v1/jobs", token=token)[0] == 200

if "--reuse-session" not in sys.argv:
    # No TOS configuration or data-space initializer is needed for local users.
    status, _, _ = request("/api/v1/local-users", token=token, data={
        "username": "ci-engineer", "password": "ci-engineer-password-12345",
        "roles": ["Engineer"], "tenantId": "local",
    })
    assert status == 201, f"local user creation status {status}"
    status, _, body = request("/api/v1/auth/login", data={
        "username": "ci-engineer", "password": "ci-engineer-password-12345",
    })
    assert status == 200, f"ordinary user login status {status}"
    credentials["user_token"] = json.loads(body)["data"]["token"]
    status, _, body = request(
        "/api/v1/personal-access-tokens", token=credentials["user_token"],
        data={"scopes": ["jobs:read"], "expiresInDays": 1},
    )
    assert status == 201, f"ordinary user PAT creation status {status}"
    credentials["pat"] = json.loads(body)["data"]["token"]
    # Persist only in the disposable runner's private directory, never in logs.
    session_file.touch(mode=0o600)
    session_file.chmod(0o600)
    session_file.write_text(json.dumps(credentials))

status, _, body = request("/api/v1/me", token=credentials["user_token"])
assert status == 200, f"ordinary user session status {status}"
user_identity = json.loads(body)["data"]
assert user_identity["username"] == "ci-engineer"
assert user_identity["roles"] == ["Engineer"]
for credential in (credentials["user_token"], credentials["pat"]):
    assert request("/api/v1/jobs", token=credential)[0] == 200
    assert request("/api/v1/local-users", token=credential)[0] == 403
print("UI, local users, scoped PATs and persistent session checks passed")
