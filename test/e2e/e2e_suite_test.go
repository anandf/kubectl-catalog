package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	kindClusterName = "kubectl-catalog-e2e"
	registryName    = "kubectl-catalog-registry"
	registryPort    = "5555"
)

var (
	binaryPath  string
	kubeconfig  string
	projectRoot string
	cacheDir    string
	registryURL string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "kubectl-catalog E2E Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error

	// Determine project root (two levels up from test/e2e)
	projectRoot, err = filepath.Abs(filepath.Join("..", ".."))
	Expect(err).NotTo(HaveOccurred())

	// Read version from VERSION file
	By("Building kubectl-catalog binary")
	binaryPath = filepath.Join(projectRoot, "kubectl-catalog-e2e")
	versionBytes, err := os.ReadFile(filepath.Join(projectRoot, "VERSION"))
	Expect(err).NotTo(HaveOccurred(), "Failed to read VERSION file")
	version := strings.TrimSpace(string(versionBytes))

	// Get git commit
	gitCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	gitCmd.Dir = projectRoot
	commitBytes, err := gitCmd.Output()
	gitCommit := "unknown"
	if err == nil {
		gitCommit = strings.TrimSpace(string(commitBytes))
	}

	ldflags := fmt.Sprintf("-X github.com/anandf/kubectl-catalog/cmd.version=%s -X github.com/anandf/kubectl-catalog/cmd.gitCommit=%s -X github.com/anandf/kubectl-catalog/cmd.buildDate=e2e-test",
		version, gitCommit)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", binaryPath, ".")
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to build binary: %s", string(output))

	// Create a temp cache directory for tests
	cacheDir, err = os.MkdirTemp("", "kubectl-catalog-e2e-cache-*")
	Expect(err).NotTo(HaveOccurred())

	// Start a local OCI registry for generate-to-OCI tests
	By("Starting local OCI registry")
	startLocalRegistry()

	// Create kind cluster
	By("Creating kind cluster")
	createKindCluster()

	// Set kubeconfig
	kubeconfig = filepath.Join(os.TempDir(), "kubectl-catalog-e2e-kubeconfig")
	cmd = exec.Command("kind", "get", "kubeconfig", "--name", kindClusterName)
	kubeconfigData, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "Failed to get kubeconfig")
	err = os.WriteFile(kubeconfig, kubeconfigData, 0o600)
	Expect(err).NotTo(HaveOccurred())

	// Wait for the cluster to be ready
	By("Waiting for cluster to be ready")
	Eventually(func() error {
		cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-o", "name")
		_, err := cmd.Output()
		return err
	}, 60*time.Second, 2*time.Second).Should(Succeed())

	return nil
}, func(_ []byte) {
	// For parallel processes — re-derive paths
	var err error
	projectRoot, err = filepath.Abs(filepath.Join("..", ".."))
	Expect(err).NotTo(HaveOccurred())
	binaryPath = filepath.Join(projectRoot, "kubectl-catalog-e2e")
	kubeconfig = filepath.Join(os.TempDir(), "kubectl-catalog-e2e-kubeconfig")
	registryURL = fmt.Sprintf("localhost:%s", registryPort)
})

var _ = SynchronizedAfterSuite(func() {}, func() {
	By("Deleting kind cluster")
	deleteKindCluster()

	By("Stopping local OCI registry")
	stopLocalRegistry()

	By("Cleaning up")
	if binaryPath != "" {
		os.Remove(binaryPath)
	}
	if kubeconfig != "" {
		os.Remove(kubeconfig)
	}
	if cacheDir != "" {
		os.RemoveAll(cacheDir)
	}
})

// createKindCluster creates a kind cluster for e2e testing.
func createKindCluster() {
	// Delete any existing cluster with the same name
	exec.Command("kind", "delete", "cluster", "--name", kindClusterName).Run()

	cmd := exec.Command("kind", "create", "cluster", "--name", kindClusterName, "--wait", "120s")
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create kind cluster: %s", string(output))
}

// deleteKindCluster removes the kind cluster.
func deleteKindCluster() {
	cmd := exec.Command("kind", "delete", "cluster", "--name", kindClusterName)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to delete kind cluster: %s", string(output))
}

// startLocalRegistry starts a local Docker registry for OCI generate tests.
func startLocalRegistry() {
	// Stop any existing registry
	exec.Command("docker", "rm", "-f", registryName).Run()

	cmd := exec.Command("docker", "run", "-d",
		"--name", registryName,
		"-p", fmt.Sprintf("%s:5000", registryPort),
		"registry:2",
	)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to start local registry: %s", string(output))

	registryURL = fmt.Sprintf("localhost:%s", registryPort)

	// Wait for registry to be ready
	Eventually(func() error {
		cmd := exec.Command("curl", "-sf", fmt.Sprintf("http://%s/v2/", registryURL))
		return cmd.Run()
	}, 15*time.Second, 1*time.Second).Should(Succeed(), "Local registry did not become ready")
}

// stopLocalRegistry removes the local Docker registry.
func stopLocalRegistry() {
	exec.Command("docker", "rm", "-f", registryName).Run()
}

// runBinary executes the kubectl-catalog binary with the given args and returns
// stdout, stderr, and any error.
func runBinary(args ...string) (string, string, error) {
	// Always inject kubeconfig and cache-dir
	fullArgs := append([]string{
		"--kubeconfig", kubeconfig,
		"--cache-dir", cacheDir,
	}, args...)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, fullArgs...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfig))

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("command timed out after 10 minutes: %s", strings.Join(args, " "))
	}
	return stdoutBuf.String(), stderrBuf.String(), err
}
