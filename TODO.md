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

### 11. No `status` command
- **Problem**: There's no way to check the health of an installed operator (deployment readiness, pod status, recent events) without using raw kubectl commands.
- **Files**: New `cmd/status.go`
- **Work**: Add `kubectl catalog status <package-name>` that shows deployment status, pod health, recent events, and CRD readiness

### 12. No `diff` before upgrade
- **Problem**: `upgrade` applies the new bundle without showing what will change. Users have no way to preview the diff between current and target versions.
- **Files**: `cmd/upgrade.go`
- **Work**: Add `--diff` flag that shows a diff of current vs new manifests before applying (similar to `kubectl diff`)

### 13. Cache has no expiry or size management
- **Problem**: Cached catalogs and bundles grow indefinitely. `clean` removes everything but there's no TTL-based expiry or size limit.
- **Files**: `cmd/clean.go`, `internal/registry/puller.go`
- **Work**:
  - Add `--max-age` flag to `clean` (e.g., remove entries older than 7 days)
  - Add `--max-size` to cap total cache size
  - Consider automatic cleanup of unused bundles after uninstall

### 14. No shell completion support
- **Problem**: No tab-completion for package names, channels, versions, or subcommands.
- **Files**: `cmd/root.go`
- **Work**: Add cobra's built-in completion generation (`rootCmd.GenBashCompletion`, `GenZshCompletion`, `GenFishCompletion`) and register `ValidArgsFunction` on subcommands that take package names

### 15. Error messages don't suggest next steps
- **Problem**: Errors like "package not found" or "channel not found" don't suggest what the user should do (e.g., "run `kubectl catalog search` to find available packages").
- **Files**: Various `cmd/*.go`
- **Work**: Add contextual hints to common error messages

### 16. `list --installed` shows stale data after partial uninstall
- **Problem**: If an uninstall partially fails, some resources still have tracking labels. `ListInstalled()` picks metadata from the first resource found, which may have outdated annotations.
- **Files**: `internal/state/manager.go`
- **Work**: Cross-reference all resources for a package and report inconsistencies, or prefer Deployment/CRD annotations over RBAC annotations

### 17. Multi-document YAML not supported in bundle manifests
- **Problem**: `bundle.Extract()` reads each file as a single YAML document. If a bundle contains multi-document YAML files (separated by `---`), only the first document is parsed.
- **Files**: `internal/bundle/extract.go`
- **Work**: Use a streaming YAML decoder (like `gopkg.in/yaml.v3` Decoder) to handle `---` separators, similar to how `catalog/loader.go` handles multi-document YAML

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

### 20. E2E test suite is a skeleton
- **Problem**: `test/e2e/e2e_test.go` exists but needs real test cases covering install, upgrade, uninstall, and generate workflows.
- **Files**: `test/e2e/`
- **Work**: Add e2e tests using kind/k3s that exercise the full install → verify → upgrade → uninstall lifecycle
