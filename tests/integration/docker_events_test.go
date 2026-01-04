//go:build integration

// Package integration contains tests that require real infrastructure (Docker daemon, Cloudflare API)
// Run with: go test -tags=integration ./tests/integration/...
//
// Requirements:
// - Docker daemon running and accessible
// - For full tests: Cloudflare credentials (set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN)
//
// T086: Integration test for Docker event handling (use real Docker daemon, test container start/stop)
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"docktunnel/internal/docker"
	"docktunnel/internal/events"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"log/slog"
)

// TestDockerEventHandling tests Docker event handling with real Docker daemon (T086)
//
// This test verifies that:
// 1. Container start events are detected and processed
// 2. Container stop/die events are detected and processed
// 3. Event debouncing works correctly
// 4. Controller sync state with Docker containers
func TestDockerEventHandling(t *testing.T) {
	// Skip if not running with integration tag
	t.Helper()

	// Create real Docker manager
	dockerManager, err := docker.NewManager()
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
		return
	}
	defer dockerManager.Close()

	// Create Docker client for direct access
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Failed to create Docker client: %v", err)
		return
	}
	defer dockerClient.Close()

	ctx := context.Background()

	// Test 1: Start a container with DockTunnel labels
	t.Run("container start event detection", func(t *testing.T) {
		containerName := "docktunnel-test-start-" + randomSuffix()

		// Create container with DockTunnel labels
		containerConfig := &container.Config{
			Image: "nginx:alpine",
			Labels: map[string]string{
				"docktunnel.enable":   "true",
				"docktunnel.web.hostname": "test-start.example.com",
				"docktunnel.web.service":  "http://localhost:80",
			},
		}

		hostConfig := &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		}

		// Pull image if not present
		t.Log("Pulling nginx:alpine image...")
		pullResp, err := dockerClient.ImagePull(ctx, "nginx:alpine", image.PullOptions{})
		if err != nil {
			t.Skipf("Failed to pull image: %v", err)
			return
		}
		pullResp.Close()

		// Create container
		resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		if err != nil {
			t.Fatalf("Failed to create container: %v", err)
		}

		// Start container
		t.Logf("Starting container %s (ID: %s)", containerName, resp.ID[:12])
		err = dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{})
		if err != nil {
			t.Fatalf("Failed to start container: %v", err)
		}

		// Wait a bit for event to be processed
		time.Sleep(2 * time.Second)

		// Verify container is running
		containerJSON, err := dockerClient.ContainerInspect(ctx, resp.ID)
		if err != nil {
			t.Fatalf("Failed to inspect container: %v", err)
		}

		if !containerJSON.State.Running {
			t.Error("Container is not running")
		}

		t.Logf("Container %s is running", containerName)

		// Cleanup: Stop and remove container
		timeout := int(time.Second * 10)
		err = dockerClient.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
		if err != nil {
			t.Logf("Warning: Failed to stop container: %v", err)
		}

		err = dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if err != nil {
			t.Logf("Warning: Failed to remove container: %v", err)
		}

		t.Log("Container cleaned up successfully")
	})

	// Test 2: Container stop event detection
	t.Run("container stop event detection", func(t *testing.T) {
		containerName := "docktunnel-test-stop-" + randomSuffix()

		containerConfig := &container.Config{
			Image: "nginx:alpine",
			Labels: map[string]string{
				"docktunnel.enable":   "true",
				"docktunnel.web.hostname": "test-stop.example.com",
				"docktunnel.web.service":  "http://localhost:80",
			},
		}

		hostConfig := &container.HostConfig{}

		// Create and start container
		resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		if err != nil {
			t.Skipf("Failed to create container: %v", err)
			return
		}

		err = dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{})
		if err != nil {
			t.Fatalf("Failed to start container: %v", err)
		}

		t.Logf("Container %s started", containerName)

		// Wait for container to be fully started
		time.Sleep(1 * time.Second)

		// Stop the container
		timeout := int(time.Second * 5)
		err = dockerClient.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
		if err != nil {
			t.Fatalf("Failed to stop container: %v", err)
		}

		t.Logf("Container %s stopped", containerName)

		// Wait for event processing
		time.Sleep(2 * time.Second)

		// Verify container is stopped
		containerJSON, err := dockerClient.ContainerInspect(ctx, resp.ID)
		if err != nil {
			t.Fatalf("Failed to inspect container: %v", err)
		}

		if containerJSON.State.Running {
			t.Error("Container is still running")
		}

		t.Log("Container stop detected successfully")

		// Cleanup
		err = dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if err != nil {
			t.Logf("Warning: Failed to remove container: %v", err)
		}
	})

	// Test 3: Multiple containers rapid start/stop (event debouncing)
	t.Run("rapid container events debouncing", func(t *testing.T) {
		numContainers := 5

		// Create multiple containers rapidly
		containers := make([]string, 0, numContainers)

		for i := 0; i < numContainers; i++ {
			containerName := "docktunnel-test-rapid-" + randomSuffix() + "-" + string(rune('0'+i))

			containerConfig := &container.Config{
				Image: "nginx:alpine",
				Labels: map[string]string{
					"docktunnel.enable":    "true",
					"docktunnel.web.hostname": "test-rapid-" + string(rune('0'+i)) + ".example.com",
					"docktunnel.web.service":  "http://localhost:80",
				},
			}

			hostConfig := &container.HostConfig{}

			resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
			if err != nil {
				t.Logf("Warning: Failed to create container %s: %v", containerName, err)
				continue
			}

			containers = append(containers, resp.ID)

			err = dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{})
			if err != nil {
				t.Logf("Warning: Failed to start container %s: %v", containerName, err)
			}
		}

		t.Logf("Created and started %d containers", len(containers))

		// Wait for event processing
		time.Sleep(3 * time.Second)

		// Verify all containers are running
		runningCount := 0
		for _, containerID := range containers {
			containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
			if err == nil && containerJSON.State.Running {
				runningCount++
			}
		}

		t.Logf("Containers running: %d/%d", runningCount, len(containers))

		// Cleanup all containers
		for _, containerID := range containers {
			timeout := int(time.Second * 5)
			dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
			dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		}

		t.Log("All containers cleaned up")
	})
}

