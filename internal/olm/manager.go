package olm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const fieldManager = "kubectl-catalog"

// InstallOptions configures an OLM-based install.
type InstallOptions struct {
	PackageName    string
	CatalogImage   string
	CatalogType    string
	Channel        string
	StartingCSV    string
	InstallMode    string
	Force          bool
	NoWait         bool
	WaitTimeout    time.Duration
	PullSecretName string
}

// UpgradeOptions configures an OLM-based upgrade.
type UpgradeOptions struct {
	PackageName string
	Channel     string
	NoWait      bool
	WaitTimeout time.Duration
}

// Manager manages operator lifecycle via OLM resources.
type Manager struct {
	dynamicClient   dynamic.Interface
	discoveryClient discovery.DiscoveryInterface
	namespace       string
	dryRun          bool
}

// NewManager creates a Manager from kubeconfig.
func NewManager(kubeconfig, namespace string, dryRun bool) (*Manager, error) {
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
		dryRun:          dryRun,
	}, nil
}

// Install creates the OLM resources needed to install an operator.
func (m *Manager) Install(ctx context.Context, opts InstallOptions) error {
	if err := RequireOLM(m.discoveryClient); err != nil {
		return err
	}

	if err := m.ensureNamespace(ctx); err != nil {
		return err
	}

	// OperatorGroup — OLM requires exactly one per namespace
	if err := m.ensureOperatorGroup(ctx, opts.InstallMode); err != nil {
		return err
	}

	// CatalogSource
	csName := CatalogSourceName(opts.CatalogType, opts.CatalogImage)
	if err := m.ensureCatalogSource(ctx, csName, opts.CatalogImage, opts.CatalogType, opts.PullSecretName); err != nil {
		return err
	}

	if !m.dryRun && !opts.NoWait {
		waitTimeout := opts.WaitTimeout
		if waitTimeout == 0 {
			waitTimeout = 5 * time.Minute
		}
		if err := waitForCatalogSource(ctx, m.dynamicClient, csName, m.namespace, waitTimeout); err != nil {
			return err
		}
	}

	// Subscription
	if err := m.ensureSubscription(ctx, opts.PackageName, opts.Channel, csName, opts.StartingCSV, opts.Force); err != nil {
		return err
	}

	if !m.dryRun && !opts.NoWait {
		waitTimeout := opts.WaitTimeout
		if waitTimeout == 0 {
			waitTimeout = 10 * time.Minute
		}
		csvName, err := waitForSubscription(ctx, m.dynamicClient, opts.PackageName, m.namespace, waitTimeout)
		if err != nil {
			return err
		}
		if err := waitForCSV(ctx, m.dynamicClient, csvName, m.namespace, waitTimeout); err != nil {
			return err
		}
	}

	return nil
}

// Upgrade updates an existing OLM Subscription to a new channel.
func (m *Manager) Upgrade(ctx context.Context, opts UpgradeOptions) error {
	if err := RequireOLM(m.discoveryClient); err != nil {
		return err
	}

	sub, err := m.findSubscription(ctx, opts.PackageName)
	if err != nil {
		return err
	}

	if opts.Channel == "" {
		return fmt.Errorf("--channel is required for OLM upgrade")
	}

	currentChannel, _, _ := unstructured.NestedString(sub.Object, "spec", "channel")
	if currentChannel == opts.Channel {
		fmt.Printf("Subscription %q is already on channel %q\n", opts.PackageName, opts.Channel)
		return nil
	}

	fmt.Printf("Updating Subscription %q channel: %s -> %s\n", opts.PackageName, currentChannel, opts.Channel)

	if m.dryRun {
		fmt.Printf("  Would update Subscription %q channel to %q (dry-run)\n", opts.PackageName, opts.Channel)
		return nil
	}

	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"channel": opts.Channel,
		},
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling patch: %w", err)
	}

	_, err = m.dynamicClient.Resource(SubscriptionGVR()).Namespace(sub.GetNamespace()).Patch(
		ctx, sub.GetName(), types.MergePatchType, patchData, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating Subscription: %w", err)
	}

	fmt.Printf("  Subscription %q updated to channel %q\n", opts.PackageName, opts.Channel)

	if !opts.NoWait {
		waitTimeout := opts.WaitTimeout
		if waitTimeout == 0 {
			waitTimeout = 10 * time.Minute
		}
		csvName, err := waitForSubscription(ctx, m.dynamicClient, opts.PackageName, sub.GetNamespace(), waitTimeout)
		if err != nil {
			return err
		}
		if err := waitForCSV(ctx, m.dynamicClient, csvName, sub.GetNamespace(), waitTimeout); err != nil {
			return err
		}
	}

	return nil
}

