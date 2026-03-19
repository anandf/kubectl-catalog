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

---

## Comprehensive Review (March 2026)

The following gaps were identified from a full codebase review covering all cmd/, internal/, test/, and project infrastructure files.

---

### High Priority — Bugs & Safety

### ~~21. Nil pointer dereference in `bestMetadataResource()`~~ DONE
- Added `if len(resources) == 0 { return nil }` guard at the top of `bestMetadataResource()` in `internal/state/manager.go`
- Callers (`GetInstalled`, `ListInstalled`) already guard against empty slices before calling, but the function is now safe independently

### ~~22. Upgrade has no rollback for failed dependency installs~~ DONE
- Tracked newly installed dependencies in `cmd/upgrade.go` via `newlyInstalledDeps` slice
- If the main operator upgrade (`Apply`) fails, all newly installed dependencies are rolled back in reverse order
- Rollback uses a bounded context (2-minute timeout) consistent with install rollback (item 23)

### ~~23. Install rollback uses unbounded context~~ DONE
- Replaced `context.Background()` with `context.WithTimeout(context.Background(), 2*time.Minute)` for rollback operations in `cmd/install.go`
- Rollback will now time out after 2 minutes if the cluster is unresponsive

### ~~24. CRD/Deployment wait loops don't respect context cancellation~~ DONE
- Replaced all `time.Sleep(pollInterval)` calls in `waitForCRDs()` and `waitForDeployments()` with `select { case <-ctx.Done(): return ctx.Err() case <-time.After(pollInterval): }`
- Both functions now return immediately with `ctx.Err()` when the parent context is cancelled

### ~~25. Uninstall cleanup race condition with unavailable bundles~~ DONE
- Enhanced warning messages in `cleanupUntrackedResources()` when bundle pull or extraction fails
- Now lists potentially orphaned resource types (CRDs, ClusterRoles, ClusterRoleBindings, webhook configs)
- Suggests manual cleanup command: `kubectl get crd,clusterrole,clusterrolebinding -l kubectl-catalog.io/package=<name>`

---

### Medium Priority — Robustness & Correctness

### 26. No CI/CD pipeline
- **Problem**: No GitHub Actions, GitLab CI, or any automated pipeline. Tests are only run manually via `make test` / `make test-e2e`.
- **Files**: Missing `.github/workflows/`
- **Work**:
  - Add a CI workflow: lint (`golangci-lint`), vet, unit tests on push/PR
  - Add a release workflow: multi-platform build, checksums, GitHub Release creation
  - Consider GoReleaser (`.goreleaser.yml`) for automated release with changelogs

### 27. No linting or static analysis in Makefile
- **Problem**: Makefile has `test` and `test-e2e` targets but no `lint`, `vet`, `fmt-check`, or `security-scan` targets.
- **Files**: `Makefile`
- **Work**: Add targets for `golangci-lint run`, `go vet ./...`, `gofmt -d .`, and optionally `gosec ./...`.

### 28. Applier package has zero unit tests
- **Problem**: `internal/applier/applier.go` (~900 lines) has no unit tests at all. Critical functions untested: `Apply()`, `DeleteResources()`, `Preflight()`, `waitForCRDs()`, `waitForDeployments()`, `EnsurePullSecret()`, `PatchServiceAccountPullSecret()`, `CleanupWebhookConfigurations()`, `DeleteNamespace()`.
- **Files**: `internal/applier/`
- **Work**: Add unit tests using a fake Kubernetes client (`k8s.io/client-go/kubernetes/fake`) for at least Preflight, wait loops, and resource application logic.

### 29. Certs package mostly untested
- **Problem**: `internal/certs/serving.go` only has tests for cert generation. `EnsureServingCerts()`, `EnsureWebhookCert()`, `InjectCABundle()`, `FindWebhookCertSecrets()`, and `BuildWebhookServiceMap()` have no unit tests.
- **Files**: `internal/certs/serving.go`, `internal/certs/serving_test.go`
- **Work**: Add tests with fake clients for cert secret creation, annotation detection, and CA bundle injection.

### 30. Silent failures in dependency resolution
- **Problem**: `internal/resolver/resolver.go:403` — when a bundle referenced in the catalog is not found, it `continue`s silently. `resolver.go:505-508` — if a bundle property has invalid JSON, the version extraction silently returns empty. `resolver.go:149-171` — if `skipRange` semver parsing fails, the entire skipRange processing is silently skipped.
- **Files**: `internal/resolver/resolver.go`
- **Work**: Log warnings for these cases so users can diagnose resolution failures. Consider returning errors for missing bundles referenced in the catalog.

### 31. `splitYAMLDocuments` returns original data when all docs are empty
- **Problem**: `internal/bundle/extract.go:190-192` — if all documents in a multi-doc YAML are empty or comment-only, the function returns `[][]byte{data}` (the original unparseable data). This can cause confusing errors downstream.
- **Files**: `internal/bundle/extract.go`
- **Work**: Return an empty slice or a descriptive error instead of the original data.

