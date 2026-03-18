# TODO — kubectl-catalog

Technical gaps and improvements identified from codebase analysis.

---

## High Priority

### ~~1. Self-signed certificate rotation and CA bundle injection~~ DONE
- `GenerateServingCert` now returns the CA PEM alongside cert and key
- `InjectCABundle()` patches `caBundle` on webhook configurations matching the service name
- Called automatically from `EnsureServingCerts`, `EnsureWebhookCert`, and `generateServingCertSecrets`
- **Remaining**: Consider adding `--cert-validity` flag or annotation-based renewal for long-lived clusters

### ~~2. Upgrade does not handle dependency changes~~ DONE
- Upgrade now resolves the full dependency tree of the target version via `res.Resolve()`
- Checks each dependency against currently installed packages via `stateManager.GetInstalled()`
- Installs new dependencies (with install mode, namespace, etc.) before upgrading the main package
- **Remaining**: Cleanup of dropped dependencies (orphan detection)

### ~~3. No pre-flight validation before install~~ DONE
- Added `Applier.Preflight()` method that checks:
  - Cluster connectivity (API server version query)
  - CRD conflicts (existing CRDs managed by a different package)
  - RBAC permissions (dry-run apply of a Deployment)
- Called from both `install` and `upgrade` commands before applying

### ~~4. `BuildIndexes()` not called after catalog load~~ DONE
- `LoadFromDirectory()` now calls `fbc.BuildIndexes()` before returning
- All `GetBundle()`, `GetPackage()`, and `ChannelsForPackage()` lookups are now O(1)

---

## Medium Priority

### ~~5. Uninstall does not clean up webhook configurations~~ DONE
- Added `CleanupWebhookConfigurations()` method to `Applier` that scans for `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` resources referencing services in the operator namespace
- Called automatically during uninstall after operational resources are removed

### ~~6. No `--wait` / `--no-wait` flag for deployment readiness~~ DONE
- Added `--no-wait` global flag to skip deployment readiness checks (useful for CI/CD pipelines)
- Added `--deployment-timeout` global flag to customize the deployment readiness timeout
- Both flags are passed via `applierOptions()` helper and respected by `waitForDeployments()`

### ~~7. No progress feedback during image pulls~~ DONE
- `pullAndExtract()` now prints the image reference being pulled and the total download size (in MB) before downloading
- Prints the extraction destination path after completion

### ~~8. `apply` command does not support `--env` flag~~ DONE
- Added `--env` flag to the `apply` command, calling `manifests.SetEnvVars()` before applying
- Consistent with `install`, `generate`, and `upgrade` subcommands

### ~~9. Serving cert secrets not tracked for uninstall~~ DONE
- `EnsureServingCerts()` and `EnsureWebhookCert()` now accept a `packageName` parameter
- TLS secrets are stamped with `kubectl-catalog.io/package` label so they are discovered by `state.ResourcesForPackage()` during uninstall
- Callers in `install.go` and `upgrade.go` pass the package name through

### ~~10. GVK dependency resolution picks arbitrary bundle version~~ DONE
- `findGVKProvider()` now collects all bundles providing the required GVK, groups them by package
- For each provider package, looks up the channel head of the default channel
- Prefers the channel head if it provides the GVK, falls back to first candidate otherwise

---

## Low Priority

### ~~11. No `status` command~~ DONE
- Added `cmd/status.go` with `kubectl catalog status <package-name>`
- Shows installed version, channel, catalog reference, namespace, resource counts
- Deployment status with ready/updated/available replicas and condition checks
- Pod health with phase, container readiness, restart counts, crash loop detection
- CRD status (Established / NotEstablished)
- Optional `--show-events` flag for recent events (last hour, max 10)
- Pod discovery falls back to ReplicaSet owner reference matching when labels are missing