// Uninstall removes OLM resources for a package.
func (m *Manager) Uninstall(ctx context.Context, packageName string) error {
	if err := RequireOLM(m.discoveryClient); err != nil {
		return err
	}

	sub, err := m.findSubscription(ctx, packageName)
	if err != nil {
		return err
	}

	subNamespace := sub.GetNamespace()

	// Read CSV name before deleting Subscription
	csvName, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")

	// Delete Subscription
	if err := m.deleteResource(ctx, SubscriptionGVR(), sub.GetName(), subNamespace, "Subscription"); err != nil {
		return err
	}

	// Delete CSV
	if csvName != "" {
		if err := m.deleteResource(ctx, CSVGVR(), csvName, subNamespace, "ClusterServiceVersion"); err != nil {
			fmt.Printf("  Warning: could not delete CSV %q: %v\n", csvName, err)
		}
	}

	// Delete CatalogSource if managed by us
	if err := m.deleteOwnedResources(ctx, CatalogSourceGVR(), subNamespace, "CatalogSource"); err != nil {
		fmt.Printf("  Warning: could not clean up CatalogSources: %v\n", err)
	}

	// Delete OperatorGroup if managed by us and no other Subscriptions remain
	remainingSubs, _ := m.dynamicClient.Resource(SubscriptionGVR()).Namespace(subNamespace).List(ctx, metav1.ListOptions{})
	if remainingSubs == nil || len(remainingSubs.Items) == 0 {
		if err := m.deleteOwnedResources(ctx, OperatorGroupGVR(), subNamespace, "OperatorGroup"); err != nil {
			fmt.Printf("  Warning: could not clean up OperatorGroups: %v\n", err)
		}
	} else {
		fmt.Printf("  Preserving OperatorGroup (other Subscriptions exist in namespace)\n")
	}

	return nil
}

func (m *Manager) ensureNamespace(ctx context.Context) error {
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	_, err := m.dynamicClient.Resource(nsGVR).Get(ctx, m.namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %q: %w", m.namespace, err)
	}

	if m.dryRun {
		fmt.Printf("  Would create namespace %q (dry-run)\n", m.namespace)
		return nil
	}

	ns := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": m.namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
		},
	}
	data, err := ns.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling namespace: %w", err)
	}
	_, err = m.dynamicClient.Resource(nsGVR).Patch(ctx, m.namespace, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("creating namespace %q: %w", m.namespace, err)
	}
	fmt.Printf("  Created namespace %q\n", m.namespace)
	return nil
}

func (m *Manager) ensureOperatorGroup(ctx context.Context, installMode string) error {
	resource := m.dynamicClient.Resource(OperatorGroupGVR()).Namespace(m.namespace)
	list, err := resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing OperatorGroups: %w", err)
	}

	if len(list.Items) > 0 {
		fmt.Printf("  Using existing OperatorGroup %q in namespace %q\n", list.Items[0].GetName(), m.namespace)
		return nil
	}

	var targetNamespaces []string
	switch installMode {
	case "SingleNamespace", "OwnNamespace":
		targetNamespaces = []string{m.namespace}
	}

	og := NewOperatorGroup("kubectl-catalog-og", m.namespace, targetNamespaces)

	if m.dryRun {
		fmt.Printf("  Would create OperatorGroup %q (dry-run)\n", og.GetName())
		return nil
	}

	data, err := og.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling OperatorGroup: %w", err)
	}
	_, err = resource.Patch(ctx, og.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("creating OperatorGroup: %w", err)
	}
	fmt.Printf("  Created OperatorGroup %q\n", og.GetName())
	return nil
}

