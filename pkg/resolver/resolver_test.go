package resolver

import (
	"encoding/json"
	"testing"

	"github.com/anandf/kubectl-catalog/pkg/catalog"
)

func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func newTestFBC() *catalog.FBC {
	return &catalog.FBC{
		Packages: []catalog.Package{
			{Schema: "olm.package", Name: "test-operator", DefaultChannel: "stable"},
			{Schema: "olm.package", Name: "dep-operator", DefaultChannel: "stable"},
		},
		Channels: []catalog.Channel{
			{
				Schema:  "olm.channel",
				Name:    "stable",
				Package: "test-operator",
				Entries: []catalog.ChannelEntry{
					{Name: "test-operator.v1.0.0"},
					{Name: "test-operator.v1.1.0", Replaces: "test-operator.v1.0.0"},
					{Name: "test-operator.v2.0.0", Replaces: "test-operator.v1.1.0"},
				},
			},
			{
				Schema:  "olm.channel",
				Name:    "stable",
				Package: "dep-operator",
				Entries: []catalog.ChannelEntry{
					{Name: "dep-operator.v0.5.0"},
				},
			},
		},
		Bundles: []catalog.Bundle{
			{
				Schema: "olm.bundle", Name: "test-operator.v1.0.0", Package: "test-operator",
				Image: "registry.example.com/test-operator:v1.0.0",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"packageName": "test-operator", "version": "1.0.0"})},
				},
			},
			{
				Schema: "olm.bundle", Name: "test-operator.v1.1.0", Package: "test-operator",
				Image: "registry.example.com/test-operator:v1.1.0",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"packageName": "test-operator", "version": "1.1.0"})},
				},
			},
			{
				Schema: "olm.bundle", Name: "test-operator.v2.0.0", Package: "test-operator",
				Image: "registry.example.com/test-operator:v2.0.0",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"packageName": "test-operator", "version": "2.0.0"})},
				},
			},
			{
				Schema: "olm.bundle", Name: "dep-operator.v0.5.0", Package: "dep-operator",
				Image: "registry.example.com/dep-operator:v0.5.0",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"packageName": "dep-operator", "version": "0.5.0"})},
				},
			},
		},
	}
}

func TestResolveChannelHead(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	plan, err := r.Resolve("test-operator", "stable", "")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if plan.TargetVersion != "2.0.0" {
		t.Errorf("expected target version 2.0.0, got %s", plan.TargetVersion)
	}
	if len(plan.Bundles) != 1 {
		t.Errorf("expected 1 bundle, got %d", len(plan.Bundles))
	}
}

func TestResolveExactVersion(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	plan, err := r.Resolve("test-operator", "stable", "1.1.0")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if plan.TargetVersion != "1.1.0" {
		t.Errorf("expected target version 1.1.0, got %s", plan.TargetVersion)
	}
}

func TestResolveDefaultChannel(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	plan, err := r.Resolve("test-operator", "", "")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if plan.TargetVersion != "2.0.0" {
		t.Errorf("expected target version 2.0.0, got %s", plan.TargetVersion)
	}
}

func TestResolvePackageNotFound(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	_, err := r.Resolve("nonexistent", "", "")
	if err == nil {
		t.Fatal("expected error for nonexistent package")
	}
}

func TestResolveVersionNotFound(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	_, err := r.Resolve("test-operator", "stable", "9.9.9")
	if err == nil {
		t.Fatal("expected error for nonexistent version")
	}
}

func TestResolveUpgrade(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	plan, err := r.ResolveUpgrade("test-operator", "stable", "1.0.0")
	if err != nil {
		t.Fatalf("ResolveUpgrade() error: %v", err)
	}

	if plan.TargetVersion != "2.0.0" {
		t.Errorf("expected target version 2.0.0, got %s", plan.TargetVersion)
	}

	// BFS should find a path from v1.0.0 to v2.0.0
	if len(plan.Bundles) == 0 {
		t.Fatal("expected at least one bundle in upgrade path")
	}

	last := plan.Bundles[len(plan.Bundles)-1]
	if last.Name != "test-operator.v2.0.0" {
		t.Errorf("expected last bundle to be test-operator.v2.0.0, got %s", last.Name)
	}
}

func TestResolveUpgradeAlreadyAtHead(t *testing.T) {
	fbc := newTestFBC()
	r := New(fbc)

	_, err := r.ResolveUpgrade("test-operator", "stable", "2.0.0")
	if err == nil {
		t.Fatal("expected error when already at head")
	}
}

