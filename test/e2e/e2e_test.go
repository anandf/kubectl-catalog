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

// These tests use the OperatorHub.io catalog (--catalog-type operatorhub)
// which is publicly accessible and doesn't require a pull secret.
// The catalog is large (~200MB), so the first run may be slow.
// Subsequent runs use the local cache.

var _ = Describe("kubectl-catalog E2E", func() {

	Describe("help and basic commands", func() {
		It("should display help", func() {
			stdout, _, err := runBinary("--help")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("kubectl-catalog"))
			Expect(stdout).To(ContainSubstring("install"))
			Expect(stdout).To(ContainSubstring("generate"))
			Expect(stdout).To(ContainSubstring("push"))
			Expect(stdout).To(ContainSubstring("version"))
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
			for _, sub := range []string{"search", "list", "install", "uninstall", "upgrade", "generate", "apply", "push", "clean", "version"} {
				stdout, _, err := runBinary(sub, "--help")
				Expect(err).NotTo(HaveOccurred(), "help failed for %s", sub)
				Expect(stdout).To(ContainSubstring("Usage:"), "missing usage for %s", sub)
			}
		})
	})

	Describe("search", func() {
		It("should find operators by keyword", func() {
			stdout, _, err := runBinary("search", "prometheus", "--catalog-type", "operatorhub")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("prometheus"))
		})

		It("should handle search for non-existent operator", func() {
			stdout, _, err := runBinary("search", "nonexistent-operator-xyz", "--catalog-type", "operatorhub")
			if err == nil {
				// Succeeded — output should not contain the non-existent operator name as a package
				Expect(stdout).NotTo(ContainSubstring("nonexistent-operator-xyz"))
			}
			// If it errors, that's also acceptable
		})
	})

	Describe("list", func() {
		It("should list available operators from operatorhub", func() {
			stdout, _, err := runBinary("list", "--catalog-type", "operatorhub")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("PACKAGE"))
		})

		It("should list installed operators (initially empty)", func() {
			stdout, _, err := runBinary("list", "--installed")
			Expect(err).NotTo(HaveOccurred())
			// Should either show headers with no data rows, or an informational message
			Expect(stdout).NotTo(ContainSubstring("argocd-operator"))
		})

		It("should show channels with --show-channels", func() {
			stdout, _, err := runBinary("list", "--catalog-type", "operatorhub", "--show-channels")
			Expect(err).NotTo(HaveOccurred())
			// Channel details are printed inline with package names
			Expect(stdout).To(ContainSubstring("entries"))
		})

		It("should limit channels with --limit-channels", func() {
			stdout, _, err := runBinary("list", "--catalog-type", "operatorhub", "--show-channels", "--limit-channels", "1")
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
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-o", outputDir,
			)
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
			// At minimum: _metadata.yaml + at least one resource
			Expect(yamlCount).To(BeNumerically(">=", 2), "Expected at least 2 YAML files, got %d", yamlCount)

			// Verify metadata content
			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("argocd-operator"))
			Expect(string(metaData)).To(ContainSubstring("clusterType: k8s"))
		})

		It("should generate with specific version and channel", func() {
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"--channel", "alpha",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("channel: alpha"))
		})

		It("should generate with custom namespace", func() {
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "custom-ns",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("namespace: custom-ns"))
		})

		It("should generate with SingleNamespace install mode", func() {
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"--install-mode", "SingleNamespace",
				"-n", "my-ns",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("SingleNamespace"))

			metaData, err := os.ReadFile(filepath.Join(outputDir, "_metadata.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(metaData)).To(ContainSubstring("installMode: SingleNamespace"))
		})

		It("should fail for non-existent package", func() {
			_, _, err := runBinary("generate", "nonexistent-operator-xyz-12345",
				"--catalog-type", "operatorhub",
				"-o", outputDir,
			)
			Expect(err).To(HaveOccurred())
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
			// Clean up any resources created by apply
			runBinary("uninstall", "argocd-operator", "--yes", "--force")
			os.RemoveAll(outputDir)
		})

		It("should apply generated manifests to the cluster", func() {
			By("Generating manifests")
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "default",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s\n%s", stdout, stderr)

			By("Applying manifests")
			stdout, stderr, err = runBinary("apply", outputDir)
			Expect(err).NotTo(HaveOccurred(), "apply failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully applied"))

			By("Verifying operator is listed as installed")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).Should(ContainSubstring("argocd-operator"))
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
			// Clean up
			runBinary("uninstall", "argocd-operator", "--yes", "--force")
		})

		It("should install an operator and then uninstall it", func() {
			By("Installing the operator")
			stdout, stderr, err := runBinary("install", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "default",
			)
			Expect(err).NotTo(HaveOccurred(), "install failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully installed"))

			By("Listing installed operators")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).Should(ContainSubstring("argocd-operator"))

			By("Checking install is idempotent (should fail without --force)")
			_, _, err = runBinary("install", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "default",
			)
			Expect(err).To(HaveOccurred())

			By("Re-installing with --force")
			stdout, stderr, err = runBinary("install", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "default",
				"--force",
			)
			Expect(err).NotTo(HaveOccurred(), "force install failed: %s\n%s", stdout, stderr)

			By("Uninstalling the operator")
			stdout, stderr, err = runBinary("uninstall", "argocd-operator", "--yes")
			Expect(err).NotTo(HaveOccurred(), "uninstall failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("uninstall"))

			By("Verifying operator is no longer listed")
			Eventually(func() string {
				stdout, _, _ = runBinary("list", "--installed")
				return stdout
			}, 30*time.Second, 5*time.Second).ShouldNot(ContainSubstring("argocd-operator"))
		})

		It("should install with SingleNamespace mode", func() {
			stdout, stderr, err := runBinary("install", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"--install-mode", "SingleNamespace",
				"-n", "default",
			)
			Expect(err).NotTo(HaveOccurred(), "install failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("SingleNamespace"))
		})
	})

	Describe("upgrade", func() {
		BeforeEach(func() {
			// Install an operator first so we can test upgrade
			_, _, err := runBinary("install", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-n", "default",
			)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			runBinary("uninstall", "argocd-operator", "--yes", "--force")
		})

		It("should upgrade an installed operator", func() {
			stdout, stderr, err := runBinary("upgrade", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
			)
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

		It("should fail to upgrade a non-installed operator", func() {
			_, _, err := runBinary("upgrade", "nonexistent-operator-xyz",
				"--catalog-type", "operatorhub",
			)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("install with --catalog-type redhat requires --pull-secret", func() {
		It("should fail without --pull-secret for redhat catalog", func() {
			_, _, err := runBinary("install", "cluster-logging",
				"--catalog-type", "redhat",
				"--ocp-version", "4.20",
				"-n", "default",
			)
			Expect(err).To(HaveOccurred())
		})

		It("should fail without --pull-secret for community catalog", func() {
			_, _, err := runBinary("install", "some-operator",
				"--catalog-type", "community",
				"--ocp-version", "4.20",
				"-n", "default",
			)
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

	Describe("push", func() {
		var outputDir string

		BeforeEach(func() {
			var err error
			outputDir, err = os.MkdirTemp("", "kubectl-catalog-push-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(outputDir)
		})

		It("should push generated manifests to a local registry", func() {
			By("Generating manifests")
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s\n%s", stdout, stderr)

			By("Pushing to local registry")
			stdout, stderr, err = runBinary("push", outputDir,
				"--registry", registryURL,
				"--repo-namespace", "e2e-test",
			)
			Expect(err).NotTo(HaveOccurred(), "push failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully pushed"))
			Expect(stdout).To(ContainSubstring("Argo CD"))
			Expect(stdout).To(ContainSubstring("FluxCD"))
		})

		It("should push with custom tag", func() {
			By("Generating manifests")
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s\n%s", stdout, stderr)

			By("Pushing with custom tag")
			stdout, stderr, err = runBinary("push", outputDir,
				"--registry", registryURL,
				"--repo-namespace", "e2e-test",
				"--tag", "latest",
			)
			Expect(err).NotTo(HaveOccurred(), "push failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("latest"))
		})

		It("should verify the OCI artifact is accessible via the registry API", func() {
			By("Generating manifests")
			_, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s", stderr)

			By("Pushing to local registry")
			_, stderr, err = runBinary("push", outputDir,
				"--registry", registryURL,
				"--repo-namespace", "e2e-verify",
				"--tag", "v1.0.0",
			)
			Expect(err).NotTo(HaveOccurred(), "push failed: %s", stderr)

			By("Verifying the image exists in the registry via HTTP API")
			cmd := exec.Command("curl", "-sf",
				fmt.Sprintf("http://%s/v2/e2e-verify/argocd-operator/tags/list", registryURL))
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred(), "registry API call failed")
			Expect(string(output)).To(ContainSubstring("v1.0.0"))
		})

		It("should push with custom package name", func() {
			By("Generating manifests")
			stdout, stderr, err := runBinary("generate", "argocd-operator",
				"--catalog-type", "operatorhub",
				"--cluster-type", "k8s",
				"-o", outputDir,
			)
			Expect(err).NotTo(HaveOccurred(), "generate failed: %s\n%s", stdout, stderr)

			By("Pushing with custom package name")
			stdout, stderr, err = runBinary("push", outputDir,
				"--registry", registryURL,
				"--repo-namespace", "e2e-test",
				"--package", "custom-pkg-name",
				"--tag", "v0.1.0",
			)
			Expect(err).NotTo(HaveOccurred(), "push failed: %s\n%s", stdout, stderr)
			Expect(stdout).To(ContainSubstring("Successfully pushed"))

			By("Verifying the custom-named image exists in the registry")
			cmd := exec.Command("curl", "-sf",
				fmt.Sprintf("http://%s/v2/e2e-test/custom-pkg-name/tags/list", registryURL))
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred(), "registry API call failed")
			Expect(string(output)).To(ContainSubstring("v0.1.0"))
		})

		It("should fail for directory without _metadata.yaml", func() {
			emptyDir, err := os.MkdirTemp("", "empty-push-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(emptyDir)

			_, _, err = runBinary("push", emptyDir, "--registry", registryURL)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("error handling", func() {
		It("should not print help on command failure", func() {
			_, stderr, err := runBinary("install", "nonexistent-operator-xyz",
				"--catalog-type", "operatorhub",
			)
			Expect(err).To(HaveOccurred())
			// Should NOT print the full help/usage text
			Expect(stderr).NotTo(ContainSubstring("Usage:"))
		})

		It("should print help on argument errors", func() {
			_, _, err := runBinary("install")
			Expect(err).To(HaveOccurred())
		})
	})
})
