package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var statusCmd = &cobra.Command{
	Use:   "status <package-name>",
	Short: "Show the status of an installed operator",
	Long: `Display the health and status of an installed operator.

Shows:
  - Installed version, channel, and catalog reference
  - Deployment status (ready replicas, available conditions)
  - Pod health (running, pending, crash-looping pods)
  - Recent events related to the operator's namespace
  - CRD status (established, names served)

Examples:
  kubectl catalog status cluster-logging
  kubectl catalog status cluster-logging --show-events`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create state manager: %w", err)
		}

		installed, err := stateManager.GetInstalled(ctx, packageName)
		if err != nil {
			return withHint(
				fmt.Errorf("package %q is not installed: %w", packageName, err),
				"run 'kubectl catalog list --installed' to see installed packages",
			)
		}

		// Build a dynamic client for querying pods, events, etc.
		dynClient, err := newDynamicClient()
		if err != nil {
			return fmt.Errorf("failed to create dynamic client: %w", err)
		}

		// Determine the operator namespace from tracked resources
		resources, err := stateManager.ResourcesForPackage(ctx, packageName)
		if err != nil {
			return fmt.Errorf("failed to get resources for package: %w", err)
		}
		operatorNS := determineOperatorNamespace(resources)
		if operatorNS == "" {
			operatorNS = namespace
		}

		// Print install info
		fmt.Printf("Package:   %s\n", installed.PackageName)
		fmt.Printf("Version:   %s\n", installed.Version)
		fmt.Printf("Channel:   %s\n", installed.Channel)
		fmt.Printf("Catalog:   %s\n", installed.CatalogRef)
		fmt.Printf("Bundle:    %s\n", installed.BundleName)
		fmt.Printf("Namespace: %s\n", operatorNS)

		// Resource summary
		resourceCounts := make(map[string]int)
		for _, r := range installed.Resources {
			resourceCounts[r.Kind]++
		}
		fmt.Printf("Resources: %d total", len(installed.Resources))
		if len(resourceCounts) > 0 {
			var kinds []string
			for k, c := range resourceCounts {
				kinds = append(kinds, fmt.Sprintf("%d %s", c, k))
			}
			sort.Strings(kinds)
			fmt.Printf(" (%s)", strings.Join(kinds, ", "))
		}
		fmt.Println()

		// Deployment status
		fmt.Println("\nDEPLOYMENTS:")
		deployGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
		deploymentFound := false
		for _, r := range installed.Resources {
			if r.Kind != "Deployment" {
				continue
			}
			deploymentFound = true
			ns := r.Namespace
			if ns == "" {
				ns = operatorNS
			}
			dep, err := dynClient.Resource(deployGVR).Namespace(ns).Get(ctx, r.Name, metav1.GetOptions{})
			if err != nil {
				fmt.Printf("  %-40s ERROR: %v\n", r.Name, err)
				continue
			}
			printDeploymentStatus(dep)
		}
		if !deploymentFound {
			fmt.Println("  (no deployments found)")
		}

		// Pod status
		fmt.Println("\nPODS:")
		podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
		labelSelector := fmt.Sprintf("%s=%s", state.LabelPackage, packageName)
		pods, err := dynClient.Resource(podGVR).Namespace(operatorNS).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err == nil && len(pods.Items) > 0 {
			for _, pod := range pods.Items {
				printPodStatus(&pod)
			}
		} else {
			// Try finding pods by matching deployment labels
			foundPods := findOperatorPods(ctx, dynClient, installed, operatorNS)
			if len(foundPods) > 0 {
				for _, pod := range foundPods {
					printPodStatus(&pod)
				}
			} else {
				fmt.Println("  (no pods found)")
			}
		}

		// CRD status
		crdCount := 0
		for _, r := range installed.Resources {
			if r.Kind == "CustomResourceDefinition" {
				crdCount++
			}
		}
		if crdCount > 0 {
			fmt.Println("\nCRDs:")
			crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
			for _, r := range installed.Resources {
				if r.Kind != "CustomResourceDefinition" {
					continue
				}
				crd, err := dynClient.Resource(crdGVR).Get(ctx, r.Name, metav1.GetOptions{})
				if err != nil {
					fmt.Printf("  %-50s ERROR: %v\n", r.Name, err)
					continue
				}
				printCRDStatus(crd)
			}
		}

		// Events
		if showEvents {
			fmt.Println("\nRECENT EVENTS:")
			printRecentEvents(ctx, dynClient, operatorNS)
		}

		return nil
	},
}

var showEvents bool

func printDeploymentStatus(dep *unstructured.Unstructured) {
	desired, _, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas")
	if desired == 0 {
		desired = 1
	}
	ready, _, _ := unstructured.NestedInt64(dep.Object, "status", "readyReplicas")
	available, _, _ := unstructured.NestedInt64(dep.Object, "status", "availableReplicas")
	updated, _, _ := unstructured.NestedInt64(dep.Object, "status", "updatedReplicas")

	status := "Ready"
	if ready < desired {
		status = "NotReady"
	}

	// Check conditions for more detail
	conditions, found, _ := unstructured.NestedSlice(dep.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			cond, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			condType, _ := cond["type"].(string)
			condStatus, _ := cond["status"].(string)
			if condType == "Progressing" && condStatus == "False" {
				msg, _ := cond["message"].(string)
				status = "ProgressDeadlineExceeded"
				if msg != "" {
					status = fmt.Sprintf("Failed: %s", truncate(msg, 60))
				}
			}
		}
	}

	fmt.Printf("  %-40s %d/%d ready, %d updated, %d available  [%s]\n",
		dep.GetName(), ready, desired, updated, available, status)
}

