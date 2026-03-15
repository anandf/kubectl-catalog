package applier

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/state"
)

const (
	fieldManager = "kubectl-catalog"

	// Default timeouts for waiting on resources (can be overridden via ApplierOptions).
	defaultCRDEstablishTimeout    = 60 * time.Second
	defaultDeploymentReadyTimeout = 5 * time.Minute
	pollInterval                  = 2 * time.Second

)

// PullSecretName returns the name of the image pull secret for a given package.
func PullSecretName(packageName string) string {
	return packageName + "-pull-secret"
}

// InstallContext holds the tracking metadata for the current install/upgrade operation.
type InstallContext struct {
	PackageName string
	Version     string
	Channel     string
	BundleName  string
	BundleImage string
	CatalogRef  string
}

// Options configures optional behavior for the Applier.
type Options struct {
	CRDEstablishTimeout    time.Duration
	DeploymentReadyTimeout time.Duration
	DryRun                 bool
	NoWait                 bool // skip deployment readiness checks
}

// Applier applies Kubernetes manifests to a cluster using server-side apply.
type Applier struct {
	dynamicClient          dynamic.Interface
	discoveryClient        discovery.DiscoveryInterface
	mapper                 meta.RESTMapper
	mapperMu               sync.RWMutex
	namespace              string
	crdEstablishTimeout    time.Duration
	deploymentReadyTimeout time.Duration
	dryRun                 bool
	noWait                 bool
}

func New(kubeconfig, namespace string, opts ...Options) (*Applier, error) {
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

	groupResources, err := restmapper.GetAPIGroupResources(disc)
	if err != nil {
		return nil, fmt.Errorf("getting API group resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	a := &Applier{
		dynamicClient:          dynClient,
		discoveryClient:        disc,
		mapper:                 mapper,
		namespace:              namespace,
		crdEstablishTimeout:    defaultCRDEstablishTimeout,
		deploymentReadyTimeout: defaultDeploymentReadyTimeout,
	}

	if len(opts) > 0 {
		o := opts[0]
		if o.CRDEstablishTimeout > 0 {
			a.crdEstablishTimeout = o.CRDEstablishTimeout
		}
		if o.DeploymentReadyTimeout > 0 {
			a.deploymentReadyTimeout = o.DeploymentReadyTimeout
		}
		a.dryRun = o.DryRun
		a.noWait = o.NoWait
	}

	return a, nil
}

// EnsureNamespace creates the given namespace if it does not already exist.
func (a *Applier) EnsureNamespace(ctx context.Context, name string) error {
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	_, err := a.dynamicClient.Resource(nsGVR).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %q: %w", name, err)
	}

	if a.dryRun {
		fmt.Printf("  Would create namespace %q (dry-run)\n", name)
		return nil
	}

	ns := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": name,
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

	_, err = a.dynamicClient.Resource(nsGVR).Patch(ctx, name, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("creating namespace %q: %w", name, err)
	}

	fmt.Printf("  Created namespace %q\n", name)
	return nil
}

// Namespace returns the applier's target namespace.
func (a *Applier) Namespace() string {
	return a.namespace
}

// SetNamespace updates the applier's target namespace.
func (a *Applier) SetNamespace(ns string) {
	a.namespace = ns
}

