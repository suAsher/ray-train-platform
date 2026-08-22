# External rayctl Delivery Design

**Status:** approved for implementation on 2026-08-18

## Goal

Allow an engineer on any approved external development machine to install a
small `rayctl` binary, submit a changed local directory as an immutable source
artifact, and operate the resulting Ray training job without Kubernetes or TOS
credentials.

## Security boundary

`rayctl` accepts only an HTTPS platform URL. A personal access token (PAT) is
entered through standard input and is stored only in an owner-only local config
file. The token, TOS pre-signed URL query, and certificate private key are never
logged or committed. The existing HTTP `train.xx.com` ingress remains unchanged;
a separate HTTPS ALB ingress serves `raytrain.wellspiking.ai`.

The wildcard certificate supplied by the platform owner is placed only in the
Kubernetes TLS Secret `raytrain-web-tls` in namespace `ray-train-platform`.
The internal DNS record for `raytrain.wellspiking.ai` must target the private
ALB VIP `172.28.6.171`; build-host acceptance may use explicit DNS resolution
until that record is in place.

## User experience

The Portal contains an “external submit” page with a direct link to the correct
platform binary and an install/checksum command. The user creates a PAT under
Account Security, then runs:

```text
rayctl login --server https://raytrain.wellspiking.ai --token-stdin
rayctl submit --dir . --name experiment-a --image <catalogue-digest> \
  --entrypoint 'torchrun train.py' --workers 2 --gpus-per-worker 8
```

The queue is omitted from the normal CLI path. The API derives the authenticated
tenant queue. A source directory is archived deterministically after applying
`.gitignore` and `.rayignore`, then uploaded only when its digest has not already
been materialized for that user. The CLI prints the job ID and Portal URL.

## Delivery topology

`backend/Dockerfile.rayctl` builds the signed-by-checksum Linux, macOS, and
Windows binaries. A small non-privileged nginx release image serves those files
from a dedicated `rayctl-release` deployment on the CPU control-plane nodes.
The HTTPS ingress maps `/downloads/rayctl/` to that service and all other paths
to the existing frontend. This keeps binary releases decoupled from the UI image
and gives the download service two replicas.

## Acceptance

1. The certificate/key pair is valid, matches, and the HTTPS listener presents
   the wildcard certificate for `raytrain.wellspiking.ai`.
2. A release binary is downloaded through HTTPS, its SHA256 is verified, and it
   is executable on the build host.
3. `rayctl login` writes a mode-0600 config and rejects unsafe server URLs.
4. A temporary local-platform user obtains a short-lived PAT, submits a local
   one-GPU source directory from the build host, reaches a terminal successful
   job state, and exposes logs and output only to that user.
5. Revoking the PAT causes subsequent CLI access to fail with an authentication
   response. Test user, PAT, job, and temporary local files are deleted after
   verification; durable training output follows the project retention policy.