### 32. Apply command missing `requirePullSecretForRedHat()` validation
- **Problem**: The `apply` command (`cmd/apply.go`) does not call `requirePullSecretForRedHat()` when resolving OCI sources, unlike `install` and `generate`. A user could hit an opaque auth error when applying from a Red Hat registry OCI artifact.
- **Files**: `cmd/apply.go`
- **Work**: Add pull secret validation when the OCI source is from `registry.redhat.io`.

### 33. Pull secret parsing only supports `auth` field
- **Problem**: `internal/registry/puller.go:169` — `authEntry` struct only reads the `Auth` (base64) field. Kubernetes pull secrets that use `username`/`password` fields directly are silently ignored.
- **Files**: `internal/registry/puller.go`
- **Work**: Support `username`/`password` fields as a fallback when `auth` is empty.

---

### Low Priority — Code Quality & UX

### 34. Inconsistent error wrapping (`%w` vs `%v`)
- **Problem**: Some functions use `fmt.Errorf("...: %w", err)` (proper wrapping), others use `%v` (loses error chain). This breaks `errors.Is()` / `errors.As()` checks.
- **Files**: Various across `cmd/` and `internal/`
- **Work**: Audit all `fmt.Errorf` calls and use `%w` consistently for wrapped errors.

### 35. Discovery API queried on every `discoverSearchableResources()` call
- **Problem**: `internal/state/manager.go:265-304` queries `ServerGroupsAndResources()` on every call with no caching. For repeated `ListInstalled()` calls this is wasteful.
- **Files**: `internal/state/manager.go`
- **Work**: Cache discovery results with a short TTL or for the duration of a single command execution.

### 36. Hardcoded event limit and time window in status command
- **Problem**: `cmd/status.go:292-298` — events are limited to 10 and filtered to the last hour. These values are not configurable.
- **Files**: `cmd/status.go`
- **Work**: Add `--event-limit` and `--event-age` flags, or document the defaults clearly.

### 37. No Dockerfile for containerized usage
- **Problem**: No Dockerfile exists. While the tool is a CLI binary, a container image would enable use in CI/CD pipelines, GitOps init containers, and air-gapped environments.
- **Files**: Missing `Dockerfile`
- **Work**: Add a multi-stage Dockerfile that builds from source and produces a minimal scratch/distroless image.

### 38. No GoReleaser configuration
- **Problem**: Releases are manual (`make build-all && make checksums` then upload to GitHub). No changelog generation, no signed artifacts, no homebrew tap.
- **Files**: Missing `.goreleaser.yml`
- **Work**: Add GoReleaser config for automated multi-platform builds, checksums, changelogs, and GitHub Release publishing.

### 39. Symlink handling in tar extraction could be stricter
- **Problem**: `internal/registry/untar.go:79-96` validates symlinks don't escape `destDir` but doesn't guard against symlink chains (symlink → symlink → escape) or circular symlinks.
- **Files**: `internal/registry/untar.go`
- **Work**: Consider rejecting symlinks entirely (OCI image layers rarely need them) or resolving the full chain before validating.

### 40. No code coverage reporting
- **Problem**: `make test` runs tests but doesn't produce coverage reports. No coverage threshold enforcement.
- **Files**: `Makefile`
- **Work**: Add `make test-coverage` target with `-coverprofile` and optional threshold check (e.g., fail if below 60%).

---

### Testing Gaps

### 41. Command-level unit tests are sparse
- **Problem**: Most `cmd/*.go` files have no dedicated unit tests. Only `cmd/helpers_test.go` tests extracted helper functions. The `RunE` functions for install, uninstall, generate, upgrade, apply, search, list, clean, and status are only exercised through E2E tests.
- **Files**: `cmd/`
- **Work**: Add unit tests for command logic using test doubles. At minimum, test error paths and flag validation that don't require a real cluster.

### 42. Registry puller has no unit tests for pull operations
- **Problem**: `internal/registry/puller.go` — `PullCatalog()`, `PullBundle()`, `pullAndExtract()`, and `VerifyCredentials()` have no unit tests. Cache hit/miss paths, extraction failure cleanup, and directory creation failures are untested.
- **Files**: `internal/registry/puller.go`, `internal/registry/puller_test.go`
- **Work**: Add tests using a test registry server or mock the `go-containerregistry` interfaces.

### 43. E2E tests depend on external registries
- **Problem**: E2E tests pull from `registry.redhat.io` and `quay.io` at runtime. Network failures or catalog changes cause false negatives. No mock/local catalog fallback.
- **Files**: `test/e2e/`
- **Work**: Consider pre-caching the test operator bundle or using a local catalog mirror for hermetic tests. At minimum, tag tests that require network access so they can be skipped in air-gapped CI.