// Apply applies all manifests from a bundle to the cluster in the correct order:
//  1. Apply CRDs and wait for them to be established
//  2. Refresh the REST mapper so new CRD types are discoverable
//  3. Apply RBAC (ServiceAccounts, Roles, ClusterRoles, Bindings)
//  4. Apply Deployments and wait for rollout to complete
//  5. Apply Services and other resources
//
// All resources are stamped with tracking labels and annotations.
func (a *Applier) Apply(ctx context.Context, manifests *bundle.Manifests, ic *InstallContext) error {
	labels := state.TrackingLabels(ic.PackageName)
	annotations := state.TrackingAnnotations(ic.Version, ic.Channel, ic.BundleName, ic.BundleImage, ic.CatalogRef)

	// Phase 1: CRDs
	if len(manifests.CRDs) > 0 {
		fmt.Printf("  Applying %d CRD(s)...\n", len(manifests.CRDs))
		for _, obj := range manifests.CRDs {
			stampMetadata(obj, labels, annotations)
			if err := a.applyObject(ctx, obj); err != nil {
				return fmt.Errorf("applying CRD %q: %w", obj.GetName(), err)
			}
		}

		if err := a.waitForCRDs(ctx, manifests.CRDs); err != nil {
			return err
		}

		// Refresh the REST mapper so newly created CRD types can be resolved
		if err := a.refreshMapper(); err != nil {
			return fmt.Errorf("refreshing REST mapper: %w", err)
		}
	}

	// Phase 2: RBAC
	if len(manifests.RBAC) > 0 {
		fmt.Printf("  Applying %d RBAC resource(s)...\n", len(manifests.RBAC))
		for _, obj := range manifests.RBAC {
			a.setDefaultSubjectNamespaces(obj)
			stampMetadata(obj, labels, annotations)
			if err := a.applyObject(ctx, obj); err != nil {
				return fmt.Errorf("applying RBAC %s %q: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
	}

	// Phase 3: Deployments
	if len(manifests.Deployments) > 0 {
		fmt.Printf("  Applying %d Deployment(s)...\n", len(manifests.Deployments))
		for _, obj := range manifests.Deployments {
			stampMetadata(obj, labels, annotations)
			if err := a.applyObject(ctx, obj); err != nil {
				return fmt.Errorf("applying Deployment %q: %w", obj.GetName(), err)
			}
		}

		if err := a.waitForDeployments(ctx, manifests.Deployments); err != nil {
			return err
		}
	}

	// Phase 4: Services
	if len(manifests.Services) > 0 {
		fmt.Printf("  Applying %d Service(s)...\n", len(manifests.Services))
		for _, obj := range manifests.Services {
			stampMetadata(obj, labels, annotations)
			if err := a.applyObject(ctx, obj); err != nil {
				return fmt.Errorf("applying Service %q: %w", obj.GetName(), err)
			}
		}
	}

	// Phase 5: Other resources
	if len(manifests.Other) > 0 {
		fmt.Printf("  Applying %d other resource(s)...\n", len(manifests.Other))
		for _, obj := range manifests.Other {
			stampMetadata(obj, labels, annotations)
			if err := a.applyObject(ctx, obj); err != nil {
				return fmt.Errorf("applying %s %q: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
	}

	return nil
}

// DeleteResources removes the given unstructured resources from the cluster.
func (a *Applier) DeleteResources(ctx context.Context, resources []unstructured.Unstructured) error {
	// Delete in reverse order (deployments/services before CRDs)
	for i := len(resources) - 1; i >= 0; i-- {
		obj := resources[i]
		if a.dryRun {
			fmt.Printf("  Would delete %s/%s (dry-run)\n", obj.GetKind(), obj.GetName())
			continue
		}
		if err := a.deleteObject(ctx, &obj); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("deleting %s %q: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
		fmt.Printf("  Deleted %s/%s\n", obj.GetKind(), obj.GetName())
	}
	return nil
}

// refreshMapper re-discovers API group resources and rebuilds the REST mapper.
// This is needed after CRDs are created so that new types become resolvable.
func (a *Applier) refreshMapper() error {
	groupResources, err := restmapper.GetAPIGroupResources(a.discoveryClient)
	if err != nil {
		return err
	}
	a.mapperMu.Lock()
	a.mapper = restmapper.NewDiscoveryRESTMapper(groupResources)
	a.mapperMu.Unlock()
	return nil
}

func (a *Applier) getMapper() meta.RESTMapper {
	a.mapperMu.RLock()
	defer a.mapperMu.RUnlock()
	return a.mapper
}

// setDefaultSubjectNamespaces fills in the target namespace on any
// ClusterRoleBinding or RoleBinding subject that is a ServiceAccount
// without a namespace set. This mirrors OLM behavior — operator bundles
// often omit the namespace on subjects since OLM injects it at install time.
func (a *Applier) setDefaultSubjectNamespaces(obj *unstructured.Unstructured) {
	kind := obj.GetKind()
	if kind != "ClusterRoleBinding" && kind != "RoleBinding" {
		return
	}

	subjects, found, _ := unstructured.NestedSlice(obj.Object, "subjects")
	if !found {
		return
	}

	modified := false
	for i, s := range subjects {
		subject, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		subjectKind, _ := subject["kind"].(string)
		ns, _ := subject["namespace"].(string)
		if subjectKind == "ServiceAccount" && ns == "" {
			subject["namespace"] = a.namespace
			subjects[i] = subject
			modified = true
		}
	}

	if modified {
		unstructured.SetNestedSlice(obj.Object, subjects, "subjects")
	}
}

func stampMetadata(obj *unstructured.Unstructured, labels, annotations map[string]string) {
	existing := obj.GetLabels()
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range labels {
		existing[k] = v
	}
	obj.SetLabels(existing)

	existingAnn := obj.GetAnnotations()
	if existingAnn == nil {
		existingAnn = make(map[string]string)
	}
	for k, v := range annotations {
		existingAnn[k] = v
	}
	obj.SetAnnotations(existingAnn)
}

// DryRun returns whether the applier is in dry-run mode.
func (a *Applier) DryRun() bool {
	return a.dryRun
}

func (a *Applier) applyObject(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.getMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("finding REST mapping for %s: %w", gvk, err)
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if obj.GetNamespace() == "" {
			obj.SetNamespace(a.namespace)
		}
	}

	if a.dryRun {
		fmt.Printf("    %s/%s would be applied (dry-run)\n", obj.GetKind(), obj.GetName())
		return nil
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	var resource dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		resource = a.dynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())
	} else {
		resource = a.dynamicClient.Resource(mapping.Resource)
	}

	_, err = resource.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("server-side apply: %w", err)
	}

	fmt.Printf("    %s/%s applied\n", obj.GetKind(), obj.GetName())
	return nil
}

func (a *Applier) deleteObject(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.getMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return err
	}

	var resource dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		resource = a.dynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())
	} else {
		resource = a.dynamicClient.Resource(mapping.Resource)
	}

	propagation := metav1.DeletePropagationForeground
	return resource.Delete(ctx, obj.GetName(), metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
}

// EnsurePullSecret creates (or updates) a kubernetes.io/dockerconfigjson Secret
// in the target namespace using the provided pull secret data. The secret is
// stamped with tracking labels so it can be discovered and cleaned up.
// This secret is referenced by ServiceAccounts via imagePullSecrets so that
// operator pods can pull images from authenticated registries.
func (a *Applier) EnsurePullSecret(ctx context.Context, pullSecretData []byte, packageName string) error {
	secretName := PullSecretName(packageName)

	if a.dryRun {
		fmt.Printf("  Would create pull secret %q in namespace %q (dry-run)\n", secretName, a.namespace)
		return nil
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	resource := a.dynamicClient.Resource(secretGVR).Namespace(a.namespace)

	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      secretName,
				"namespace": a.namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
			"type": "kubernetes.io/dockerconfigjson",
			"stringData": map[string]interface{}{
				".dockerconfigjson": string(pullSecretData),
			},
		},
	}

	data, err := secret.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling pull secret: %w", err)
	}

	_, err = resource.Patch(ctx, secretName, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("applying pull secret: %w", err)
	}

	fmt.Printf("  Pull secret %q created in namespace %q (type: kubernetes.io/dockerconfigjson)\n", secretName, a.namespace)
	return nil
}

