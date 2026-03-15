package catalog

import "encoding/json"

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
