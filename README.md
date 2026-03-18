# kubectl-catalog

A kubectl plugin that installs OLM operator bundles on vanilla Kubernetes clusters — without requiring OLM.

```
kubectl catalog search prometheus --ocp-version 4.20 --pull-secret ~/pull-secret.json
kubectl catalog search --what-provides argoproj.io/ArgoCD --ocp-version 4.20
kubectl catalog install cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json
kubectl catalog generate cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json
kubectl catalog generate my-operator -o oci://quay.io/myorg/my-operator --push-secret ~/creds.json
kubectl catalog apply ./cluster-logging-manifests
kubectl catalog apply oci://quay.io/myorg/my-operator:stable-v6.1
kubectl catalog list --installed
kubectl catalog upgrade cluster-logging
kubectl catalog uninstall cluster-logging
kubectl catalog clean
kubectl catalog version
```

## The Problem

The OLM operator ecosystem contains thousands of operators packaged as OLM bundles, organized in File-Based Catalogs (FBC). These operators cover monitoring, logging, security, storage, networking, and more.

To use any of them today, you need either:

- **OLM installed on your cluster** — a heavyweight controller stack (CRDs, catalog operator, OLM operator) that takes ownership of resource lifecycle
- **Helm charts** — which don't exist for most OLM operators, requiring someone to create and maintain a separate packaging format

Neither option is ideal for teams running vanilla Kubernetes (EKS, AKS, GKE, k3s) who want to use these operators without the operational overhead.

## The Solution

`kubectl-catalog` bridges the gap. It reads OLM catalogs and bundles from any OCI-compliant container registry, and applies them as plain Kubernetes resources. Nothing runs in your cluster — it's a CLI tool that operates like `kubectl apply`, then gets out of the way.

## Why Not Just Install OLM?

| Concern | OLM | kubectl-catalog |
|---|---|---|
| **Cluster footprint** | Multiple CRDs, controllers, and operators running continuously | Zero — nothing runs in the cluster |
| **Resource ownership** | OLM watches and reconciles resources; manual edits get reverted | Apply and walk away; you retain full control |
| **Operational model** | Create Subscription CR, CatalogSource CR, wait for reconciliation loop | One command: `kubectl catalog install <pkg>` |
| **Version control** | Approval policies, subscription configuration | Direct: `--version`, `--channel`, deterministic upgrade paths |
| **OpenShift coupling** | Designed for OpenShift; many operators have OCP-specific dependencies | Lets you inspect bundles and selectively install on any K8s cluster |

## Why Not Just Use Helm?

| Concern | Helm | kubectl-catalog |
|---|---|---|
| **Availability** | Helm charts don't exist for most OLM operators | Uses existing OLM bundles directly — no re-packaging needed |
| **Dependency resolution** | No built-in operator dependency resolution | Resolves `olm.package.required` and `olm.gvk.required` dependencies with semver constraints |
| **Upgrade semantics** | "Apply the new chart" — no upgrade safety | Honors `replaces`, `skips`, and `skipRange` upgrade edges to prevent incompatible upgrades |
| **Maintenance** | Someone must create and maintain separate Helm charts | Taps directly into operator authors' release stream |
| **Operator semantics** | Templates YAML | Understands CSVs: extracts deployments, fine-grained RBAC, API declarations |

## Features

- **Search** operators in OLM catalogs by name or keyword
- **List** available operators with channel details, or list what's installed in your cluster
- **Install** operators with full dependency resolution (package and GVK dependencies)
- **Generate + Apply** — two-step workflow to inspect and customize manifests before deploying
- **OCI GitOps** — push manifests as OCI artifacts for Argo CD v3.1+ and FluxCD, no Git repo required
- **Upgrade** operators following the catalog's upgrade graph (replaces/skips/skipRange)
- **Uninstall** operators immediately; CRDs preserved by default (use `--force` with confirmation to remove); cleans up pull secrets and managed namespaces
- **Phased apply** — CRDs first (wait for establishment), then RBAC, then Deployments (wait for rollout), then Services
- **Server-side apply** with field manager tracking
- **Annotation-based state tracking** — no ConfigMaps or custom CRDs; state lives on the resources themselves
- **Install modes** — AllNamespaces or SingleNamespace, auto-detected from the operator's CSV
- **Environment variable injection** — inject custom env vars into operator containers via `--env` (mirrors OLM Subscription `spec.config.env`)
- **Cluster type awareness** — auto-provisions self-signed serving certificates on vanilla Kubernetes (`--cluster-type k8s`)
- **Multiple catalog types** — Red Hat, Community, Certified, and OperatorHub catalogs
- **Pull secret support** — authenticate to private registries and provision credentials in the cluster (mandatory for Red Hat catalog images)
- **Docker/Podman auth** — uses your existing `~/.docker/config.json` credentials
- **Local caching** of catalog and bundle images for fast repeated operations with configurable cache directory
- **Cache management** — `clean` command to reclaim disk space