// DeletePullSecret removes the package's pull secret from the target namespace.
func (a *Applier) DeletePullSecret(ctx context.Context, packageName string) error {
	secretName := PullSecretName(packageName)

	if a.dryRun {
		fmt.Printf("  Would delete pull secret %q from namespace %q (dry-run)\n", secretName, a.namespace)
		return nil
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	resource := a.dynamicClient.Resource(secretGVR).Namespace(a.namespace)

	err := resource.Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // already gone
		}
		return fmt.Errorf("deleting pull secret %q: %w", secretName, err)
	}

	fmt.Printf("  Deleted pull secret %q from namespace %q\n", secretName, a.namespace)
	return nil
}

// DeleteNamespace deletes a namespace only if it was created by kubectl-catalog
// (has the managed-by label) and is not a protected system namespace.
func (a *Applier) DeleteNamespace(ctx context.Context, name string) error {
	protected := map[string]bool{
		"default":         true,
		"kube-system":     true,
		"kube-public":     true,
		"kube-node-lease": true,
	}
	if protected[name] {
		return nil
	}

	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	ns, err := a.dynamicClient.Resource(nsGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("checking namespace %q: %w", name, err)
	}

	// Only delete if we created it
	labels := ns.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "kubectl-catalog" {
		fmt.Printf("  Namespace %q was not created by kubectl-catalog, preserving\n", name)
		return nil
	}

	if a.dryRun {
		fmt.Printf("  Would delete namespace %q (dry-run)\n", name)
		return nil
	}

	propagation := metav1.DeletePropagationForeground
	err = a.dynamicClient.Resource(nsGVR).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting namespace %q: %w", name, err)
	}

	fmt.Printf("  Deleted namespace %q\n", name)
	return nil
}

