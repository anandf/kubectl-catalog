package state

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// Labels
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPackage   = "kubectl-catalog.io/package"

	// Annotations
	AnnVersion = "kubectl-catalog.io/version"
	AnnChannel = "kubectl-catalog.io/channel"
	AnnBundle  = "kubectl-catalog.io/bundle"
	AnnCatalog = "kubectl-catalog.io/catalog"

	ManagedByValue = "kubectl-catalog"
)

// InstalledOperator describes an operator discovered from resource annotations.
type InstalledOperator struct {
	PackageName string
	Version     string
	Channel     string
	CatalogRef  string
	BundleName  string
	Resources   []ResourceInfo
}

// ResourceInfo identifies a tracked resource in the cluster.
type ResourceInfo struct {
	Group     string
	Version   string
	Kind      string
	Name      string
	Namespace string
}

// Manager discovers installed operator state by querying resource annotations/labels.
type Manager struct {
	dynamicClient   dynamic.Interface
	discoveryClient discovery.DiscoveryInterface
	namespace       string
}

func NewManager(kubeconfig, namespace string) (*Manager, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	disc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}

	return &Manager{
		dynamicClient:   dynClient,
		discoveryClient: disc,
		namespace:       namespace,
	}, nil
}

// GetInstalled discovers the installed state of a specific package by finding
// resources labeled with kubectl-catalog.io/package=<packageName>.
func (m *Manager) GetInstalled(ctx context.Context, packageName string) (*InstalledOperator, error) {
	resources, err := m.findResourcesByPackage(ctx, packageName)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("package %q is not installed", packageName)
	}

	first := resources[0]
	annotations := first.GetAnnotations()

	op := &InstalledOperator{
		PackageName: packageName,
		Version:     annotations[AnnVersion],
		Channel:     annotations[AnnChannel],
		BundleName:  annotations[AnnBundle],
		CatalogRef:  annotations[AnnCatalog],
	}

	for _, r := range resources {
		gvk := r.GroupVersionKind()
		op.Resources = append(op.Resources, ResourceInfo{
			Group:     gvk.Group,
			Version:   gvk.Version,
			Kind:      gvk.Kind,
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
		})
	}

	return op, nil
}

// ListInstalled discovers all installed operators by finding resources
// with the kubectl-catalog managed-by label.
func (m *Manager) ListInstalled(ctx context.Context) ([]InstalledOperator, error) {
	allResources, err := m.findManagedResources(ctx)
	if err != nil {
		return nil, err
	}

	byPackage := make(map[string]*InstalledOperator)
	for _, r := range allResources {
		labels := r.GetLabels()
		pkgName := labels[LabelPackage]
		if pkgName == "" {
			continue
		}

		if _, exists := byPackage[pkgName]; !exists {
			annotations := r.GetAnnotations()
			byPackage[pkgName] = &InstalledOperator{
				PackageName: pkgName,
				Version:     annotations[AnnVersion],
				Channel:     annotations[AnnChannel],
				BundleName:  annotations[AnnBundle],
				CatalogRef:  annotations[AnnCatalog],
			}
		}

		gvk := r.GroupVersionKind()
		byPackage[pkgName].Resources = append(byPackage[pkgName].Resources, ResourceInfo{
			Group:     gvk.Group,
			Version:   gvk.Version,
			Kind:      gvk.Kind,
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
		})
	}

	var operators []InstalledOperator
	for _, op := range byPackage {
		operators = append(operators, *op)
	}
	return operators, nil
}

// ResourcesForPackage returns all resources belonging to a given package.
func (m *Manager) ResourcesForPackage(ctx context.Context, packageName string) ([]unstructured.Unstructured, error) {
	return m.findResourcesByPackage(ctx, packageName)
}

func (m *Manager) findResourcesByPackage(ctx context.Context, packageName string) ([]unstructured.Unstructured, error) {
	labelSelector := fmt.Sprintf("%s=%s,%s=%s", LabelManagedBy, ManagedByValue, LabelPackage, packageName)
	return m.listWithSelector(ctx, labelSelector)
}

func (m *Manager) findManagedResources(ctx context.Context) ([]unstructured.Unstructured, error) {
	labelSelector := fmt.Sprintf("%s=%s", LabelManagedBy, ManagedByValue)
	return m.listWithSelector(ctx, labelSelector)
}

// listWithSelector dynamically discovers all API resources in the cluster that
// support listing with label selectors, and searches across all of them.
// This ensures we find resources of any type (including webhooks, PDBs,
// NetworkPolicies, etc.) that were labeled by kubectl-catalog.
func (m *Manager) listWithSelector(ctx context.Context, labelSelector string) ([]unstructured.Unstructured, error) {
	searchGVRs, err := m.discoverSearchableResources()
	if err != nil {
		return nil, fmt.Errorf("discovering API resources: %w", err)
	}

	var allResources []unstructured.Unstructured

	for _, gvr := range searchGVRs {
		var resource dynamic.ResourceInterface
		if gvr.namespaced {
			resource = m.dynamicClient.Resource(gvr.gvr).Namespace(m.namespace)
		} else {
			resource = m.dynamicClient.Resource(gvr.gvr)
		}

		list, err := resource.List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err == nil {
			allResources = append(allResources, list.Items...)
		}
	}

	// Deduplicate by UID
	seen := make(map[string]bool)
	var deduped []unstructured.Unstructured
	for _, r := range allResources {
		uid := string(r.GetUID())
		if uid != "" && seen[uid] {
			continue
		}
		seen[uid] = true
		deduped = append(deduped, r)
	}

	return deduped, nil
}

type searchableResource struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

// discoverSearchableResources queries the API server to find all resource types
// that support the "list" verb (required for label selector queries).
func (m *Manager) discoverSearchableResources() ([]searchableResource, error) {
	_, apiResourceLists, err := m.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Partial discovery is OK — some groups may be unavailable
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return nil, err
		}
	}

	var result []searchableResource

	for _, apiResourceList := range apiResourceLists {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			continue
		}

		for _, apiResource := range apiResourceList.APIResources {
			// Skip subresources (e.g., pods/status, deployments/scale)
			if containsSlash(apiResource.Name) {
				continue
			}

			// Only include resources that support "list"
			if !supportsVerb(apiResource.Verbs, "list") {
				continue
			}

			result = append(result, searchableResource{
				gvr: schema.GroupVersionResource{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: apiResource.Name,
				},
				namespaced: apiResource.Namespaced,
			})
		}
	}

	return result, nil
}

func supportsVerb(verbs metav1.Verbs, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

func containsSlash(s string) bool {
	return strings.Contains(s, "/")
}

// TrackingLabels returns the labels to apply to resources for a given package.
func TrackingLabels(packageName string) map[string]string {
	return map[string]string{
		LabelManagedBy: ManagedByValue,
		LabelPackage:   packageName,
	}
}

// TrackingAnnotations returns the annotations to apply to resources.
func TrackingAnnotations(version, channel, bundleName, catalogRef string) map[string]string {
	return map[string]string{
		AnnVersion: version,
		AnnChannel: channel,
		AnnBundle:  bundleName,
		AnnCatalog: catalogRef,
	}
}
