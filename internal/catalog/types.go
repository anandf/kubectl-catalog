package catalog

import (
	"encoding/json"
	"sort"
	"strings"
)

// FBC represents a parsed File-Based Catalog.
type FBC struct {
	Packages []Package
	Channels []Channel
	Bundles  []Bundle

	// indexes built by BuildIndexes for fast lookups
	packageIndex map[string]int            // name -> index in Packages
	bundleIndex  map[string]int            // name -> index in Bundles
	channelIndex map[string][]int          // packageName -> indices in Channels
}

// Package represents an olm.package entry in the catalog.
type Package struct {
	Schema         string `json:"schema"`
	Name           string `json:"name"`
	DefaultChannel string `json:"defaultChannel"`
	Description    string `json:"description,omitempty"`
	Icon           *Icon  `json:"icon,omitempty"`
}

// Icon represents a package icon.
type Icon struct {
	Data      string `json:"base64data"`
	MediaType string `json:"mediatype"`
}

// Channel represents an olm.channel entry in the catalog.
type Channel struct {
	Schema  string         `json:"schema"`
	Name    string         `json:"name"`
	Package string         `json:"package"`
	Entries []ChannelEntry `json:"entries"`
}

// ChannelEntry represents a single entry in a channel's upgrade graph.
type ChannelEntry struct {
	Name      string   `json:"name"`
	Replaces  string   `json:"replaces,omitempty"`
	Skips     []string `json:"skips,omitempty"`
	SkipRange string   `json:"skipRange,omitempty"`
}

// Bundle represents an olm.bundle entry in the catalog.
type Bundle struct {
	Schema     string     `json:"schema"`
	Name       string     `json:"name"`
	Package    string     `json:"package"`
	Image      string     `json:"image"`
	Properties []Property `json:"properties,omitempty"`
}

// Property represents a bundle property (GVK, package dependency, etc.).
type Property struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// BuildIndexes constructs internal lookup maps for fast access.
// Must be called after all Packages, Channels, and Bundles are populated.
func (f *FBC) BuildIndexes() {
	f.packageIndex = make(map[string]int, len(f.Packages))
	for i, p := range f.Packages {
		f.packageIndex[p.Name] = i
	}

	f.bundleIndex = make(map[string]int, len(f.Bundles))
	for i, b := range f.Bundles {
		f.bundleIndex[b.Name] = i
	}

	f.channelIndex = make(map[string][]int)
	for i, ch := range f.Channels {
		f.channelIndex[ch.Package] = append(f.channelIndex[ch.Package], i)
	}
}

// ChannelsForPackage returns all channels belonging to the given package.
func (f *FBC) ChannelsForPackage(packageName string) []Channel {
	if f.channelIndex != nil {
		indices := f.channelIndex[packageName]
		result := make([]Channel, 0, len(indices))
		for _, i := range indices {
			result = append(result, f.Channels[i])
		}
		return result
	}
	var result []Channel
	for _, ch := range f.Channels {
		if ch.Package == packageName {
			result = append(result, ch)
		}
	}
	return result
}

// BundlesForPackage returns all bundles belonging to the given package.
func (f *FBC) BundlesForPackage(packageName string) []Bundle {
	var result []Bundle
	for _, b := range f.Bundles {
		if b.Package == packageName {
			result = append(result, b)
		}
	}
	return result
}

// GetBundle returns the bundle with the given name.
func (f *FBC) GetBundle(name string) *Bundle {
	if f.bundleIndex != nil {
		if i, ok := f.bundleIndex[name]; ok {
			return &f.Bundles[i]
		}
		return nil
	}
	for i := range f.Bundles {
		if f.Bundles[i].Name == name {
			return &f.Bundles[i]
		}
	}
	return nil
}

// GVKQuery represents a parsed Group/Version/Kind query.
type GVKQuery struct {
	Group   string // required (unless kind-only search)
	Version string // optional — empty means match any version
	Kind    string // required
}

// GVKMatch represents a specific GVK provided by a package.
type GVKMatch struct {
	Group   string
	Version string
	Kind    string
}

func (g GVKMatch) String() string {
	return g.Group + "/" + g.Version + "/" + g.Kind
}

// GVKProviderResult represents a package that provides one or more matching GVKs.
type GVKProviderResult struct {
	PackageName    string
	DefaultChannel string
	Description    string
	MatchedGVKs    []GVKMatch
}