// PatchServiceAccountPullSecret adds the named pull secret to a ServiceAccount's
// imagePullSecrets list. This ensures pods using this SA can pull images from
// authenticated registries.
func (a *Applier) PatchServiceAccountPullSecret(ctx context.Context, saName, secretName string) error {
	if a.dryRun {
		fmt.Printf("    Would patch ServiceAccount %q with imagePullSecrets (dry-run)\n", saName)
		return nil
	}

	saGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	resource := a.dynamicClient.Resource(saGVR).Namespace(a.namespace)

	// Get current SA to check if pull secret is already referenced
	sa, err := resource.Get(ctx, saName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting ServiceAccount %q: %w", saName, err)
	}

	// Check if already has the pull secret
	pullSecrets, _, _ := unstructured.NestedSlice(sa.Object, "imagePullSecrets")
	for _, ps := range pullSecrets {
		if psMap, ok := ps.(map[string]interface{}); ok {
			if psMap["name"] == secretName {
				return nil // already present
			}
		}
	}

	// Patch to add the pull secret reference
	patch := map[string]interface{}{
		"imagePullSecrets": append(pullSecrets, map[string]interface{}{
			"name": secretName,
		}),
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling SA patch: %w", err)
	}

	_, err = resource.Patch(ctx, saName, types.MergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patching ServiceAccount %q with pull secret: %w", saName, err)
	}

	fmt.Printf("    ServiceAccount %q patched with imagePullSecrets\n", saName)
	return nil
}

// waitForCRDs waits for all CRDs to reach the Established condition.
func (a *Applier) waitForCRDs(ctx context.Context, crds []*unstructured.Unstructured) error {
	if a.dryRun {
		return nil
	}
	crdGVR := crdGroupVersionResource()

	for _, crd := range crds {
		crdName := crd.GetName()
		fmt.Printf("    Waiting for CRD %s to be established...\n", crdName)

		resource := a.dynamicClient.Resource(crdGVR)
		deadline := time.Now().Add(a.crdEstablishTimeout)

		for time.Now().Before(deadline) {
			obj, err := resource.Get(ctx, crdName, metav1.GetOptions{})
			if err != nil {
				time.Sleep(pollInterval)
				continue
			}

			if isCRDEstablished(obj) {
				fmt.Printf("    CRD %s is established\n", crdName)
				break
			}

			if time.Now().Add(pollInterval).After(deadline) {
				return fmt.Errorf("timed out waiting for CRD %s to be established", crdName)
			}
			time.Sleep(pollInterval)
		}
	}
	return nil
}