func TestResolveWithSkips(t *testing.T) {
	fbc := &catalog.FBC{
		Packages: []catalog.Package{
			{Schema: "olm.package", Name: "skip-op", DefaultChannel: "stable"},
		},
		Channels: []catalog.Channel{
			{
				Schema:  "olm.channel",
				Name:    "stable",
				Package: "skip-op",
				Entries: []catalog.ChannelEntry{
					{Name: "skip-op.v1.0.0"},
					{Name: "skip-op.v1.1.0", Replaces: "skip-op.v1.0.0"},
					{Name: "skip-op.v3.0.0", Replaces: "skip-op.v1.1.0", Skips: []string{"skip-op.v1.0.0"}},
				},
			},
		},
		Bundles: []catalog.Bundle{
			{Schema: "olm.bundle", Name: "skip-op.v1.0.0", Package: "skip-op", Image: "img:v1",
				Properties: []catalog.Property{{Type: "olm.package", Value: mustMarshal(map[string]string{"version": "1.0.0"})}}},
			{Schema: "olm.bundle", Name: "skip-op.v1.1.0", Package: "skip-op", Image: "img:v1.1",
				Properties: []catalog.Property{{Type: "olm.package", Value: mustMarshal(map[string]string{"version": "1.1.0"})}}},
			{Schema: "olm.bundle", Name: "skip-op.v3.0.0", Package: "skip-op", Image: "img:v3",
				Properties: []catalog.Property{{Type: "olm.package", Value: mustMarshal(map[string]string{"version": "3.0.0"})}}},
		},
	}

	r := New(fbc)
	plan, err := r.ResolveUpgrade("skip-op", "stable", "1.0.0")
	if err != nil {
		t.Fatalf("ResolveUpgrade() error: %v", err)
	}

	// v1.0.0 can jump directly to v3.0.0 via the skips list
	last := plan.Bundles[len(plan.Bundles)-1]
	if last.Version != "3.0.0" {
		t.Errorf("expected upgrade target 3.0.0, got %s", last.Version)
	}
}

func TestResolveWithPackageDependency(t *testing.T) {
	fbc := newTestFBC()
	// Add a dependency from test-operator.v1.0.0 to dep-operator
	fbc.Bundles[0].Properties = append(fbc.Bundles[0].Properties, catalog.Property{
		Type:  "olm.package.required",
		Value: mustMarshal(map[string]string{"packageName": "dep-operator", "versionRange": ">=0.5.0"}),
	})

	r := New(fbc)
	plan, err := r.Resolve("test-operator", "stable", "1.0.0")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if len(plan.Bundles) != 2 {
		t.Fatalf("expected 2 bundles (dep + main), got %d", len(plan.Bundles))
	}

	// Dependency should come first
	if plan.Bundles[0].Package != "dep-operator" {
		t.Errorf("expected first bundle to be dep-operator, got %s", plan.Bundles[0].Package)
	}
	if plan.Bundles[1].Package != "test-operator" {
		t.Errorf("expected second bundle to be test-operator, got %s", plan.Bundles[1].Package)
	}
}

func TestResolveWithGVKDependency(t *testing.T) {
	fbc := &catalog.FBC{
		Packages: []catalog.Package{
			{Schema: "olm.package", Name: "consumer", DefaultChannel: "stable"},
			{Schema: "olm.package", Name: "provider", DefaultChannel: "stable"},
		},
		Channels: []catalog.Channel{
			{Schema: "olm.channel", Name: "stable", Package: "consumer",
				Entries: []catalog.ChannelEntry{{Name: "consumer.v1.0.0"}}},
			{Schema: "olm.channel", Name: "stable", Package: "provider",
				Entries: []catalog.ChannelEntry{{Name: "provider.v1.0.0"}}},
		},
		Bundles: []catalog.Bundle{
			{Schema: "olm.bundle", Name: "consumer.v1.0.0", Package: "consumer", Image: "img:c1",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"version": "1.0.0"})},
					{Type: "olm.gvk.required", Value: mustMarshal(map[string]string{"group": "example.com", "version": "v1", "kind": "Widget"})},
				}},
			{Schema: "olm.bundle", Name: "provider.v1.0.0", Package: "provider", Image: "img:p1",
				Properties: []catalog.Property{
					{Type: "olm.package", Value: mustMarshal(map[string]string{"version": "1.0.0"})},
					{Type: "olm.gvk", Value: mustMarshal(map[string]string{"group": "example.com", "version": "v1", "kind": "Widget"})},
				}},
		},
	}

	r := New(fbc)
	plan, err := r.Resolve("consumer", "stable", "1.0.0")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if len(plan.Bundles) != 2 {
		t.Fatalf("expected 2 bundles, got %d", len(plan.Bundles))
	}
	if plan.Bundles[0].Package != "provider" {
		t.Errorf("expected provider first, got %s", plan.Bundles[0].Package)
	}
}
