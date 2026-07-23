package olm

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const pollInterval = 2 * time.Second

func waitForCatalogSource(ctx context.Context, client dynamic.Interface, name, namespace string, timeout time.Duration) error {
	fmt.Printf("  Waiting for CatalogSource %q to be ready...\n", name)
	resource := client.Resource(CatalogSourceGVR()).Namespace(namespace)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		obj, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			state := catalogSourceState(obj)
			if state == "READY" {
				fmt.Printf("  CatalogSource %q is ready\n", name)
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("timed out waiting for CatalogSource %q to become ready", name)
}

func catalogSourceState(obj *unstructured.Unstructured) string {
	state, _, _ := unstructured.NestedString(obj.Object, "status", "connectionState", "lastObservedState")
	return state
}

func waitForSubscription(ctx context.Context, client dynamic.Interface, name, namespace string, timeout time.Duration) (string, error) {
	fmt.Printf("  Waiting for Subscription %q to install CSV...\n", name)
	resource := client.Resource(SubscriptionGVR()).Namespace(namespace)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		obj, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			csvName, found, _ := unstructured.NestedString(obj.Object, "status", "installedCSV")
			if found && csvName != "" {
				fmt.Printf("  Subscription installed CSV: %s\n", csvName)
				return csvName, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return "", fmt.Errorf("timed out waiting for Subscription %q to report installedCSV", name)
}

func waitForCSV(ctx context.Context, client dynamic.Interface, name, namespace string, timeout time.Duration) error {
	fmt.Printf("  Waiting for CSV %q to succeed...\n", name)
	resource := client.Resource(CSVGVR()).Namespace(namespace)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		obj, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			switch phase {
			case "Succeeded":
				fmt.Printf("  CSV %q succeeded\n", name)
				return nil
			case "Failed":
				msg, _, _ := unstructured.NestedString(obj.Object, "status", "message")
				return fmt.Errorf("CSV %q failed: %s", name, msg)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("timed out waiting for CSV %q to succeed", name)
}