// waitForDeployments waits for all deployments to complete their rollout.
func (a *Applier) waitForDeployments(ctx context.Context, deployments []*unstructured.Unstructured) error {
	if a.dryRun || a.noWait {
		if a.noWait {
			fmt.Println("    Skipping deployment readiness checks (--no-wait)")
		}
		return nil
	}
	deployGVR := deploymentGroupVersionResource()

	for _, dep := range deployments {
		depName := dep.GetName()
		ns := dep.GetNamespace()
		if ns == "" {
			ns = a.namespace
		}

		fmt.Printf("    Waiting for Deployment %s to be ready...\n", depName)

		resource := a.dynamicClient.Resource(deployGVR).Namespace(ns)
		deadline := time.Now().Add(a.deploymentReadyTimeout)

		for time.Now().Before(deadline) {
			obj, err := resource.Get(ctx, depName, metav1.GetOptions{})
			if err != nil {
				time.Sleep(pollInterval)
				continue
			}

			ready, reason := isDeploymentReady(obj)
			if ready {
				fmt.Printf("    Deployment %s is ready\n", depName)
				break
			}

			if time.Now().Add(pollInterval).After(deadline) {
				return fmt.Errorf("timed out waiting for Deployment %s to be ready: %s", depName, reason)
			}
			time.Sleep(pollInterval)
		}
	}
	return nil
}

func isCRDEstablished(obj *unstructured.Unstructured) bool {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Established" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

func isDeploymentReady(obj *unstructured.Unstructured) (bool, string) {
	// Check for the Available condition
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false, "no status conditions"
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		condType, _ := cond["type"].(string)
		condStatus, _ := cond["status"].(string)

		// Check for a failed rollout
		if condType == "Progressing" && condStatus == "False" {
			reason, _ := cond["message"].(string)
			return false, fmt.Sprintf("rollout failed: %s", reason)
		}
	}

	// Compare desired vs ready replicas
	replicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "replicas")
	readyReplicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
	updatedReplicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "updatedReplicas")
	desiredReplicas, found, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")

	if !found {
		desiredReplicas = 1 // Kubernetes default when spec.replicas is omitted
	}

	if replicas == desiredReplicas &&
		readyReplicas == desiredReplicas &&
		updatedReplicas == desiredReplicas {
		return true, ""
	}

	return false, fmt.Sprintf("waiting for replicas: %d/%d ready, %d/%d updated",
		readyReplicas, desiredReplicas, updatedReplicas, desiredReplicas)
}