## Installation

### From source

```bash
go install github.com/anandf/kubectl-catalog@latest
```

### Build from source

```bash
git clone https://github.com/anandf/kubectl-catalog.git
cd kubectl-catalog
make build
```

This injects version, git commit, and build date into the binary via ldflags. You can also build manually with `go build -o kubectl-catalog .`.

Place the `kubectl-catalog` binary anywhere on your `$PATH`. kubectl automatically discovers it as a plugin:

```bash
# These are equivalent:
kubectl-catalog search prometheus
kubectl catalog search prometheus
```

It also works with `oc`:

```bash
oc catalog search prometheus
```

## Quick Start

### 1. Search for an operator

```bash
# Search the Red Hat OCP 4.20 catalog for logging operators
kubectl catalog search logging --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Search in the community catalog
kubectl catalog search logging --ocp-version 4.20 --catalog-type community --pull-secret ~/pull-secret.json

# Search the OperatorHub.io catalog (no pull secret needed)
kubectl catalog search logging --catalog-type operatorhub

# List all available operators
kubectl catalog list --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Show channel details
kubectl catalog list --ocp-version 4.20 --show-channels --pull-secret ~/pull-secret.json

# Use a custom catalog image
kubectl catalog search logging --catalog registry.example.com/my-catalog:latest

# Find operators that provide a specific CRD (GVK)
kubectl catalog search --what-provides argoproj.io/ArgoCD --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Search with full Group/Version/Kind
kubectl catalog search --what-provides argoproj.io/v1alpha1/ArgoCD --ocp-version 4.20

# Search by Kind only (broad search across all groups)
kubectl catalog search --what-provides ArgoCD --catalog-type operatorhub
```

### 2. Install an operator

```bash
# Install the latest version from the default channel
# The namespace is auto-detected from the operator's suggested-namespace annotation
kubectl catalog install cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Install into a specific namespace (overrides the operator's suggested namespace)
kubectl catalog install cluster-logging --ocp-version 4.20 -n my-namespace --pull-secret ~/pull-secret.json

# Install a specific version
kubectl catalog install prometheus --ocp-version 4.20 --version 0.65.1 --pull-secret ~/pull-secret.json

# Install from a specific channel
kubectl catalog install elasticsearch-operator --ocp-version 4.20 --channel stable-5.8 --pull-secret ~/pull-secret.json

# Install in single-namespace mode (operator watches only its own namespace)
kubectl catalog install my-operator --ocp-version 4.20 --install-mode SingleNamespace --pull-secret ~/pull-secret.json

# Install from OperatorHub.io (no pull secret needed) on vanilla k8s
kubectl catalog install my-operator --catalog-type operatorhub --cluster-type k8s

# Install with custom environment variables injected into operator containers
kubectl catalog install my-operator --ocp-version 4.20 --env "DISABLE_WEBHOOKS=true,LOG_LEVEL=debug" --pull-secret ~/pull-secret.json

# Install with a pull secret for private registries
kubectl catalog install my-operator --catalog registry.example.com/catalog:v1 --pull-secret ~/pull-secret.json
```

### 3. Generate and review manifests before applying

For a two-step workflow where you inspect (and optionally modify) manifests before deploying:

```bash
# Step 1: Generate manifests to a directory
kubectl catalog generate cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json

# This creates ./cluster-logging-manifests/ with:
#   _metadata.yaml                              — install context (package, version, catalog, etc.)
#   001-crd-clusterlogforwarder.yaml            — CRDs
#   002-rbac-serviceaccount-my-sa.yaml          — RBAC resources
#   003-deployment-cluster-logging-operator.yaml — Deployments
#   004-service-metrics.yaml                    — Services
#   tls-secret-secret-my-tls.yaml               — TLS secrets (vanilla k8s only)

# Step 2: Review and edit the generated manifests
# e.g., adjust resource limits, add annotations, remove unwanted resources

# Step 3: Apply the manifests to the cluster
kubectl catalog apply ./cluster-logging-manifests

# You can override the namespace during apply
kubectl catalog apply ./cluster-logging-manifests -n custom-namespace

# Specify a custom output directory
kubectl catalog generate cluster-logging --ocp-version 4.20 -o /tmp/my-manifests --pull-secret ~/pull-secret.json
```

### 4. Push to OCI registry for GitOps (Argo CD / FluxCD)

For cluster administrators who prefer not to use Git workflows, manifests can be pushed
as standard OCI artifacts and consumed directly by Argo CD or FluxCD. The `generate`
command supports pushing directly to an OCI registry via `--output oci://...`:

```bash
# Generate and push in one step
kubectl catalog generate cluster-logging --ocp-version 4.20 \
  --pull-secret ~/pull-secret.json \
  -o oci://registry.example.com/operators/cluster-logging:v5.8.1 \
  --push-secret ~/push-creds.json

# Or generate to local directory first, review, then push
kubectl catalog generate cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json
vim ./cluster-logging-manifests/003-deployment-cluster-logging-operator.yaml
kubectl catalog generate cluster-logging --ocp-version 4.20 \
  -o oci://registry.example.com/operators/cluster-logging \
  --pull-secret ~/pull-secret.json --push-secret ~/push-creds.json

# When no tag is specified, the resolved channel name is used automatically
# e.g. oci://registry.example.com/operators/cluster-logging:stable-5.9
```

The `generate` command prints ready-to-use configs for both Argo CD and FluxCD when pushing to OCI.

**Argo CD v3.1+** — configure an Application with an OCI source:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cluster-logging
  namespace: argocd
spec:
  source:
    repoURL: oci://registry.example.com/operators/cluster-logging
    targetRevision: v5.8.1
    path: .
  destination:
    server: https://kubernetes.default.svc
    namespace: openshift-logging
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

**FluxCD** — use an OCIRepository source:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: cluster-logging
  namespace: flux-system
spec:
  interval: 5m
  url: oci://registry.example.com/operators/cluster-logging
  ref:
    tag: v5.8.1
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: cluster-logging
  namespace: flux-system
spec:
  interval: 5m
  sourceRef:
    kind: OCIRepository
    name: cluster-logging
  targetNamespace: openshift-logging
  prune: true
```

To apply from an OCI artifact directly:

```bash
# Apply directly from OCI registry
kubectl catalog apply oci://registry.example.com/operators/cluster-logging:v5.8.1

# Or pull manually with ORAS CLI and apply from local directory
oras pull registry.example.com/operators/cluster-logging:v5.8.1 -o ./manifests
kubectl catalog apply ./manifests
```

### 5. Check what's installed

```bash
kubectl catalog list --installed
```

Output:
```
PACKAGE                 VERSION   CHANNEL   RESOURCES   CATALOG
cluster-logging         5.8.1     stable    12          registry.example.com/catalog:v4.20
elasticsearch-operator  5.8.0     stable    8           registry.example.com/catalog:v4.20
```

### 6. Upgrade an operator

```bash
# Upgrade to the latest version in the current channel
kubectl catalog upgrade cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Switch to a different channel during upgrade
kubectl catalog upgrade cluster-logging --ocp-version 4.20 --channel stable-6.0 --pull-secret ~/pull-secret.json
```

### 7. Uninstall an operator

```bash
# Uninstall (preserves CRDs and custom resources by default)
kubectl catalog uninstall cluster-logging

