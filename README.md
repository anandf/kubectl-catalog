# kubectl-catalog

A kubectl plugin that installs OLM operator bundles on vanilla Kubernetes clusters — without requiring OLM.

```
kubectl catalog search prometheus --ocp-version 4.20
kubectl catalog install cluster-logging --ocp-version 4.20
kubectl catalog list --installed
kubectl catalog upgrade cluster-logging
kubectl catalog uninstall cluster-logging
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
- **Upgrade** operators following the catalog's upgrade graph (replaces/skips/skipRange)
- **Uninstall** operators with CRD/CR protection by default (use `--force` to remove data)
- **Phased apply** — CRDs first (wait for establishment), then RBAC, then Deployments (wait for rollout), then Services
- **Server-side apply** with field manager tracking
- **Annotation-based state tracking** — no ConfigMaps or custom CRDs; state lives on the resources themselves
- **Pull secret support** — authenticate to private registries and provision credentials in the cluster
- **Docker/Podman auth** — uses your existing `~/.docker/config.json` credentials
- **Local caching** of catalog and bundle images for fast repeated operations

## Installation

### From source

```bash
go install github.com/anandf/kubectl-catalog@latest
```

### Build from source

```bash
git clone https://github.com/anandf/kubectl-catalog.git
cd kubectl-catalog
go build -o kubectl-catalog .
```

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
# Search the OCP 4.20 catalog for logging operators
kubectl catalog search logging --ocp-version 4.20

# List all available operators
kubectl catalog list --ocp-version 4.20

# Show channel details
kubectl catalog list --ocp-version 4.20 --show-channels

# Use a custom catalog image
kubectl catalog search logging --catalog registry.example.com/my-catalog:latest
```

### 2. Install an operator

```bash
# Install the latest version from the default channel
kubectl catalog install cluster-logging --ocp-version 4.20 -n operators

# Install a specific version
kubectl catalog install prometheus --ocp-version 4.20 --version 0.65.1

# Install from a specific channel
kubectl catalog install elasticsearch-operator --ocp-version 4.20 --channel stable-5.8

# Install with a pull secret for private registries
kubectl catalog install my-operator --catalog registry.example.com/catalog:v1 --pull-secret ~/pull-secret.json
```

### 3. Check what's installed

```bash
kubectl catalog list --installed
```

Output:
```
PACKAGE                 VERSION   CHANNEL   RESOURCES   CATALOG
cluster-logging         5.8.1     stable    12          registry.example.com/catalog:v4.20
elasticsearch-operator  5.8.0     stable    8           registry.example.com/catalog:v4.20
```

### 4. Upgrade an operator

```bash
# Upgrade to the latest version in the current channel
kubectl catalog upgrade cluster-logging --ocp-version 4.20

# Switch to a different channel during upgrade
kubectl catalog upgrade cluster-logging --ocp-version 4.20 --channel stable-6.0
```

### 5. Uninstall an operator

```bash
# Uninstall (preserves CRDs and custom resources by default)
kubectl catalog uninstall cluster-logging

# Uninstall and remove CRDs + custom resources (with confirmation)
kubectl catalog uninstall cluster-logging --force

# Skip the initial confirmation prompt
kubectl catalog uninstall cluster-logging --yes
```

## Global Flags

| Flag | Description |
|---|---|
| `--ocp-version` | OCP version to derive the catalog image (e.g., `4.20`) |
| `--catalog` | Catalog image override (takes precedence over `--ocp-version`) |
| `--kubeconfig` | Path to kubeconfig file (defaults to `$KUBECONFIG` or `~/.kube/config`) |
| `-n, --namespace` | Target namespace for operator installation (default: `default`) |
| `--refresh` | Force re-pull of cached catalog images |
| `--pull-secret` | Path to a pull secret file for registry authentication (see below) |

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

By default, `kubectl catalog uninstall` preserves CRDs and their custom resource instances to protect user data. Only operational resources (Deployments, RBAC, Services, etc.) are removed.

Use `--force` to also remove CRDs and custom resources. This triggers a separate confirmation prompt that explicitly lists what will be deleted, requiring the user to acknowledge data destruction.

## Cache

Catalog and bundle images are cached locally at `~/.kubectl-catalog/`:

```
~/.kubectl-catalog/
├── catalogs/        # Extracted FBC catalog images
│   └── registry.example.com-catalog-v4.20/
└── bundles/         # Extracted bundle images
    └── registry.example.com-operator-bundle-v1.0.0/
```

Use `--refresh` to force re-pulling a catalog image:

```bash
kubectl catalog list --ocp-version 4.20 --refresh
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

For private or authenticated registries, you can provide a pull secret file via `--pull-secret`:

```bash
kubectl catalog install my-operator --catalog registry.example.com/catalog:v1 --pull-secret ~/pull-secret.json
```

When `--pull-secret` is provided, `kubectl-catalog` does three things:

1. **Uses the pull secret for image pulls** — the pull secret credentials are used to authenticate when pulling catalog and bundle images from the registry (with fallback to the default Docker keychain for registries not in the pull secret)

2. **Creates a Kubernetes Secret in the target namespace** — a `kubernetes.io/dockerconfigjson` Secret named `kubectl-catalog-pull-secret` is created (or updated) so that the cluster has the credentials available

3. **Patches ServiceAccounts with `imagePullSecrets`** — all ServiceAccounts created by the operator bundle are patched to reference the pull secret, ensuring that operator pods can pull their container images without `ImagePullBackOff` errors

This means a single `--pull-secret` flag handles both the CLI-side authentication (pulling catalogs and bundles) and the cluster-side authentication (operator pods pulling their images at runtime).

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
├── cmd/                       # CLI commands (cobra)
│   ├── root.go                # Global flags, catalog image resolution
│   ├── search.go              # Search operators by keyword
│   ├── list.go                # List available/installed operators
│   ├── install.go             # Install with dependency resolution
│   ├── upgrade.go             # Upgrade via channel upgrade graph
│   └── uninstall.go           # Uninstall with CRD/CR protection
├── pkg/
│   ├── applier/               # Phased server-side apply, readiness checks
│   ├── bundle/                # Bundle extraction, CSV conversion
│   ├── catalog/               # FBC parsing, caching, type definitions
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
