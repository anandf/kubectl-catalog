package resolver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blang/semver/v4"

	"github.com/anandf/kubectl-catalog/pkg/catalog"
)

// InstallPlan describes the ordered list of bundles to install.
type InstallPlan struct {
	Bundles       []BundleRef
	TargetVersion string
}

// BundleRef references a resolved bundle to be installed.
type BundleRef struct {
	Name    string
	Package string
	Image   string
	Version string
	Channel string
}

// Resolver resolves operator bundles and their dependencies from a catalog.
type Resolver struct {
	fbc *catalog.FBC
}

func New(fbc *catalog.FBC) *Resolver {
	return &Resolver{fbc: fbc}
}

// Resolve finds the bundle for the given package/channel/version and resolves its dependencies.
func (r *Resolver) Resolve(packageName, channel, version string) (*InstallPlan, error) {
	pkg := r.fbc.GetPackage(packageName)
	if pkg == nil {
		return nil, fmt.Errorf("package %q not found in catalog", packageName)
	}

	if channel == "" {
		channel = pkg.DefaultChannel
	}

	targetChannel := r.findChannel(packageName, channel)
	if targetChannel == nil {
		return nil, fmt.Errorf("channel %q not found for package %q", channel, packageName)
	}

	bundleName, err := r.findBundleInChannel(targetChannel, version)
	if err != nil {
		return nil, err
	}

	plan := &InstallPlan{}
	resolved := make(map[string]bool)

	if err := r.resolveDependencies(bundleName, channel, plan, resolved); err != nil {
		return nil, err
	}

	if len(plan.Bundles) > 0 {
		plan.TargetVersion = plan.Bundles[len(plan.Bundles)-1].Version
	}

	return plan, nil
}

// ResolveUpgrade finds the full upgrade path from the current version to the
// channel head, walking the entire upgrade graph. It handles replaces chains,
// skips lists, and skipRange semver constraints.
func (r *Resolver) ResolveUpgrade(packageName, channel, currentVersion string) (*InstallPlan, error) {
	pkg := r.fbc.GetPackage(packageName)
	if pkg == nil {
		return nil, fmt.Errorf("package %q not found in catalog", packageName)
	}

	targetChannel := r.findChannel(packageName, channel)
	if targetChannel == nil {
		return nil, fmt.Errorf("channel %q not found for package %q", channel, packageName)
	}

	// Find current bundle name
	currentBundleName := r.findBundleByVersion(targetChannel, currentVersion)
	if currentBundleName == "" {
		return nil, fmt.Errorf("current version %q not found in channel %q", currentVersion, channel)
	}

	// Find the channel head
	headName, err := r.findChannelHead(targetChannel)
	if err != nil {
		return nil, err
	}

	if currentBundleName == headName {
		return nil, fmt.Errorf("already at the latest version %q in channel %q", currentVersion, channel)
	}

	// Walk the upgrade graph from current to head
	path, err := r.findUpgradePath(targetChannel, currentBundleName, headName)
	if err != nil {
		return nil, err
	}

	plan := &InstallPlan{}
	for _, bundleName := range path {
		bundle := r.fbc.GetBundle(bundleName)
		if bundle == nil {
			return nil, fmt.Errorf("bundle %q not found in catalog", bundleName)
		}
		plan.Bundles = append(plan.Bundles, BundleRef{
			Name:    bundle.Name,
			Package: bundle.Package,
			Image:   bundle.Image,
			Version: r.bundleVersion(bundle),
			Channel: channel,
		})
	}

	if len(plan.Bundles) > 0 {
		plan.TargetVersion = plan.Bundles[len(plan.Bundles)-1].Version
	}

	return plan, nil
}