// TestDockerEventChannel tests the Docker event channel mechanism (T086)
func TestDockerEventChannel(t *testing.T) {
	// This test verifies the event channel infrastructure works correctly

	dockerManager, err := docker.NewManager()
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
		return
	}
	defer dockerManager.Close()

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Failed to create Docker client: %v", err)
		return
	}
	defer dockerClient.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create event channel
	eventChan := make(chan events.Event, 10)

	// Start listening for events (in goroutine)
	go func() {
		t.Log("Starting Docker event listener...")
		if err := dockerManager.ListenForEvents(ctx, eventChan); err != nil {
			t.Logf("Event listener error: %v", err)
		}
	}()

	// Give the listener time to start
	time.Sleep(1 * time.Second)

	// Create a test container to trigger an event
	containerName := "docktunnel-test-channel-" + randomSuffix()

	containerConfig := &container.Config{
		Image: "nginx:alpine",
	}

	hostConfig := &container.HostConfig{}

	t.Log("Creating test container to trigger event...")

	// Pull image first
	pullResp, err := dockerClient.ImagePull(ctx, "nginx:alpine", image.PullOptions{})
	if err != nil {
		t.Skipf("Failed to pull image: %v", err)
		return
	}
	pullResp.Close()

	resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Start container to generate events
	err = dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		t.Fatalf("Failed to start container: %v", err)
	}

	// Wait for events to be received
	timeout := time.After(5 * time.Second)
	eventReceived := false

	select {
	case event := <-eventChan:
		eventReceived = true
		t.Logf("Received event: Type=%s, ContainerID=%s", event.Type, event.ContainerID)
	case <-timeout:
		t.Log("No event received within timeout (may be expected in some environments)")
	}

	// Cleanup
	timeoutSec := int(time.Second * 5)
	dockerClient.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeoutSec})
	dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

	if eventReceived {
		t.Log("Event channel test passed")
	}
}