func printPodStatus(pod *unstructured.Unstructured) {
	phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")

	// Check container statuses for restarts and crash loops
	restarts := int64(0)
	containerStatuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	readyContainers := 0
	totalContainers := len(containerStatuses)

	for _, cs := range containerStatuses {
		csMap, ok := cs.(map[string]interface{})
		if !ok {
			continue
		}
		r, _, _ := unstructured.NestedInt64(csMap, "restartCount")
		restarts += r
		isReady, _, _ := unstructured.NestedBool(csMap, "ready")
		if isReady {
			readyContainers++
		}

		// Check for CrashLoopBackOff
		waiting, found, _ := unstructured.NestedMap(csMap, "state", "waiting")
		if found {
			reason, _ := waiting["reason"].(string)
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				phase = reason
			}
		}
	}

	age := podAge(pod)
	fmt.Printf("  %-40s %-20s %d/%d ready  %d restarts  %s\n",
		pod.GetName(), phase, readyContainers, totalContainers, restarts, age)
}

func printCRDStatus(crd *unstructured.Unstructured) {
	established := false
	conditions, found, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			cond, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cond["type"] == "Established" && cond["status"] == "True" {
				established = true
			}
		}
	}

	status := "NotEstablished"
	if established {
		status = "Established"
	}

	fmt.Printf("  %-50s [%s]\n", crd.GetName(), status)
}

func printRecentEvents(ctx context.Context, dynClient dynamic.Interface, ns string) {
	eventGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}
	events, err := dynClient.Resource(eventGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("  (could not list events: %v)\n", err)
		return
	}

	if len(events.Items) == 0 {
		fmt.Println("  (no events)")
		return
	}

	// Sort by last timestamp descending, show most recent 10
	sort.Slice(events.Items, func(i, j int) bool {
		ti := eventTime(&events.Items[i])
		tj := eventTime(&events.Items[j])
		return ti.After(tj)
	})

	limit := 10
	if len(events.Items) < limit {
		limit = len(events.Items)
	}

	// Only show events from the last hour
	cutoff := time.Now().Add(-1 * time.Hour)
	printed := 0
	for _, event := range events.Items[:limit] {
		t := eventTime(&event)
		if t.Before(cutoff) {
			continue
		}
		eventType, _, _ := unstructured.NestedString(event.Object, "type")
		reason, _, _ := unstructured.NestedString(event.Object, "reason")
		message, _, _ := unstructured.NestedString(event.Object, "message")
		involvedName, _, _ := unstructured.NestedString(event.Object, "involvedObject", "name")
		involvedKind, _, _ := unstructured.NestedString(event.Object, "involvedObject", "kind")
		ago := time.Since(t).Truncate(time.Second)

		fmt.Printf("  %-8s %-6s %-20s %-20s %s\n",
			ago.String()+" ago", eventType, reason, involvedKind+"/"+involvedName, truncate(message, 60))
		printed++
	}

	if printed == 0 {
		fmt.Println("  (no recent events in the last hour)")
	}
}

// findOperatorPods finds pods owned by deployments that belong to the operator.
func findOperatorPods(ctx context.Context, dynClient dynamic.Interface, installed *state.InstalledOperator, ns string) []unstructured.Unstructured {
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	// Get all pods in the namespace and match by owner references
	deployNames := make(map[string]bool)
	for _, r := range installed.Resources {
		if r.Kind == "Deployment" {
			deployNames[r.Name] = true
		}
	}

	if len(deployNames) == 0 {
		return nil
	}

	pods, err := dynClient.Resource(podGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}

	var matched []unstructured.Unstructured
	for _, pod := range pods.Items {
		owners := pod.GetOwnerReferences()
		for _, owner := range owners {
			// Pods are owned by ReplicaSets, which are owned by Deployments.
			// Check if the pod's labels match the deployment name pattern.
			if owner.Kind == "ReplicaSet" {
				// ReplicaSet names are <deployment-name>-<hash>
				for depName := range deployNames {
					if strings.HasPrefix(owner.Name, depName+"-") {
						matched = append(matched, pod)
						break
					}
				}
			}
		}
	}
	return matched
}

func eventTime(event *unstructured.Unstructured) time.Time {
	lastTimestamp, _, _ := unstructured.NestedString(event.Object, "lastTimestamp")
	if lastTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, lastTimestamp); err == nil {
			return t
		}
	}
	// Fallback to metadata.creationTimestamp
	return event.GetCreationTimestamp().Time
}

func podAge(pod *unstructured.Unstructured) string {
	created := pod.GetCreationTimestamp().Time
	if created.IsZero() {
		return ""
	}
	d := time.Since(created)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func newDynamicClient() (dynamic.Interface, error) {
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
	return dynamic.NewForConfig(config)
}

func init() {
	statusCmd.Flags().BoolVar(&showEvents, "show-events", false, "show recent events in the operator's namespace")
	statusCmd.ValidArgsFunction = completeInstalledPackages
	rootCmd.AddCommand(statusCmd)
}
