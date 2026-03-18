package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These tests use the community operator catalog from registry.redhat.io
// (--catalog-type community --ocp-version 4.20) which is publicly accessible
// without a pull secret. Operator bundle images are hosted on quay.io.
//
// The test operator is argocd-operator, chosen because:
//   - It is available in the community catalog
//   - Its bundle images are on quay.io (no auth required)
//   - It supports multiple install modes and channels

const (
	testCatalogType = "community"
	testOCPVersion  = "4.20"
	testOperator    = "argocd-operator"
	testClusterType = "k8s"
)

// catalogFlags returns the standard catalog flags for all tests.
func catalogFlags() []string {
	return []string{
		"--catalog-type", testCatalogType,
		"--ocp-version", testOCPVersion,
		"--cluster-type", testClusterType,
	}
}

var _ = Describe("kubectl-catalog E2E", func() {

	Describe("help and basic commands", func() {
		It("should display help with all subcommands", func() {
			stdout, _, err := runBinary("--help")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("kubectl-catalog"))
			for _, sub := range []string{"install", "generate", "apply", "upgrade", "uninstall", "search", "list", "status", "clean", "version", "completion"} {
				Expect(stdout).To(ContainSubstring(sub), "missing subcommand %q in help output", sub)
			}
		})

		It("should display version information with real values", func() {
			stdout, _, err := runBinary("version")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("kubectl-catalog version"))
			Expect(stdout).To(ContainSubstring("go version:"))
			// Built with ldflags — version should NOT be "dev"
			Expect(stdout).NotTo(ContainSubstring("version dev"))
			Expect(stdout).NotTo(ContainSubstring("git commit: unknown"))
		})

		It("should display usage for each subcommand help", func() {
			for _, sub := range []string{"search", "list", "install", "uninstall", "upgrade", "generate", "apply", "status", "clean", "version"} {
				stdout, _, err := runBinary(sub, "--help")
				Expect(err).NotTo(HaveOccurred(), "help failed for %s", sub)
				Expect(stdout).To(ContainSubstring("Usage:"), "missing usage for %s", sub)
			}
		})
	})

	Describe("completion", func() {
		It("should generate bash completion", func() {
			stdout, _, err := runBinary("completion", "bash")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("bash completion"))
		})

		It("should generate zsh completion", func() {
			stdout, _, err := runBinary("completion", "zsh")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("zsh completion"))
		})

		It("should generate fish completion", func() {
			stdout, _, err := runBinary("completion", "fish")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("fish completion"))
		})
	})

	Describe("search", func() {
		It("should find operators by keyword", func() {
			args := append([]string{"search", "argocd"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("argocd"))
		})

		It("should search by GVK with --what-provides", func() {
			args := append([]string{"search", "--what-provides", "argoproj.io/ArgoCD"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("argocd-operator"))
		})

		It("should handle search for non-existent operator", func() {
			args := append([]string{"search", "nonexistent-operator-xyz-99999"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			if err == nil {
				Expect(stdout).NotTo(ContainSubstring("nonexistent-operator-xyz-99999"))
			}
		})
	})

	Describe("list", func() {
		It("should list available operators from community catalog", func() {
			args := append([]string{"list"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("PACKAGE"))
			Expect(stdout).To(ContainSubstring("argocd-operator"))
		})

		It("should list installed operators (initially empty)", func() {
			stdout, _, err := runBinary("list", "--installed")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).NotTo(ContainSubstring(testOperator))
		})

		It("should show channels with --show-channels", func() {
			args := append([]string{"list", "--show-channels"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("entries"))
		})

		It("should limit channels with --limit-channels", func() {
			args := append([]string{"list", "--show-channels", "--limit-channels", "1"}, catalogFlags()...)
			stdout, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("entries"))
		})
	})

	Describe("generate", func() {
		var outputDir string

		BeforeEach(func() {
			var err error
			outputDir, err = os.MkdirTemp("", "kubectl-catalog-generate-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(outputDir)
		})

		It("should generate manifests for an operator", func() {
			args := append([]string{"generate", testOperator, "-o", outputDir}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)

			// Verify _metadata.yaml was created
			_, err = os.Stat(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred(), "_metadata.yaml not found")

			// Verify at least some YAML files were generated
			entries, err := os.ReadDir(outputDir)
			Expect(err).NotTo(HaveOccurred())

			yamlCount := 0
			for _, e := range entries {
				if !e.IsDir() && (filepath.Ext(e.Name()) == ".yaml" || filepath.Ext(e.Name()) == ".yml") {
					yamlCount++
				}
			}
			Expect(yamlCount).To(BeNumerically(">=", 2), "Expected at least 2 YAML files, got %d", yamlCount)

			// Verify metadata content
			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring(testOperator))
			Expect(string(metaData)).To(ContainSubstring("clusterType: k8s"))
		})

		It("should generate with specific channel", func() {
			args := append([]string{"generate", testOperator, "--channel", "alpha", "-o", outputDir}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("channel: alpha"))
		})

		It("should generate with custom namespace", func() {
			args := append([]string{"generate", testOperator, "-n", "custom-ns", "-o", outputDir}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("namespace: custom-ns"))
		})

		It("should generate with SingleNamespace install mode", func() {
			args := append([]string{"generate", testOperator, "--install-mode", "SingleNamespace", "-n", "my-ns", "-o", outputDir}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("SingleNamespace"))

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("installMode: SingleNamespace"))
		})

		It("should fail for non-existent package", func() {
			args := append([]string{"generate", "nonexistent-operator-xyz-12345", "-o", outputDir}, catalogFlags()...)
			_, stderr, err := runBinary(args...)
			Expect(err).To(HaveOccurred())
			// Should include a hint
			Expect(stderr).To(ContainSubstring("Hint:"))
		})
	})

	Describe("generate to OCI registry", func() {
		var outputDir string

		BeforeEach(func() {
			var err error
			outputDir, err = os.MkdirTemp("", "kubectl-catalog-oci-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(outputDir)
		})

		It("should push generated manifests to a local OCI registry", func() {
			ociRef := fmt.Sprintf("oci://%s/e2e-test/%s:v1.0.0", registryURL, testOperator)
			args := append([]string{"generate", testOperator, "-o", ociRef}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "generate+push failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully pushed"))
			Expect(stdout).To(ContainSubstring("Argo CD"))
			Expect(stdout).To(ContainSubstring("FluxCD"))
		})

		It("should push with auto-generated tag from channel", func() {
			// No tag specified — should auto-derive from the channel name
			ociRef := fmt.Sprintf("oci://%s/e2e-test/%s-autotag", registryURL, testOperator)
			args := append([]string{"generate", testOperator, "-o", ociRef}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "generate+push failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully pushed"))
		})

		It("should verify the OCI artifact is accessible via the registry API", func() {
			tag := "v2.0.0"
			ociRef := fmt.Sprintf("oci://%s/e2e-verify/%s:%s", registryURL, testOperator, tag)
			args := append([]string{"generate", testOperator, "-o", ociRef}, catalogFlags()...)
			_, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "generate+push failed: %s", stderr)

			By("Verifying the image exists in the registry via HTTP API")
			cmd := exec.Command("curl", "-sf",
				fmt.Sprintf("http://%s/v2/e2e-verify/%s/tags/list", registryURL, testOperator))
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred(), "registry API call failed")
			Expect(string(output)).To(ContainSubstring(tag))
		})
	})

	Describe("apply", func() {
		var outputDir string

		BeforeEach(func() {
			var err error
			outputDir, err = os.MkdirTemp("", "kubectl-catalog-apply-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			runBinary("uninstall", testOperator)
			os.RemoveAll(outputDir)
		})

		It("should apply generated manifests to the cluster", func() {
			By("Generating manifests")
			args := append([]string{"generate", testOperator, "-n", "default", "-o", outputDir}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s\n%s", stdout, stderr)

			By("Applying manifests")
			stdout, stderr, err = runBinary("apply", outputDir)
			Expect(err).NotTo(HaveOccurred(), "apply failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully applied"))

			By("Verifying operator is listed as installed")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).Should(ContainSubstring(testOperator))
		})

		It("should fail for directory without _metadata.yaml", func() {
			emptyDir, err := os.MkdirTemp("", "empty-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(emptyDir)

			_, _, err = runBinary("apply", emptyDir)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("install and uninstall", func() {
		AfterEach(func() {
			runBinary("uninstall", testOperator)
		})

		It("should install an operator and then uninstall it", func() {
			By("Installing the operator")
			args := append([]string{"install", testOperator, "-n", "default"}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "install failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully installed"))

			By("Listing installed operators")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).Should(ContainSubstring(testOperator))

			By("Checking install is idempotent (should fail without --force)")
			args = append([]string{"install", testOperator, "-n", "default"}, catalogFlags()...)
			_, _, err = runBinary(args...)
			Expect(err).To(HaveOccurred())

			By("Re-installing with --force")
			args = append([]string{"install", testOperator, "-n", "default", "--force"}, catalogFlags()...)
			stdout, stderr, err = runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "force install failed: %s\n%s", stdout, stderr)

			By("Uninstalling the operator")
			stdout, stderr, err = runBinary("uninstall", testOperator)
			Expect(err).NotTo(HaveOccurred(), "uninstall failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("uninstall"))

			By("Verifying operator is no longer listed")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).ShouldNot(ContainSubstring(testOperator))
		})

		It("should install with SingleNamespace mode", func() {
			args := append([]string{"install", testOperator, "--install-mode", "SingleNamespace", "-n", "default"}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred(), "install failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("SingleNamespace"))
		})
	})

	Describe("status", func() {
		BeforeEach(func() {
			args := append([]string{"install", testOperator, "-n", "default", "--no-wait"}, catalogFlags()...)
			_, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			runBinary("uninstall", testOperator)
		})

		It("should show status of an installed operator", func() {
			stdout, stderr, err := runBinary("status", testOperator)
			Expect(err).NotTo(HaveOccurred(), "status failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Package:"))
			Expect(stdout).To(ContainSubstring("Version:"))
			Expect(stdout).To(ContainSubstring("Channel:"))
			Expect(stdout).To(ContainSubstring("DEPLOYMENTS:"))
		})

		It("should show status with events", func() {
			stdout, stderr, err := runBinary("status", testOperator, "--show-events")
			Expect(err).NotTo(HaveOccurred(), "status --show-events failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("RECENT EVENTS:"))
		})

		It("should fail for non-installed operator with a hint", func() {
			_, stderr, err := runBinary("status", "nonexistent-operator-xyz")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("Hint:"))
		})
	})

	Describe("upgrade", func() {
		BeforeEach(func() {
			args := append([]string{"install", testOperator, "-n", "default"}, catalogFlags()...)
			_, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			runBinary("uninstall", testOperator)
		})

		It("should upgrade or report already at latest", func() {
			args := append([]string{"upgrade", testOperator}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			// Upgrade may succeed or report "already at latest" — both are valid
			if err != nil {
				combined := stdout + stderr
				Expect(combined).To(SatisfyAny(
					ContainSubstring("already"),
					ContainSubstring("latest"),
					ContainSubstring("no upgrade"),
				))
			} else {
				Expect(stdout).To(SatisfyAny(
					ContainSubstring("Successfully upgraded"),
					ContainSubstring("Upgrade"),
				))
			}
		})

		It("should show diff without applying when --diff is used", func() {
			args := append([]string{"upgrade", testOperator, "--diff"}, catalogFlags()...)
			stdout, stderr, err := runBinary(args...)
			// May succeed (showing diff) or fail (already at latest)
			if err == nil {
				Expect(stdout).To(ContainSubstring("Summary:"))
				Expect(stdout).To(ContainSubstring("No changes applied"))
			} else {
				combined := stdout + stderr
				Expect(combined).To(SatisfyAny(
					ContainSubstring("already"),
					ContainSubstring("latest"),
					ContainSubstring("no upgrade"),
				))
			}
		})

		It("should fail to upgrade a non-installed operator with a hint", func() {
			_, stderr, err := runBinary("upgrade", "nonexistent-operator-xyz")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("Hint:"))
		})
	})

	Describe("error handling and hints", func() {
		It("should require --ocp-version for redhat catalog type with a hint", func() {
			_, stderr, err := runBinary("search", "test", "--catalog-type", "redhat")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("--ocp-version"))
		})

		It("should require --pull-secret for redhat catalog type", func() {
			_, stderr, err := runBinary("install", "cluster-logging",
				"--catalog-type", "redhat",
				"--ocp-version", "4.20",
				"-n", "default",
			)
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("pull-secret"))
		})

		It("should NOT require --pull-secret for community catalog type", func() {
			// Community catalog at registry.redhat.io is publicly accessible
			args := append([]string{"search", "argocd"}, catalogFlags()...)
			_, _, err := runBinary(args...)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should show hint when package not found", func() {
			args := append([]string{"install", "nonexistent-operator-xyz-12345", "-n", "default"}, catalogFlags()...)
			_, stderr, err := runBinary(args...)
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("Hint:"))
		})

		It("should not print help on command failure", func() {
			args := append([]string{"install", "nonexistent-operator-xyz", "-n", "default"}, catalogFlags()...)
			_, stderr, err := runBinary(args...)
			Expect(err).To(HaveOccurred())
			Expect(stderr).NotTo(ContainSubstring("Usage:"))
		})

		It("should print help on argument errors", func() {
			_, _, err := runBinary("install")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("clean", func() {
		It("should clean cache without error", func() {
			stdout, stderr, err := runBinary("clean")
			Expect(err).NotTo(HaveOccurred(), "clean failed: %s\n%s", stdout, stderr)
		})

		It("should clean only catalogs", func() {
			stdout, stderr, err := runBinary("clean", "--catalogs")
			Expect(err).NotTo(HaveOccurred(), "clean --catalogs failed: %s\n%s", stdout, stderr)
		})

		It("should clean only bundles", func() {
			stdout, stderr, err := runBinary("clean", "--bundles")
			Expect(err).NotTo(HaveOccurred(), "clean --bundles failed: %s\n%s", stdout, stderr)
		})
	})
})