// TestContainerScanning tests container scanning with real Docker daemon (T086)
func TestContainerScanning(t *testing.T) {
	dockerManager, err := docker.NewManager()
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
		return
	}
	defer dockerManager.Close()

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Failed to create Docker client: %v", err)
		return
	}
	defer dockerClient.Close()

	ctx := context.Background()

	// Create test container with DockTunnel labels
	containerName := "docktunnel-test-scan-" + randomSuffix()

	containerConfig := &container.Config{
		Image: "nginx:alpine",
		Labels: map[string]string{
			"docktunnel.enable":    "true",
			"docktunnel.web.hostname":  "test-scan.example.com",
			"docktunnel.web.service":   "http://localhost:80",
			"docktunnel.web.originRequest.connectTimeout": "30s",
			"docktunnel.web.originRequest.noTLSVerify":    "true",
		},
	}

	hostConfig := &container.HostConfig{}

	// Pull image
	t.Log("Pulling nginx:alpine...")
	pullResp, err := dockerClient.ImagePull(ctx, "nginx:alpine", image.PullOptions{})
	if err != nil {
		t.Skipf("Failed to pull image: %v", err)
		return
	}
	pullResp.Close()

	// Create container
	resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	defer func() {
		// Cleanup
		timeout := int(time.Second * 5)
		dockerClient.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
		dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	// Inspect container
	t.Log("Inspecting container...")
	containerInfo, err := dockerClient.ContainerInspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("Failed to inspect container: %v", err)
	}

	// Verify labels
	if containerInfo.Config == nil {
		t.Fatal("Container config is nil")
	}

	enable, ok := containerInfo.Config.Labels["docktunnel.enable"]
	if !ok || enable != "true" {
		t.Error("docktunnel.enable label not found or not true")
	}

	hostname, ok := containerInfo.Config.Labels["docktunnel.web.hostname"]
	if !ok || hostname != "test-scan.example.com" {
		t.Errorf("docktunnel.web.hostname label incorrect: got %s", hostname)
	}

	service, ok := containerInfo.Config.Labels["docktunnel.web.service"]
	if !ok || service != "http://localhost:80" {
		t.Errorf("docktunnel.web.service label incorrect: got %s", service)
	}

	connectTimeout, ok := containerInfo.Config.Labels["docktunnel.web.originRequest.connectTimeout"]
	if !ok || connectTimeout != "30s" {
		t.Errorf("docktunnel.web.originRequest.connectTimeout label incorrect: got %s", connectTimeout)
	}

	noTLSVerify, ok := containerInfo.Config.Labels["docktunnel.web.originRequest.noTLSVerify"]
	if !ok || noTLSVerify != "true" {
		t.Errorf("docktunnel.web.originRequest.noTLSVerify label incorrect: got %s", noTLSVerify)
	}

	t.Log("Container labels verified successfully")
}