func (m *Manager) ensureCatalogSource(ctx context.Context, name, image, catalogType, pullSecretName string) error {
	resource := m.dynamicClient.Resource(CatalogSourceGVR()).Namespace(m.namespace)

	existing, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		existingImage, _, _ := unstructured.NestedString(existing.Object, "spec", "image")
		if existingImage == image {
			fmt.Printf("  CatalogSource %q already exists with correct image\n", name)
			return nil
		}
		fmt.Printf("  Updating CatalogSource %q image: %s -> %s\n", name, existingImage, image)
	}

	displayName := catalogType + " Catalog"
	if catalogType == "" {
		displayName = "Custom Catalog"
	}

	cs := NewCatalogSource(name, m.namespace, image, displayName, pullSecretName)

	if m.dryRun {
		fmt.Printf("  Would create CatalogSource %q with image %q (dry-run)\n", name, image)
		return nil
	}

	data, err := cs.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling CatalogSource: %w", err)
	}
	_, err = resource.Patch(ctx, name, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("creating CatalogSource: %w", err)
	}
	fmt.Printf("  CatalogSource %q created with image %q\n", name, image)
	return nil
}

func (m *Manager) ensureSubscription(ctx context.Context, packageName, channel, catalogSourceName, startingCSV string, force bool) error {
	resource := m.dynamicClient.Resource(SubscriptionGVR()).Namespace(m.namespace)

	existing, err := resource.Get(ctx, packageName, metav1.GetOptions{})
	if err == nil && !force {
		currentChannel, _, _ := unstructured.NestedString(existing.Object, "spec", "channel")
		return fmt.Errorf("subscription %q already exists (channel: %s); use --force to re-create", packageName, currentChannel)
	}

	sub := NewSubscription(packageName, m.namespace, channel, catalogSourceName, m.namespace, startingCSV)

	if m.dryRun {
		fmt.Printf("  Would create Subscription %q (channel: %s) (dry-run)\n", packageName, channel)
		return nil
	}

	data, err := sub.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling Subscription: %w", err)
	}
	_, err = resource.Patch(ctx, packageName, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("creating Subscription: %w", err)
	}
	fmt.Printf("  Subscription %q created (channel: %s)\n", packageName, channel)
	return nil
}

func (m *Manager) findSubscription(ctx context.Context, packageName string) (*unstructured.Unstructured, error) {
	// First try direct get by name (Subscription name matches package name)
	resource := m.dynamicClient.Resource(SubscriptionGVR()).Namespace(m.namespace)
	sub, err := resource.Get(ctx, packageName, metav1.GetOptions{})
	if err == nil {
		return sub, nil
	}

	// Fall back to label selector search across all namespaces
	allResource := m.dynamicClient.Resource(SubscriptionGVR())
	list, err := allResource.List(ctx, metav1.ListOptions{
		LabelSelector: "kubectl-catalog.io/package=" + packageName,
	})
	if err != nil {
		return nil, fmt.Errorf("searching for Subscription: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no Subscription found for package %q\nHint: is this package installed via OLM? Check with 'kubectl get subscriptions -A'", packageName)
	}
	return &list.Items[0], nil
}

func (m *Manager) deleteResource(ctx context.Context, gvr schema.GroupVersionResource, name, namespace, kind string) error {
	if m.dryRun {
		fmt.Printf("  Would delete %s %q (dry-run)\n", kind, name)
		return nil
	}

	resource := m.dynamicClient.Resource(gvr).Namespace(namespace)
	propagation := metav1.DeletePropagationForeground
	err := resource.Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting %s %q: %w", kind, name, err)
	}
	fmt.Printf("  Deleted %s %q\n", kind, name)
	return nil
}

func (m *Manager) deleteOwnedResources(ctx context.Context, gvr schema.GroupVersionResource, namespace, kind string) error {
	resource := m.dynamicClient.Resource(gvr).Namespace(namespace)
	list, err := resource.List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=kubectl-catalog",
	})
	if err != nil {
		return err
	}

	for _, item := range list.Items {
		if err := m.deleteResource(ctx, gvr, item.GetName(), namespace, kind); err != nil {
			return err
		}
	}
	return nil
}