// Preflight performs pre-install validation checks:
//  1. Verifies cluster connectivity by querying the API server version
//  2. Checks for CRD conflicts (existing CRDs with the same name but managed by a different package)
//  3. Verifies RBAC permissions by dry-run applying a sample namespaced resource
//
// Returns a list of warnings (non-fatal) and an error (fatal).
func (a *Applier) Preflight(ctx context.Context, manifests *bundle.Manifests, packageName string) (warnings []string, err error) {
	// 1. Cluster connectivity check
	_, err = a.discoveryClient.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("cluster is not reachable: %w", err)
	}

	// 2. CRD conflict check — ensure existing CRDs aren't managed by a different package
	if len(manifests.CRDs) > 0 {
		crdGVR := crdGroupVersionResource()
		for _, crd := range manifests.CRDs {
			crdName := crd.GetName()
			existing, getErr := a.dynamicClient.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
			if getErr != nil {
				continue // CRD doesn't exist yet — no conflict
			}

			labels := existing.GetLabels()
			existingPkg := labels[state.LabelPackage]
			existingManagedBy := labels[state.LabelManagedBy]

			if existingManagedBy == state.ManagedByValue && existingPkg != "" && existingPkg != packageName {
				return nil, fmt.Errorf("CRD %q already exists and is managed by package %q (this install is for %q); uninstall %q first or use --force",
					crdName, existingPkg, packageName, existingPkg)
			}

			if existingManagedBy != state.ManagedByValue {
				warnings = append(warnings, fmt.Sprintf("CRD %q already exists (not managed by kubectl-catalog); it will be updated via server-side apply", crdName))
			}
		}
	}

	// 3. RBAC permission check — verify we can create resources in the target namespace
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	_, err = a.dynamicClient.Resource(nsGVR).Get(ctx, a.namespace, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Namespace doesn't exist — that's fine, we'll create it
		} else if errors.IsForbidden(err) {
			return nil, fmt.Errorf("insufficient permissions: cannot access namespace %q: %w", a.namespace, err)
		}
		// Other errors (network, etc.) — already caught by connectivity check
	}

	// Check if we can create deployments (a good proxy for general write access)
	deployGVR := deploymentGroupVersionResource()
	testDeploy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "kubectl-catalog-preflight-test",
				"namespace": a.namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "preflight"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{"app": "preflight"},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "test",
								"image": "busybox",
							},
						},
					},
				},
			},
		},
	}

	data, marshalErr := testDeploy.MarshalJSON()
	if marshalErr == nil {
		_, err = a.dynamicClient.Resource(deployGVR).Namespace(a.namespace).Patch(
			ctx, "kubectl-catalog-preflight-test", types.ApplyPatchType, data,
			metav1.PatchOptions{FieldManager: fieldManager, DryRun: []string{"All"}},
		)
		if err != nil {
			if errors.IsForbidden(err) {
				return nil, fmt.Errorf("insufficient RBAC permissions to create Deployments in namespace %q: %w", a.namespace, err)
			}
			// Other errors during dry-run may be expected (e.g., namespace doesn't exist yet)
			if !errors.IsNotFound(err) {
				warnings = append(warnings, fmt.Sprintf("dry-run permission check returned: %v", err))
			}
		}
	}

	return warnings, nil
}

// CleanupWebhookConfigurations scans for ValidatingWebhookConfiguration and
// MutatingWebhookConfiguration resources that reference services in the given
// namespace and deletes them. Returns the number of configurations removed.
func (a *Applier) CleanupWebhookConfigurations(ctx context.Context, namespace string) (int, error) {
	cleaned := 0

	for _, gvr := range []schema.GroupVersionResource{
		{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"},
		{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"},
	} {
		list, err := a.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // skip if the resource type is unavailable
		}

		for _, item := range list.Items {
			webhooks, found, _ := unstructured.NestedSlice(item.Object, "webhooks")
			if !found {
				continue
			}

			referencesNamespace := false
			for _, wh := range webhooks {
				whMap, ok := wh.(map[string]interface{})
				if !ok {
					continue
				}
				svcNS, found, _ := unstructured.NestedString(whMap, "clientConfig", "service", "namespace")
				if found && svcNS == namespace {
					referencesNamespace = true
					break
				}
			}

			if !referencesNamespace {
				continue
			}

			if a.dryRun {
				fmt.Printf("  Would delete %s/%s (dry-run)\n", item.GetKind(), item.GetName())
				cleaned++
				continue
			}

			propagation := metav1.DeletePropagationForeground
			if err := a.dynamicClient.Resource(gvr).Delete(ctx, item.GetName(), metav1.DeleteOptions{
				PropagationPolicy: &propagation,
			}); err != nil {
				if !errors.IsNotFound(err) {
					return cleaned, fmt.Errorf("deleting %s %q: %w", item.GetKind(), item.GetName(), err)
				}
			} else {
				fmt.Printf("  Deleted %s/%s\n", item.GetKind(), item.GetName())
				cleaned++
			}
		}
	}

	return cleaned, nil
}

func crdGroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
}

func deploymentGroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}
}