// ParseGVKQuery parses a flexible GVK query string into a GVKQuery.
// Supported formats:
//
//	group/version/kind     (e.g. argoproj.io/v1alpha1/ArgoCD)
//	group_version_kind     (e.g. argoproj.io_v1alpha1_ArgoCD)
//	group/kind             (e.g. argoproj.io/ArgoCD) — version is wildcard
//	group_kind             (e.g. argoproj.io_ArgoCD) — version is wildcard
//	kind                   (e.g. ArgoCD) — matches any group/version
func ParseGVKQuery(input string) GVKQuery {
	// Normalize underscores to slashes for uniform parsing
	normalized := strings.ReplaceAll(input, "_", "/")
	parts := strings.Split(normalized, "/")

	switch len(parts) {
	case 1:
		// Kind only
		return GVKQuery{Kind: parts[0]}
	case 2:
		// group/kind
		return GVKQuery{Group: parts[0], Kind: parts[1]}
	default:
		// group/version/kind — if more than 3 parts, the group contains slashes
		// (unlikely for k8s groups, but handle gracefully)
		kind := parts[len(parts)-1]
		version := parts[len(parts)-2]
		group := strings.Join(parts[:len(parts)-2], "/")
		return GVKQuery{Group: group, Version: version, Kind: kind}
	}
}

// FindGVKProviders returns all packages that contain bundles providing GVKs
// matching the query. Results are deduplicated by package and sorted by name.
func (f *FBC) FindGVKProviders(query GVKQuery) []GVKProviderResult {
	// Map: packageName -> set of matched GVKs (deduplicated)
	type gvkKey struct{ group, version, kind string }
	packageGVKs := make(map[string]map[gvkKey]bool)

	for _, b := range f.Bundles {
		for _, prop := range b.Properties {
			if prop.Type != "olm.gvk" {
				continue
			}
			var gvk struct {
				Group   string `json:"group"`
				Version string `json:"version"`
				Kind    string `json:"kind"`
			}
			if err := json.Unmarshal(prop.Value, &gvk); err != nil {
				continue
			}

			if !matchesGVKQuery(query, gvk.Group, gvk.Version, gvk.Kind) {
				continue
			}

			key := gvkKey{gvk.Group, gvk.Version, gvk.Kind}
			if packageGVKs[b.Package] == nil {
				packageGVKs[b.Package] = make(map[gvkKey]bool)
			}
			packageGVKs[b.Package][key] = true
		}
	}

	// Build results
	results := make([]GVKProviderResult, 0, len(packageGVKs))
	for pkgName, gvks := range packageGVKs {
		result := GVKProviderResult{PackageName: pkgName}

		if pkg := f.GetPackage(pkgName); pkg != nil {
			result.DefaultChannel = pkg.DefaultChannel
			result.Description = pkg.Description
		}

		for key := range gvks {
			result.MatchedGVKs = append(result.MatchedGVKs, GVKMatch{
				Group:   key.group,
				Version: key.version,
				Kind:    key.kind,
			})
		}
		// Sort matched GVKs for stable output
		sort.Slice(result.MatchedGVKs, func(i, j int) bool {
			return result.MatchedGVKs[i].String() < result.MatchedGVKs[j].String()
		})

		results = append(results, result)
	}

	// Sort results by package name
	sort.Slice(results, func(i, j int) bool {
		return results[i].PackageName < results[j].PackageName
	})

	return results
}

// matchesGVKQuery checks if a provided GVK matches the query.
func matchesGVKQuery(q GVKQuery, group, version, kind string) bool {
	// Kind must always match (case-sensitive)
	if q.Kind != kind {
		return false
	}
	// Group: case-insensitive match, or skip if empty (kind-only query)
	if q.Group != "" && !strings.EqualFold(q.Group, group) {
		return false
	}
	// Version: exact match, or skip if empty (wildcard)
	if q.Version != "" && q.Version != version {
		return false
	}
	return true
}

// GetPackage returns the package with the given name.
func (f *FBC) GetPackage(name string) *Package {
	if f.packageIndex != nil {
		if i, ok := f.packageIndex[name]; ok {
			return &f.Packages[i]
		}
		return nil
	}
	for i := range f.Packages {
		if f.Packages[i].Name == name {
			return &f.Packages[i]
		}
	}
	return nil
}