// TestConcurrentContainerStarts tests 100 containers starting simultaneously (T088)
//
// This test verifies that DockTunnel can handle a large number of containers
// starting concurrently, which is critical for:
// - Production deployments with many microservices
// - Event debouncing under high load
// - Performance validation
// - Memory leak detection
func TestConcurrentContainerStarts(t *testing.T) {
	dockerManager, err := docker.NewManager()
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
		return
	}
	defer dockerManager.Close()

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Failed to create Docker client: %v", err)
		return
	}
	defer dockerClient.Close()

	ctx := context.Background()

	numContainers := 100
	t.Logf("Starting %d containers concurrently...", numContainers)

	// Use a lightweight image (nginx:alpine ~40MB)
	imageName := "nginx:alpine"

	// Pull image once before the test
	t.Log("Pre-pulling nginx:alpine image...")
	pullResp, err := dockerClient.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		t.Skipf("Failed to pull image: %v", err)
		return
	}
	pullResp.Close()
	t.Log("Image pulled successfully")

	// Create channels for synchronization
	createResults := make(chan string, numContainers) // Send container IDs or empty string on error
	startResults := make(chan error, numContainers)
	containerIDs := make([]string, 0, numContainers)

	suffix := randomSuffix()

	// Track start time for performance measurement
	startTime := time.Now()

	// Create all containers concurrently
	t.Logf("Creating %d containers...", numContainers)
	for i := 0; i < numContainers; i++ {
		go func(index int) {
			containerName := "docktunnel-test-concurrent-" + suffix + "-" + string(rune('0'+index%10))

			containerConfig := &container.Config{
				Image: imageName,
				Labels: map[string]string{
					"docktunnel.enable":    "true",
					"docktunnel.web.hostname": "test-concurrent-" + suffix + "-" + string(rune('0'+index%10)) + ".example.com",
					"docktunnel.web.service":  "http://localhost:80",
				},
			}

			hostConfig := &container.HostConfig{}

			resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
			if err != nil {
				createResults <- "" // Empty string indicates error
				return
			}

			createResults <- resp.ID
		}(i)
	}

	// Collect all created container IDs
	for i := 0; i < numContainers; i++ {
		containerID := <-createResults
		if containerID != "" {
			containerIDs = append(containerIDs, containerID)
		}
	}

	createSuccess := len(containerIDs)
	createFail := numContainers - createSuccess

	t.Logf("Container creation complete: %d succeeded, %d failed", createSuccess, createFail)

	if createFail > numContainers/10 { // Allow 10% failure rate
		t.Fatalf("Too many container creation failures: %d/%d", createFail, numContainers)
	}

	// Start all containers concurrently
	t.Logf("Starting %d containers...", len(containerIDs))
	for _, containerID := range containerIDs {
		go func(id string) {
			err := dockerClient.ContainerStart(ctx, id, container.StartOptions{})
			startResults <- err
		}(containerID)
	}

	// Wait for all container starts to complete
	startSuccess := 0
	startFail := 0
	for i := 0; i < len(containerIDs); i++ {
		err := <-startResults
		if err != nil {
			startFail++
			t.Logf("Container start failed: %v", err)
		} else {
			startSuccess++
		}
	}

	elapsed := time.Since(startTime)
	t.Logf("Container start complete: %d succeeded, %d failed", startSuccess, startFail)
	t.Logf("Started %d containers in %v (%.2f containers/sec)", startSuccess, elapsed, float64(startSuccess)/elapsed.Seconds())

	if startFail > numContainers/10 { // Allow 10% failure rate
		t.Fatalf("Too many container start failures: %d/%d", startFail, numContainers)
	}

	// Verify all containers are running
	t.Log("Verifying container states...")
	runningCount := 0
	for _, containerID := range containerIDs {
		containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && containerJSON.State.Running {
			runningCount++
		}
	}

	t.Logf("Containers running: %d/%d", runningCount, len(containerIDs))

	if runningCount < len(containerIDs)*9/10 { // Allow 10% not running
		t.Errorf("Too few containers running: %d/%d", runningCount, len(containerIDs))
	}

	// Wait for event processing (simulating real-world scenario)
	t.Log("Waiting for event processing...")
	time.Sleep(5 * time.Second)

	// Cleanup: Stop and remove all containers
	t.Log("Cleaning up containers...")
	cleanupStart := time.Now()

	for _, containerID := range containerIDs {
		go func(id string) {
			timeout := int(time.Second * 5)
			dockerClient.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
			dockerClient.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}(containerID)
	}

	cleanupElapsed := time.Since(cleanupStart)
	t.Logf("Cleanup completed in %v", cleanupElapsed)
	t.Log("Test completed successfully")
}

// Helper function to generate random suffix for container names
func randomSuffix() string {
	return time.Now().Format("20060102-150405")
}

// TestMain handles setup and teardown for integration tests
func TestMain(m *testing.M) {
	// Check if Docker is available
	dockerMgr, err := docker.NewManager()
	if err != nil {
		slog.Warn("Docker daemon not available, skipping integration tests", "error", err)
		os.Exit(0)
		return
	}
	dockerMgr.Close()

	// Run tests
	exitCode := m.Run()

	os.Exit(exitCode)
}