### ~~12. No `diff` before upgrade~~ DONE
- Added `--diff` flag to `upgrade` command
- `showUpgradeDiff()` compares current cluster resources with new bundle manifests
- Categorizes resources as added (+), removed (-), changed (~), or unchanged
- `resourceDiffers()` compares spec/data/rules/roleRef/subjects/webhooks fields via YAML serialization
- Also compares tracking annotations and container images in Deployments
- Exits without applying when `--diff` is set

### 13. Cache has no expiry or size management
- **Problem**: Cached catalogs and bundles grow indefinitely. `clean` removes everything but there's no TTL-based expiry or size limit.
- **Files**: `cmd/clean.go`, `internal/registry/puller.go`
- **Work**:
  - Add `--max-age` flag to `clean` (e.g., remove entries older than 7 days)
  - Add `--max-size` to cap total cache size
  - Consider automatic cleanup of unused bundles after uninstall

### ~~14. No shell completion support~~ DONE
- Added `ValidArgsFunction` on `install`, `generate` (catalog package names), `upgrade`, `uninstall`, `status` (installed package names)
- Registered `RegisterFlagCompletionFunc` for `--catalog-type`, `--cluster-type`, and `--install-mode` with static value lists
- Cobra's built-in `completion` command provides `bash`, `zsh`, `fish`, and `powershell` generation

### ~~15. Error messages don't suggest next steps~~ DONE
- Added `withHint()` helper that appends `\nHint: ...` to error messages
- Hints added to: package not found, channel not found, not installed, no upgrade available, --ocp-version required, --pull-secret required
- Each hint suggests the relevant `kubectl catalog` command to run

### ~~16. `list --installed` shows stale data after partial uninstall~~ DONE
- Added `bestMetadataResource()` — selects annotations from the most authoritative resource type (Deployment > CRD > ClusterRole > ServiceAccount > other)
- Added `detectInconsistencies()` — checks all resources for version/channel annotation mismatches
- Added `Warning` field to `InstalledOperator`; displayed in `list --installed` output
- Both `GetInstalled()` and `ListInstalled()` use priority-based selection

### ~~17. Multi-document YAML not supported in bundle manifests~~ DONE
- Added `splitYAMLDocuments()` that splits on `\n---` boundaries
- Empty documents and comment-only documents are skipped
- `Extract()` now iterates over all documents in each file
- Error messages include the document index for easier debugging

### ~~18. `push` command Argo CD template uses wrong OCI URL format~~ DONE
- `push` command removed; OCI push functionality unified into `generate --output oci://...`
- `splitImageRef()` properly separates repository and tag for Argo CD / FluxCD templates
- `apply` command now accepts both local directories and `oci://` references
- Added `--push-secret` flag for OCI push authentication (separate from `--pull-secret`)

---

## Testing

### ~~19. No unit tests for new features~~ DONE
- Added tests for all previously untested functions:
  - `SetEnvVars()`, `SetImagePullSecrets()`, `InjectWebhookCertVolumes()` in `internal/bundle/extract_test.go`
  - `parseEnvVars()`, `partitionResources()`, `determineOperatorNamespace()` in `cmd/helpers_test.go`
  - `setSubjectNamespaces()` was already tested
  - Also added: `classifyResource()`, `isOCIOutput()`, `sanitizeOCITag()`, `splitImageRef()`, `resolveOCIRef()`, `ParseGVKQuery()`, `FindGVKProviders()`

### ~~20. E2E test suite is a skeleton~~ DONE
- Rewrote E2E suite to use `community` catalog (`registry.redhat.io/redhat/community-operator-index:v4.20`) which is publicly accessible without a pull secret
- Test operator: `argocd-operator` (bundle images on quay.io, no auth required)
- Coverage: help, version, completion (bash/zsh/fish), search (keyword + GVK), list (available + installed + channels), generate (basic + channel + namespace + install mode + error), generate-to-OCI (push + auto-tag + registry verify), apply, install/uninstall lifecycle (idempotency + --force + SingleNamespace), status (basic + --show-events + error hint), upgrade (basic + --diff), error handling and hints, clean
- Suite setup: kind cluster + local Docker registry for OCI tests
