package applier

import (
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/anandf/kubectl-catalog/pkg/bundle"
	"github.com/anandf/kubectl-catalog/pkg/state"
)

const (
	fieldManager = "kubectl-catalog"

	// Timeouts for waiting on resources
	crdEstablishTimeout    = 60 * time.Second
	deploymentReadyTimeout = 5 * time.Minute
	pollInterval           = 2 * time.Second

	// PullSecretName is the name of the image pull secret created in the target namespace.
	PullSecretName = "kubectl-catalog-pull-secret"
)

// InstallContext holds the tracking metadata for the current install/upgrade operation.
type InstallContext struct {
	PackageName string
	Version     string
	Channel     string
	BundleName  string
	CatalogRef  string
}

// Applier applies Kubernetes manifests to a cluster using server-side apply.
type Applier struct {
	dynamicClient   dynamic.Interface
	discoveryClient discovery.DiscoveryInterface
	mapper          meta.RESTMapper
	namespace       string
}

func New(kubeconfig, namespace string) (*Applier, error) {
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

	return &Applier{
		dynamicClient:   dynClient,
		discoveryClient: disc,
		mapper:          mapper,
		namespace:       namespace,
	}, nil
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
	annotations := state.TrackingAnnotations(ic.Version, ic.Channel, ic.BundleName, ic.CatalogRef)

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
	a.mapper = restmapper.NewDiscoveryRESTMapper(groupResources)
	return nil
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

func (a *Applier) applyObject(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("finding REST mapping for %s: %w", gvk, err)
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if obj.GetNamespace() == "" {
			obj.SetNamespace(a.namespace)
		}
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
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
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
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	resource := a.dynamicClient.Resource(secretGVR).Namespace(a.namespace)

	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      PullSecretName,
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

	_, err = resource.Patch(ctx, PullSecretName, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("applying pull secret: %w", err)
	}

	fmt.Printf("  Pull secret %q created in namespace %q (type: kubernetes.io/dockerconfigjson)\n", PullSecretName, a.namespace)
	return nil
}

// PatchServiceAccountPullSecret adds the kubectl-catalog pull secret to a ServiceAccount's
// imagePullSecrets list. This ensures pods using this SA can pull images from
// authenticated registries.
func (a *Applier) PatchServiceAccountPullSecret(ctx context.Context, saName string) error {
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
			if psMap["name"] == PullSecretName {
				return nil // already present
			}
		}
	}

	// Patch to add the pull secret reference
	patch := map[string]interface{}{
		"imagePullSecrets": append(pullSecrets, map[string]interface{}{
			"name": PullSecretName,
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
	crdGVR := crdGroupVersionResource()

	for _, crd := range crds {
		crdName := crd.GetName()
		fmt.Printf("    Waiting for CRD %s to be established...\n", crdName)

		resource := a.dynamicClient.Resource(crdGVR)
		deadline := time.Now().Add(crdEstablishTimeout)

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
	deployGVR := deploymentGroupVersionResource()

	for _, dep := range deployments {
		depName := dep.GetName()
		ns := dep.GetNamespace()
		if ns == "" {
			ns = a.namespace
		}

		fmt.Printf("    Waiting for Deployment %s to be ready...\n", depName)

		resource := a.dynamicClient.Resource(deployGVR).Namespace(ns)
		deadline := time.Now().Add(deploymentReadyTimeout)

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
	desiredReplicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")

	if desiredReplicas == 0 {
		desiredReplicas = 1 // default
	}

	if replicas == desiredReplicas &&
		readyReplicas == desiredReplicas &&
		updatedReplicas == desiredReplicas {
		return true, ""
	}

	return false, fmt.Sprintf("waiting for replicas: %d/%d ready, %d/%d updated",
		readyReplicas, desiredReplicas, updatedReplicas, desiredReplicas)
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
