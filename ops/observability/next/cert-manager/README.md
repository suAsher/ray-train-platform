# cert-manager PKI plan

This directory contains the additive cert-manager migration plan for the new
observability stack. It is not an online status record.

## Image source and mirror overlays

`versions.yaml` records the official Jetstack chart versions and source image
digests. `harbor-values.yaml` and `approver-policy-harbor-values.yaml` override
image repository, tag, and digest so the VKE cluster can pull from Harbor when
`quay.io`/`cdn01.quay.io` times out.

For cert-manager controller, webhook, cainjector, startupapicheck, and
acmesolver, the Harbor overlays keep the official multi-arch source index
digest. Before deployment, verify that each Harbor mirror resolves to the same
index digest as the source image.

For approver-policy, the Harbor overlay intentionally pins only the official
`linux/amd64` child image because copying the full multi-arch index timed out.
Before deployment, verify both conditions:

- the child digest
  `sha256:e6547e9ff58c3fcb1bc70ef9f42dfe9297de385cfd22aa1d6754f06cb50d7cc9`
  is a member of the official source index
  `sha256:1a955b0a5c41d319fd56b5f85aaff6f2eaba01d70f3b1adf001eac4d7b1d49e5`;
- `harbor.wellspiking.ai/guofeng.su/cert-manager-approver-policy:v0.28.0-amd64-e6547e9f`
  resolves to that same `linux/amd64` child digest.

If any mirror digest differs, stop before Helm install or upgrade.

## Staged deployment procedure

1. Render and review the namespace/quota bootstrap:
   `python3 render.py --part bootstrap`.
2. Install cert-manager with the base values and Harbor overlay:
   `helm upgrade --install cert-manager jetstack/cert-manager -n cert-manager -f cert-manager-values.yaml -f harbor-values.yaml`.
3. Install approver-policy with the base values and Harbor overlay:
   `helm upgrade --install cert-manager-approver-policy jetstack/cert-manager-approver-policy -n cert-manager -f approver-policy-values.yaml -f approver-policy-harbor-values.yaml`.
4. Import CA Secrets from the private builder PKI directory with
   `import_ca_bundle.py`. The import path preflights target absence, validates
   CA key/cert pairs, checks `CA:TRUE` and expiry, and creates Secrets through
   stdin. It does not print private key or certificate bodies.
5. Render and apply ClusterIssuers:
   `python3 render.py --part issuers`.
6. Render and apply identity Certificates, CertificateRequestPolicies, and
   policy-use RBAC:
   `python3 render.py --part identities`.
7. Render `public-ca-plan`, inject only public CA data from the corresponding
   cert-manager CA Secrets, then apply those public CA projection Secrets.
8. Render and review the CNPG certificate patch:
   `python3 render.py --part postgres-patch`.

Run server-side dry-run and diff before each apply. This plan does not replace
the old observability stack, does not enable NetworkPolicy, and does not claim
deployment completion.

## Security rationale

The CA private keys live only in the `cert-manager` namespace. Workload
namespaces receive leaf certificates and explicit public CA projections, not CA
private keys.

The cert-manager default CertificateRequest approver is disabled. Approval is
limited by approver-policy in two layers:

- chart-level `app.approveSignerNames` limits approver-policy to the five
  expected ClusterIssuers;
- each `CertificateRequestPolicy` matches one request namespace, one issuer,
  exact URI, exact allowed DNS set, usages, duration, and key shape.

Approver-policy allows a request to ask for a subset of the allowed SANs, but
not a value outside the allowlist. Normal Certificates rendered here request the
complete DNS and URI set.

## Rotation and consumers

cert-manager automates leaf certificate renewal. Root CA rotation remains a
separate maintenance workflow with dual-trust staging and explicit cutover.

Kubernetes updates mounted Secret files eventually, but that does not guarantee
every process reloads TLS material. CNPG-related Secrets add `cnpg.io/reload`
for CNPG reconciliation:

- `obs-mtls-n9e-postgres-server`
- `obs-mtls-n9e-postgres-replication`
- public CA projection shells

Other consumers still need component-specific reload or rolling restart
validation before the old one-shot certificate flow is retired.

## Negative policy acceptance

After ClusterIssuers, CertificateRequestPolicies, policy-use RBAC, and all
normal leaf Certificates are Ready, run the negative acceptance helper in
read-only mode first:

`python3 negative_certificate_acceptance.py`

The helper checks the expected kube context, verifies that
`obs-mtls-postgres-server`, `obs-mtls-nightingale-n9e-postgres-server`, and the
normal Nightingale PostgreSQL server Certificate are Ready, then prints the
three temporary negative cases.

Only root should run the write path:

`python3 negative_certificate_acceptance.py --execute`

The write path creates temporary `Certificate` objects so cert-manager creates
the corresponding `CertificateRequest` objects through the same controller path
as production issuance. It verifies:

- wrong URI is Denied and no Secret is created;
- wrong DNS name is Denied and no Secret is created;
- wrong namespace uses `victoriametrics`, confirms no matching
  postgres-server policy, stays `pending_no_matching_policy` for at least 45
  seconds, and creates no Secret.

Cleanup is limited to the current run's temporary Certificates, their owned
CertificateRequests, and the exact temporary Secret names. The helper does not
print private keys or Secret data.

Approver-policy only approves or denies requests that are selected by an
applicable `CertificateRequestPolicy`. A wrong-namespace request that matches no
policy may remain neither Approved nor Denied. The helper treats that fail-closed
state as the expected wrong-namespace result and still requires the controller to
create a CertificateRequest, cert-manager deployments to be Available, no
matching postgres-server policy for `victoriametrics`, and no Secret.
