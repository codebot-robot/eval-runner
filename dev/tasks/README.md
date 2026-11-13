# Development Tasks (`dev/tasks/`)

This directory contains automation and development task scripts.

> **Important**: `ap` automatically discovers executable scripts in `dev/tasks/` matching reserved prefixes:
> - `build-*`: Executed as build tasks during `ap build` and `ap deploy`.
> - `test-*`: Executed as test tasks during `ap test`.
> - `lint-*`: Executed as lint tasks during `ap lint`.
> - `test-e2e*`: Executed as end-to-end test tasks during `ap e2e`.
>
> New scripts added to `dev/tasks/` must pick prefixes deliberately. Standalone utility or maintenance scripts (such as `sandboxd-image` or `deploy-to-kube`) must use names outside these reserved prefixes so they are not run unexpectedly during every build or deploy cycle.

## Workload Identity Federation (`setup-workload-identity-federation`)

For Kubernetes clusters running outside GKE (such as kOps on GCE, AWS EKS, or on-premises clusters), pods cannot access the GKE metadata server. In order for the `eval-runner` controller and UI to authenticate to GCP and access the artifact storage bucket, cluster identity must be established with GCP Workload Identity Federation:
1. Run `./dev/tasks/setup-workload-identity-federation` first to configure the Workload Identity Pool, OIDC provider, and credential configuration ConfigMap using uploaded cluster JWKS keys (no publicly reachable cluster OIDC endpoint required).
2. Run `./dev/tasks/setup-artifact-bucket` next to create the artifact storage bucket and grant IAM permissions to the controller and UI ServiceAccounts.

### Usage

```bash
./dev/tasks/setup-workload-identity-federation \
    --project <project-id> \
    --pool <workload-identity-pool-id> \
    [--provider <provider-id>] \
    [--namespace <namespace>] \
    [--kubeconfig <path>] \
    [--dry-run]
```

### Steps Performed

1. **Read Cluster OIDC Issuer and JWKS**: Retrieves the OIDC issuer from `kubectl get --raw /.well-known/openid-configuration` (validating it is an `https://` URL) and signing keys from `kubectl get --raw /openid/v1/jwks`.
2. **Workload Identity Pool**: Ensures the global workload identity pool exists in the target GCP project (`gcloud iam workload-identity-pools create`).
3. **OIDC Provider**: Creates or updates the OIDC provider (`create-oidc` / `update-oidc`) with the uploaded JWKS, attribute mapping (`google.subject`, `attribute.namespace`, `attribute.service_account_name`), and allowed audiences (the provider's default audience plus the fixed audience `eval-runner.gke-labs.dev/gcp-workload-identity`).
4. **Credential Configuration ConfigMap**: Applies `eval-runner-gcp-credentials` in `<namespace>` with:
   - `credential-config.json`: GCP STS external account configuration for exchanging projected tokens.
   - `GOOGLE_APPLICATION_CREDENTIALS`: Points to `/etc/eval-runner/gcp/credential-config.json`.

### ConfigMap Contract

The script defines the deployment manifest contract:
- The projected Kubernetes ServiceAccount token must be mounted at `/var/run/secrets/eval-runner/gcp/token`.
- The credential configuration from `credential-config.json` is mounted at `/etc/eval-runner/gcp/credential-config.json`.
- The environment variable `GOOGLE_APPLICATION_CREDENTIALS` is set to `/etc/eval-runner/gcp/credential-config.json`.

### ServiceAccount Signing-Key Rotation

Because the cluster's JWKS is read from the cluster and uploaded directly to GCP (`--jwk-json-path`), the provider trusts the uploaded public keys. When the Kubernetes cluster rotates its ServiceAccount signing keys, **`setup-workload-identity-federation` must be re-run** to refresh the uploaded keys in the GCP OIDC provider.

## Artifact Storage Bucket Setup (`setup-artifact-bucket`)

`./dev/tasks/setup-artifact-bucket` creates the GCS artifact bucket (if absent, with uniform bucket-level access enabled) and grants direct principal IAM permissions to the `eval-runner` controller and UI Kubernetes ServiceAccounts in a given Workload Identity Pool.

On non-GKE clusters, run `setup-workload-identity-federation` first to establish the pool and OIDC provider, followed by `setup-artifact-bucket` to provision the bucket and bindings. On GKE clusters, `./dev/tasks/deploy-to-gke` invokes `setup-artifact-bucket` automatically using the cluster's GKE workload identity pool (`<project>.svc.id.goog`).

### Usage

```bash
./dev/tasks/setup-artifact-bucket \
    --project <project-id> \
    --bucket <bucket-name> \
    --location <bucket-location> \
    --workload-identity-pool <pool-id> \
    [--namespace <namespace>] \
    [--dry-run]
```

### Steps Performed

1. **Artifact Storage Bucket**: Ensures the bucket exists in `<location>` with uniform bucket-level access enabled (`gcloud storage buckets create`).
2. **Bucket IAM Bindings**: Grants direct principal bucket IAM permissions (no Google service accounts or impersonation):
   - `roles/storage.objectAdmin` to `system:serviceaccount:<namespace>:eval-runner-controller` (or `ns/<namespace>/sa/eval-runner-controller` for GKE pools)
   - `roles/storage.objectViewer` to `system:serviceaccount:<namespace>:eval-runner-ui` (or `ns/<namespace>/sa/eval-runner-ui` for GKE pools)

The principal subject format depends on the pool:
- GKE pools (`<project>.svc.id.goog`): `ns/<namespace>/sa/<name>`
- Other pools: `system:serviceaccount:<namespace>:<name>`

