package olm

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
)

func fakeDiscoveryWithOLM() *fakediscovery.FakeDiscovery {
	fake := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "operators.coreos.com/v1alpha1",
			APIResources: []metav1.APIResource{
				{Name: "subscriptions", Kind: "Subscription", Namespaced: true},
				{Name: "catalogsources", Kind: "CatalogSource", Namespaced: true},
				{Name: "clusterserviceversions", Kind: "ClusterServiceVersion", Namespaced: true},
				{Name: "installplans", Kind: "InstallPlan", Namespaced: true},
			},
		},
		{
			GroupVersion: "operators.coreos.com/v1",
			APIResources: []metav1.APIResource{
				{Name: "operatorgroups", Kind: "OperatorGroup", Namespaced: true},
			},
		},
	}
	return fake
}

func fakeDiscoveryWithoutOLM() *fakediscovery.FakeDiscovery {
	fake := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true},
			},
		},
	}
	return fake
}

func fakeDiscoveryPartialOLM() *fakediscovery.FakeDiscovery {
	fake := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "operators.coreos.com/v1alpha1",
			APIResources: []metav1.APIResource{
				{Name: "subscriptions", Kind: "Subscription", Namespaced: true},
				// missing catalogsources
			},
		},
		{
			GroupVersion: "operators.coreos.com/v1",
			APIResources: []metav1.APIResource{
				{Name: "operatorgroups", Kind: "OperatorGroup", Namespaced: true},
			},
		},
	}
	return fake
}

func TestIsOLMInstalled_AllPresent(t *testing.T) {
	disc := fakeDiscoveryWithOLM()
	installed, err := IsOLMInstalled(disc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Fatal("expected OLM to be detected as installed")
	}
}

func TestIsOLMInstalled_NotPresent(t *testing.T) {
	disc := fakeDiscoveryWithoutOLM()
	installed, err := IsOLMInstalled(disc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Fatal("expected OLM to be detected as not installed")
	}
}

func TestIsOLMInstalled_PartialInstall(t *testing.T) {
	disc := fakeDiscoveryPartialOLM()
	installed, err := IsOLMInstalled(disc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Fatal("expected partial OLM to be detected as not installed")
	}
}

func TestRequireOLM_Installed(t *testing.T) {
	disc := fakeDiscoveryWithOLM()
	if err := RequireOLM(disc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequireOLM_NotInstalled(t *testing.T) {
	disc := fakeDiscoveryWithoutOLM()
	err := RequireOLM(disc)
	if err == nil {
		t.Fatal("expected error when OLM is not installed")
	}
}
