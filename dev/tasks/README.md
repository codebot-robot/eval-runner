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

For Kubernetes clusters running outside GKE (such as kOps on GCE, AWS EKS, or on-premises clusters), pods cannot access the GKE metadata server. In order for the `eval-runner` controller and UI to authenticate to GCP and access the artifact storage bucket, `./dev/tasks/setup-workload-identity-federation` sets up GCP Workload Identity Federation using uploaded cluster JWKS keys (no publicly reachable cluster OIDC endpoint required).

### Usage

```bash
./dev/tasks/setup-workload-identity-federation \
    --project <project-id> \
    --bucket <bucket-name> \
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
4. **Bucket IAM Bindings**: Grants direct principal bucket IAM permissions (no Google service accounts or impersonation):
   - `roles/storage.objectAdmin` to `system:serviceaccount:<namespace>:eval-runner-controller`
   - `roles/storage.objectViewer` to `system:serviceaccount:<namespace>:eval-runner-ui`
5. **Credential Configuration ConfigMap**: Applies `eval-runner-gcp-credentials` in `<namespace>` with:
   - `credential-config.json`: GCP STS external account configuration for exchanging projected tokens.
   - `GOOGLE_APPLICATION_CREDENTIALS`: Points to `/etc/eval-runner/gcp/credential-config.json`.

### ConfigMap Contract

The script defines the deployment manifest contract:
- The projected Kubernetes ServiceAccount token must be mounted at `/var/run/secrets/eval-runner/gcp/token`.
- The credential configuration from `credential-config.json` is mounted at `/etc/eval-runner/gcp/credential-config.json`.
- The environment variable `GOOGLE_APPLICATION_CREDENTIALS` is set to `/etc/eval-runner/gcp/credential-config.json`.

### ServiceAccount Signing-Key Rotation

Because the cluster's JWKS is read from the cluster and uploaded directly to GCP (`--jwk-json-path`), the provider trusts the uploaded public keys. When the Kubernetes cluster rotates its ServiceAccount signing keys, **`setup-workload-identity-federation` must be re-run** to refresh the uploaded keys in the GCP OIDC provider.

