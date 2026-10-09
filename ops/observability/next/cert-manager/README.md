# cert-manager PKI plan

This directory contains the additive cert-manager migration plan for the new
observability stack. It is not an online status record.

## Image source and mirror overlays

`versions.yaml` records the official Jetstack chart versions and source image
digests. `harbor-values.yaml` and `approver-policy-harbor-values.yaml` only
override image repository and tag so the VKE cluster can pull from Harbor when
`quay.io`/`cdn01.quay.io` times out.

The digest fields remain the official source digests. Before deployment, verify
that each Harbor mirror resolves to the same digest as the source image. If a
mirror digest differs, stop before Helm install or upgrade.

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