// findUpgradePath walks the upgrade graph from current to target, returning
// the ordered list of bundles to apply. It uses a BFS approach to find the
// shortest path through the replaces/skips/skipRange graph.
func (r *Resolver) findUpgradePath(ch *catalog.Channel, fromBundle, toBundle string) ([]string, error) {
	// Build a reverse graph: for each entry, which entries can upgrade TO it?
	// i.e., if entry B replaces A, then A -> B is a valid upgrade step.
	upgradesFrom := make(map[string][]string) // bundleName -> list of bundles that replace/skip it

	for _, entry := range ch.Entries {
		// Direct replaces
		if entry.Replaces != "" {
			upgradesFrom[entry.Replaces] = append(upgradesFrom[entry.Replaces], entry.Name)
		}
		// Skips list
		for _, skip := range entry.Skips {
			upgradesFrom[skip] = append(upgradesFrom[skip], entry.Name)
		}
		// skipRange: any bundle whose version falls within the range can upgrade to this entry
		if entry.SkipRange != "" {
			rang, err := parseSemverRange(entry.SkipRange)
			if err == nil {
				for _, other := range ch.Entries {
					if other.Name == entry.Name {
						continue
					}
					b := r.fbc.GetBundle(other.Name)
					if b == nil {
						continue
					}
					ver := r.bundleVersion(b)
					if ver == "" {
						continue
					}
					sv, err := semver.ParseTolerant(ver)
					if err != nil {
						continue
					}
					if rang(sv) {
						upgradesFrom[other.Name] = append(upgradesFrom[other.Name], entry.Name)
					}
				}
			}
		}
	}

	// BFS from fromBundle to toBundle
	type node struct {
		name string
		path []string
	}

	visited := map[string]bool{fromBundle: true}
	queue := []node{{name: fromBundle, path: nil}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, next := range upgradesFrom[current.name] {
			if visited[next] {
				continue
			}
			visited[next] = true

			newPath := make([]string, len(current.path))
			copy(newPath, current.path)
			newPath = append(newPath, next)

			if next == toBundle {
				return newPath, nil
			}

			queue = append(queue, node{name: next, path: newPath})
		}
	}

	// If we can't find a path to the exact head, but the head's skipRange covers
	// the current version, we can jump directly
	headEntry := r.findChannelEntry(ch, toBundle)
	if headEntry != nil && headEntry.SkipRange != "" {
		rang, err := parseSemverRange(headEntry.SkipRange)
		if err == nil {
			fromB := r.fbc.GetBundle(fromBundle)
			if fromB != nil {
				ver := r.bundleVersion(fromB)
				if ver != "" {
					sv, err := semver.ParseTolerant(ver)
					if err == nil && rang(sv) {
						return []string{toBundle}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("no upgrade path from %q to %q in channel", fromBundle, toBundle)
}

func (r *Resolver) findBundleInChannel(ch *catalog.Channel, version string) (string, error) {
	if version != "" {
		// Try exact version match first
		name := r.findBundleByVersion(ch, version)
		if name != "" {
			return name, nil
		}

		// Try as a semver constraint (e.g. ">=1.0.0")
		rang, err := parseSemverRange(version)
		if err == nil {
			best := r.findBestMatch(ch, rang)
			if best != "" {
				return best, nil
			}
		}

		return "", fmt.Errorf("version %q not found in channel %q", version, ch.Name)
	}

	return r.findChannelHead(ch)
}

// findBundleByVersion finds a bundle in a channel with an exact version match.
func (r *Resolver) findBundleByVersion(ch *catalog.Channel, version string) string {
	for _, entry := range ch.Entries {
		b := r.fbc.GetBundle(entry.Name)
		if b != nil && r.bundleVersion(b) == version {
			return entry.Name
		}
	}
	return ""
}

// findBestMatch finds the highest version bundle in a channel that satisfies
// the given semver range constraint.
func (r *Resolver) findBestMatch(ch *catalog.Channel, rang semverRange) string {
	var bestName string
	var bestVer semver.Version

	for _, entry := range ch.Entries {
		b := r.fbc.GetBundle(entry.Name)
		if b == nil {
			continue
		}
		ver := r.bundleVersion(b)
		if ver == "" {
			continue
		}
		sv, err := semver.ParseTolerant(ver)
		if err != nil {
			continue
		}
		if rang(sv) {
			if bestName == "" || sv.GT(bestVer) {
				bestName = entry.Name
				bestVer = sv
			}
		}
	}
	return bestName
}

// findChannelHead returns the bundle at the head of a channel
// (the entry not replaced or skipped by any other entry).
func (r *Resolver) findChannelHead(ch *catalog.Channel) (string, error) {
	replaced := make(map[string]bool)
	for _, entry := range ch.Entries {
		if entry.Replaces != "" {
			replaced[entry.Replaces] = true
		}
		for _, skip := range entry.Skips {
			replaced[skip] = true
		}
	}

	for _, entry := range ch.Entries {
		if !replaced[entry.Name] {
			return entry.Name, nil
		}
	}

	return "", fmt.Errorf("could not determine channel head for %q", ch.Name)
}

func (r *Resolver) findChannel(packageName, channelName string) *catalog.Channel {
	for _, ch := range r.fbc.ChannelsForPackage(packageName) {
		if ch.Name == channelName {
			return &ch
		}
	}
	return nil
}

func (r *Resolver) findChannelEntry(ch *catalog.Channel, bundleName string) *catalog.ChannelEntry {
	for i := range ch.Entries {
		if ch.Entries[i].Name == bundleName {
			return &ch.Entries[i]
		}
	}
	return nil
}

func (r *Resolver) resolveDependencies(bundleName, channel string, plan *InstallPlan, resolved map[string]bool) error {
	if resolved[bundleName] {
		return nil
	}
	resolved[bundleName] = true

	bundle := r.fbc.GetBundle(bundleName)
	if bundle == nil {
		return fmt.Errorf("bundle %q not found", bundleName)
	}

	// Check for package dependencies
	for _, prop := range bundle.Properties {
		if prop.Type == "olm.package.required" {
			var dep packageDependency
			if err := json.Unmarshal(prop.Value, &dep); err != nil {
				continue
			}

			depPkg := r.fbc.GetPackage(dep.PackageName)
			if depPkg == nil {
				return fmt.Errorf("required dependency package %q not found in catalog", dep.PackageName)
			}

			depChannel := r.findChannel(dep.PackageName, depPkg.DefaultChannel)
			if depChannel == nil {
				return fmt.Errorf("default channel for dependency %q not found", dep.PackageName)
			}

			// Resolve using semver constraint if provided, otherwise pick channel head
			var depBundleName string
			if dep.VersionRange != "" {
				rang, err := parseSemverRange(dep.VersionRange)
				if err != nil {
					// Fall back to channel head if range is unparseable
					depBundleName, err = r.findChannelHead(depChannel)
					if err != nil {
						return fmt.Errorf("resolving dependency %q: %w", dep.PackageName, err)
					}
				} else {
					depBundleName = r.findBestMatch(depChannel, rang)
					if depBundleName == "" {
						return fmt.Errorf("no bundle in %q satisfies version constraint %q", dep.PackageName, dep.VersionRange)
					}
				}
			} else {
				var err error
				depBundleName, err = r.findChannelHead(depChannel)
				if err != nil {
					return fmt.Errorf("resolving dependency %q: %w", dep.PackageName, err)
				}
			}

			if err := r.resolveDependencies(depBundleName, depPkg.DefaultChannel, plan, resolved); err != nil {
				return fmt.Errorf("resolving dependency %q: %w", dep.PackageName, err)
			}
		}

		if prop.Type == "olm.gvk.required" {
			var gvkDep gvkDependency
			if err := json.Unmarshal(prop.Value, &gvkDep); err != nil {
				continue
			}

			provider := r.findGVKProvider(gvkDep, bundleName)
			if provider == "" {
				return fmt.Errorf("no bundle provides required GVK %s/%s %s",
					gvkDep.Group, gvkDep.Version, gvkDep.Kind)
			}

			providerBundle := r.fbc.GetBundle(provider)
			if providerBundle == nil {
				continue
			}

			providerPkg := r.fbc.GetPackage(providerBundle.Package)
			ch := ""
			if providerPkg != nil {
				ch = providerPkg.DefaultChannel
			}

			if err := r.resolveDependencies(provider, ch, plan, resolved); err != nil {
				return fmt.Errorf("resolving GVK dependency provider %q: %w", provider, err)
			}
		}
	}

	// Add this bundle after its dependencies
	plan.Bundles = append(plan.Bundles, BundleRef{
		Name:    bundle.Name,
		Package: bundle.Package,
		Image:   bundle.Image,
		Version: r.bundleVersion(bundle),
		Channel: channel,
	})

	return nil
}

// findGVKProvider finds a bundle that provides the requested GVK.
func (r *Resolver) findGVKProvider(dep gvkDependency, excludeBundle string) string {
	for _, b := range r.fbc.Bundles {
		if b.Name == excludeBundle {
			continue
		}
		for _, prop := range b.Properties {
			if prop.Type == "olm.gvk" {
				var provided gvkDependency
				if err := json.Unmarshal(prop.Value, &provided); err != nil {
					continue
				}
				if provided.Group == dep.Group &&
					provided.Version == dep.Version &&
					provided.Kind == dep.Kind {
					return b.Name
				}
			}
		}
	}
	return ""
}

func (r *Resolver) bundleVersion(b *catalog.Bundle) string {
	for _, prop := range b.Properties {
		if prop.Type == "olm.package" {
			var pkgProp struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(prop.Value, &pkgProp); err == nil {
				return pkgProp.Version
			}
		}
	}
	return ""
}

type packageDependency struct {
	PackageName  string `json:"packageName"`
	VersionRange string `json:"versionRange"`
}

type gvkDependency struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// semverRange is a function that tests whether a version satisfies a constraint.
type semverRange func(v semver.Version) bool

// parseSemverRange parses a semver range/constraint string into a test function.
// Supports formats like: ">=1.0.0", ">=1.0.0 <2.0.0", "^1.2.3", "~1.2.0", "1.x"
func parseSemverRange(constraint string) (semverRange, error) {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil, fmt.Errorf("empty constraint")
	}

	// blang/semver uses its own range syntax
	rang, err := semver.ParseRange(constraint)
	if err != nil {
		return nil, fmt.Errorf("parsing semver range %q: %w", constraint, err)
	}

	return semverRange(rang), nil
}
