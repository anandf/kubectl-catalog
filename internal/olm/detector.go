package olm

import (
	"fmt"

	"k8s.io/client-go/discovery"
)

var requiredResources = map[string][]string{
	"operators.coreos.com/v1alpha1": {"subscriptions", "catalogsources"},
	"operators.coreos.com/v1":       {"operatorgroups"},
}

// IsOLMInstalled checks whether OLM is installed on the cluster by verifying
// that the operators.coreos.com API group is registered and serves the required
// resource types (subscriptions, catalogsources, operatorgroups).
func IsOLMInstalled(disc discovery.DiscoveryInterface) (bool, error) {
	for groupVersion, needed := range requiredResources {
		resourceList, err := disc.ServerResourcesForGroupVersion(groupVersion)
		if err != nil {
			return false, nil
		}

		available := make(map[string]bool)
		for _, r := range resourceList.APIResources {
			available[r.Name] = true
		}

		for _, name := range needed {
			if !available[name] {
				return false, nil
			}
		}
	}
	return true, nil
}

// RequireOLM returns an error if OLM is not installed on the cluster.
func RequireOLM(disc discovery.DiscoveryInterface) error {
	installed, err := IsOLMInstalled(disc)
	if err != nil {
		return fmt.Errorf("failed to check for OLM: %w", err)
	}
	if !installed {
		return fmt.Errorf("OLM is not installed on this cluster (operators.coreos.com API group not found)\nHint: install OLM first (https://olm.operatorframework.io/docs/getting-started/) or use --installation-type=direct")
	}
	return nil
}