# Uninstall and remove CRDs + custom resources (prompts to type "yes" before CRD deletion)
kubectl catalog uninstall cluster-logging --force
```

### 8. Check version

```bash
kubectl catalog version
```

Output:
```
kubectl-catalog version 0.0.1
  git commit: 05fed11
  build date: 2026-03-14T07:16:17Z
  go version: go1.26.0
```

### 9. Manage cache

```bash
# Remove all cached catalogs and bundles
kubectl catalog clean

# Remove only cached catalogs
kubectl catalog clean --catalogs

# Remove only cached bundles
kubectl catalog clean --bundles
```

## Global Flags

| Flag | Description |
|---|---|
| `--ocp-version` | OCP version to derive the catalog image (e.g., `4.20`) |
| `--catalog` | Catalog image override (takes precedence over `--catalog-type` and `--ocp-version`) |
| `--catalog-type` | Catalog type: `redhat` (default), `community`, `certified`, `operatorhub` |
| `--cluster-type` | Target cluster type: `k8s` (default), `ocp`, `okd` |
| `--kubeconfig` | Path to kubeconfig file (defaults to `$KUBECONFIG` or `~/.kube/config`) |
| `-n, --namespace` | Target namespace for operator installation (default: auto-detected from CSV or `default`) |
| `--cache-dir` | Directory for caching catalog and bundle images (default: `~/.kubectl-catalog`) |
| `--refresh` | Force re-pull of cached catalog images |
| `--pull-secret` | Path to a pull secret file for registry authentication (mandatory for Red Hat catalog images; download from https://console.redhat.com/openshift/install/pull-secret) |
| `--timeout` | Maximum time for the operation (default: `30m`, e.g. `10m`, `1h`) |
| `--dry-run` | Show what would be applied without making changes to the cluster |

### List Flags

| Flag | Description |
|---|---|
| `--installed` | List installed operators (discovered from cluster annotations) |
| `--show-channels` | Show channel details for each package |
| `--limit-channels` | Limit number of channels shown per package (0 = no limit) |

### Install / Generate / Upgrade Flags

| Flag | Description |
|---|---|
| `--install-mode` | Install mode: `AllNamespaces`, `SingleNamespace`, `OwnNamespace` (defaults to the operator's preferred mode from the CSV) |
| `--channel` | Channel to install from or switch to during upgrade |
| `--version` | Specific version to install/generate (install and generate only) |
| `--env` | Comma-separated environment variables to inject into all operator containers (e.g. `KEY1=val1,KEY2=val2`). Mirrors OLM Subscription `spec.config.env`. |
| `--force` | Force re-install if already installed (install only) |
| `-o, --output` | Output destination: local directory or `oci://` registry reference (generate only; defaults to `./<package-name>-manifests`). When using `oci://`, if no tag is specified, the resolved channel name or `v<version>` is used automatically. |
| `--push-secret` | Path to a credentials file for OCI push authentication (generate only; used with `oci://` output) |

### Uninstall Flags

| Flag | Description |
|---|---|
| `--force` | Also remove CRDs and custom resources (requires typing `yes` to confirm CRD deletion) |

### Search Flags

| Flag | Description |
|---|---|
| `--what-provides` | Find operators providing a specific GVK/CRD (e.g. `argoproj.io/ArgoCD`, `argoproj.io/v1alpha1/ArgoCD`, or just `ArgoCD`) |

## Catalog Types

`kubectl-catalog` supports multiple operator catalog sources via the `--catalog-type` flag:

| Type | Registry | Pull Secret | Description |
|---|---|---|---|
| `redhat` (default) | `registry.redhat.io` | **Required** | Red Hat certified operators |
| `community` | `registry.redhat.io` | **Required** | Community operators from the Red Hat catalog |
| `certified` | `registry.redhat.io` | **Required** | ISV-certified operators from the Red Hat catalog |
| `operatorhub` | `quay.io` | Not required | Community operators from [OperatorHub.io](https://operatorhub.io) |

For Red Hat catalog types (`redhat`, `community`, `certified`), `--ocp-version` is required to select the catalog version and `--pull-secret` is mandatory since `registry.redhat.io` requires authentication.

The `operatorhub` catalog uses `quay.io` which is publicly accessible, so neither `--ocp-version` nor `--pull-secret` is required.

You can bypass catalog type resolution entirely by providing a full image reference with `--catalog`:

```bash
kubectl catalog search logging --catalog my-registry.example.com/my-catalog:v1.0
```

## Cluster Types

The `--cluster-type` flag tells `kubectl-catalog` what kind of cluster it's targeting:

| Type | Description |
|---|---|
| `k8s` (default) | Vanilla Kubernetes (EKS, AKS, GKE, k3s, kind, etc.) |
| `ocp` | OpenShift Container Platform |
| `okd` | OKD (community distribution of Kubernetes) |

This matters because many OLM operators assume OpenShift-specific infrastructure. On vanilla Kubernetes (`--cluster-type k8s`), `kubectl-catalog` automatically:

- **Generates self-signed serving certificates** for services that use the OpenShift `service.beta.openshift.io/serving-cert-secret-name` annotation. On OCP, the service-ca-operator handles this automatically, but on vanilla k8s these TLS secrets would be missing, causing pods to fail with `MountVolume.SetUp failed`. `kubectl-catalog` creates an ECDSA P256 CA and serving certificate with the correct DNS SANs (`<service>.<namespace>.svc`, etc.) and applies the resulting `kubernetes.io/tls` Secret.

On `ocp` and `okd`, these adjustments are skipped since the platform handles them natively.

## Install Modes

Operators declare which install modes they support in their CSV (`spec.installModes`). `kubectl-catalog` respects these declarations:

| Mode | Behavior |
|---|---|
| `AllNamespaces` | Operator watches all namespaces on the cluster |
| `SingleNamespace` | Operator watches only the target namespace |
| `OwnNamespace` | Operator watches only its own installation namespace |

When `--install-mode` is not specified, the default is **automatically derived** from the operator's CSV:
1. If the operator supports `AllNamespaces`, that is used
2. Otherwise, `SingleNamespace` is preferred
3. Failing that, `OwnNamespace` is used

The install mode controls the `WATCH_NAMESPACE` environment variable injected into operator deployment containers:
- `AllNamespaces`: `WATCH_NAMESPACE=""` (empty — watches everything)
- `SingleNamespace` / `OwnNamespace`: `WATCH_NAMESPACE=<target-namespace>`

If you request an install mode that the operator doesn't support, the command fails with a clear error listing the supported modes.

### Automatic Namespace Detection

Many operators declare a preferred installation namespace via the `operatorframework.io/suggested-namespace` CSV annotation (e.g., `openshift-gitops-operator`). When `--namespace` is not explicitly specified, `kubectl-catalog` automatically:

1. Reads the suggested namespace from the CSV annotation
2. Creates the namespace if it doesn't exist
3. Installs the operator into that namespace

You can always override this by specifying `--namespace` explicitly.

### Environment Variable Injection

The `--env` flag injects custom environment variables into all containers of the operator's Deployments. This mirrors the OLM Subscription `spec.config.env` field, allowing you to configure operator behavior without modifying the bundle manifests.

```bash
kubectl catalog install my-operator --ocp-version 4.20 \
  --env "DISABLE_WEBHOOKS=true,LOG_LEVEL=debug,HTTP_PROXY=http://proxy:8080" \
  --pull-secret ~/pull-secret.json
```

The format is comma-separated `KEY=VALUE` pairs. If a variable with the same name already exists in the container spec, its value is replaced. This is available on `install`, `generate`, and `upgrade` commands.

## How It Works

### Architecture

```
┌──────────────┐     ┌───────────────┐     ┌──────────────┐
│  OCI Registry │────▶│ Catalog (FBC) │────▶│   Resolver   │
│               │     │    Parser     │     │  (deps, ver) │
└──────────────┘     └───────────────┘     └──────┬───────┘
                                                   │
┌──────────────┐     ┌───────────────┐     ┌──────▼───────┐
│  Kubernetes  │◀────│   Applier     │◀────│    Bundle    │
│   Cluster    │     │ (phased SSA)  │     │  Extractor   │
└──────────────┘     └───────────────┘     └──────────────┘
```

1. **Catalog loading** — Pulls the FBC container image from the registry, extracts and caches it locally. Parses `olm.package`, `olm.channel`, and `olm.bundle` JSON entries.

2. **Resolution** — Finds the target bundle by package/channel/version. Resolves `olm.package.required` dependencies (with semver range matching) and `olm.gvk.required` dependencies (finds bundles that provide the required GVK).

3. **Bundle extraction** — Pulls the bundle image, extracts manifests from the `manifests/` directory. Converts ClusterServiceVersions (CSVs) into standalone Kubernetes resources: Deployments, ServiceAccounts, ClusterRoles, ClusterRoleBindings, Roles, RoleBindings.

4. **Phased apply** — Applies resources in dependency order using server-side apply:
   - CRDs → wait for Established condition
   - Refresh REST mapper (so new CRD types are discoverable)
   - RBAC (ServiceAccounts, Roles, Bindings)
   - Deployments → wait for rollout complete
   - Services and other resources

5. **State tracking** — Every applied resource is stamped with labels and annotations:
   - `app.kubernetes.io/managed-by: kubectl-catalog`
   - `kubectl-catalog.io/package: <name>`
   - `kubectl-catalog.io/version: <version>`
   - `kubectl-catalog.io/channel: <channel>`
   - `kubectl-catalog.io/catalog: <image-ref>`

   This enables discovery of installed state without any external database or CRD.

### Upgrade Strategy

Upgrades follow the OLM channel upgrade graph. When you run `kubectl catalog upgrade`, the tool:

1. Discovers the currently installed version from resource annotations
2. Loads the catalog and finds the channel head (latest version)
3. Walks the upgrade graph (BFS through `replaces`, `skips`, `skipRange` edges) to verify a valid upgrade path exists
4. Applies only the target bundle — intermediate versions are skipped because each OLM bundle is designed to handle upgrades from any version it replaces/skips

### Uninstall Safety

`kubectl catalog uninstall` immediately removes operational resources (Deployments, RBAC, Services, etc.) without confirmation — these are safe to remove and can always be re-created by reinstalling.

CRDs and their custom resource instances are **preserved by default** to protect user data. Use `--force` to also remove CRDs and custom resources. Since deleting a CRD permanently destroys **all** custom resource instances of that type across the entire cluster, this triggers a confirmation prompt requiring you to type `yes` before proceeding.

### Uninstall Cleanup

During uninstall, `kubectl-catalog` performs a complete cleanup:

1. **Operational resources** — Deployments, RBAC, Services, and other resources are deleted
2. **Pull secret** — the `<package-name>-pull-secret` Secret is removed from the target namespace
3. **Untracked resources** — any resources from the bundle manifests that weren't found via tracking annotations are cleaned up
4. **Namespace** — if the operator was installed into a suggested namespace (e.g. `openshift-logging`) and that namespace was created by `kubectl-catalog`, it is deleted after all resources are removed. System namespaces (`default`, `kube-system`, `kube-public`, `kube-node-lease`) are never deleted.

## Cache

Catalog and bundle images are cached locally at `~/.kubectl-catalog/` by default. Use `--cache-dir` to change the location (useful when the home directory has limited disk space):

```bash
kubectl catalog install my-operator --cache-dir /data/kubectl-catalog --ocp-version 4.20 --pull-secret ~/pull-secret.json
```

```
~/.kubectl-catalog/
├── catalogs/        # Extracted FBC catalog images
│   └── registry.example.com-catalog-v4.20/
└── bundles/         # Extracted bundle images
    └── registry.example.com-operator-bundle-v1.0.0/
```

Use `--refresh` to force re-pulling a catalog image:

```bash
kubectl catalog list --ocp-version 4.20 --refresh --pull-secret ~/pull-secret.json
```

Use `kubectl catalog clean` to reclaim disk space:

```bash
# Remove all cached data
kubectl catalog clean

# Remove only catalogs or bundles
kubectl catalog clean --catalogs
kubectl catalog clean --bundles
```

## Authentication

### Default: Docker keychain

`kubectl-catalog` uses the default Docker keychain for registry authentication. Credentials are picked up from:

- `~/.docker/config.json`
- `docker login` / `podman login` stored credentials
- Credential helpers configured in Docker config

```bash
docker login registry.example.com
# or
podman login registry.example.com
```

### Pull Secret

For private or authenticated registries, provide a pull secret file via `--pull-secret`. This flag is **mandatory** when using Red Hat catalog images (`registry.redhat.io`), which includes the `redhat`, `community`, and `certified` catalog types.

You can download your Red Hat pull secret from: https://console.redhat.com/openshift/install/pull-secret

```bash
# Required for Red Hat catalogs
kubectl catalog install cluster-logging --ocp-version 4.20 --pull-secret ~/pull-secret.json

# Optional for custom registries
kubectl catalog install my-operator --catalog registry.example.com/catalog:v1 --pull-secret ~/pull-secret.json

# Not needed for OperatorHub.io
kubectl catalog install my-operator --catalog-type operatorhub
```

When `--pull-secret` is provided, `kubectl-catalog` does three things:

1. **Uses the pull secret for image pulls** — the pull secret credentials are used to authenticate when pulling catalog and bundle images from the registry (with fallback to the default Docker keychain for registries not in the pull secret)

2. **Creates a Kubernetes Secret in the target namespace** — a `kubernetes.io/dockerconfigjson` Secret named `<package-name>-pull-secret` is created (or updated) so that the cluster has the credentials available (e.g., `cluster-logging-pull-secret` for the `cluster-logging` package)

3. **Patches ServiceAccounts with `imagePullSecrets`** — all ServiceAccounts are patched to reference the pull secret, ensuring that operator pods can pull their container images without `ImagePullBackOff` errors. This includes:
   - The `default` ServiceAccount in the target namespace
   - All ServiceAccounts created by the bundle's RBAC manifests (from CSV permissions)
   - Any ServiceAccounts referenced by Deployments via `serviceAccountName` in the pod template

This means a single `--pull-secret` flag handles both the CLI-side authentication (pulling catalogs and bundles) and the cluster-side authentication (operator pods pulling their images at runtime).

During uninstall, the pull secret is automatically removed from the target namespace.

The pull secret is a standard Docker config JSON file:

```json
{
  "auths": {
    "registry.example.com": {
      "auth": "base64-encoded-credentials"
    },
    "quay.io": {
      "auth": "base64-encoded-credentials"
    }
  }
}
```

## Project Structure

```
├── main.go                    # Entry point
├── Makefile                   # Build with ldflags (version, git commit, date)
├── VERSION                    # Semantic version (e.g. 0.0.1)
├── cmd/                       # CLI commands (cobra)
│   ├── root.go                # Global flags, catalog/cluster type resolution
│   ├── search.go              # Search operators by keyword or GVK (--what-provides)
│   ├── list.go                # List available/installed operators
│   ├── install.go             # Install with dependency resolution
│   ├── generate.go            # Generate manifests to directory or OCI registry
│   ├── apply.go               # Apply manifests from local directory or OCI artifact
│   ├── upgrade.go             # Upgrade via channel upgrade graph
│   ├── uninstall.go           # Uninstall with CRD/CR protection
│   ├── clean.go               # Cache cleanup
│   └── version.go             # Version and build info
├── internal/
│   ├── applier/               # Phased server-side apply, readiness checks
│   ├── bundle/                # Bundle extraction, CSV conversion, install modes
│   ├── catalog/               # FBC parsing, caching, type definitions
│   ├── certs/                 # Self-signed serving certificate generation (vanilla k8s)
│   ├── registry/              # Image pulling (go-containerregistry), tar extraction
│   ├── resolver/              # Dependency resolution, upgrade graph BFS
│   ├── state/                 # Annotation-based installed state discovery
│   └── util/                  # Shared utilities
```

## Contributing

Contributions are welcome! Please feel free to submit issues and pull requests.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/my-feature`)
3. Run tests (`go test ./...`)
4. Commit your changes
5. Push to the branch and open a Pull Request

## License

This project is licensed under the Apache License 2.0 — see the [LICENSE](LICENSE) file for details.

You are free to use, modify, and distribute this software. Contributions back to the project are appreciated but not required.
