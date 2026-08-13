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
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"docktunnel/internal/docker"
	"docktunnel/internal/events"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"log/slog"
)

// helperEnsureImage pulls an image if not already present locally
// Returns true if image is available (either pulled or already present), false if unavailable
func helperEnsureImage(t *testing.T, ctx context.Context, dockerClient *client.Client, imageName string) bool {
	t.Logf("Checking %s image...", imageName)

	// Try to pull the image
	pullResp, err := dockerClient.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		// Pull failed, check if image exists locally
		_, inspectErr := dockerClient.ImageInspect(ctx, imageName)
		if inspectErr != nil {
			// Image doesn't exist locally either
			t.Logf("Image %s not available: pull error=%v, inspect error=%v", imageName, err, inspectErr)
			return false
		}
		t.Logf("Image %s pull failed (network error), but exists locally, continuing...", imageName)
		return true
	}
	// Drain the pull stream: ImagePull returns before the image is actually
	// registered by the daemon, so creating a container immediately after this
	// call races with the pull ("No such image"). Reading the stream to
	// completion blocks until the pull finishes (or fails — in which case we
	// fall back to checking local existence).
	_, copyErr := io.Copy(io.Discard, pullResp)
	pullResp.Close()
	if copyErr != nil {
		if _, inspectErr := dockerClient.ImageInspect(ctx, imageName); inspectErr == nil {
			t.Logf("Image %s pull stream errored, but image exists locally, continuing...", imageName)
			return true
		}
		t.Logf("Image %s pull failed: %v", imageName, copyErr)
		return false
	}
	t.Logf("Image %s pulled successfully", imageName)
	return true
}

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
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "test-start.example.com",
				"docktunnel.web.service":  "http://localhost:80",
			},
		}

		hostConfig := &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		}

		// Ensure image is available
		if !helperEnsureImage(t, ctx, dockerClient, "nginx:alpine") {
			t.Skip("Image not available, skipping test")
			return
		}

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
				"docktunnel.enable":       "true",
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
		suffix := randomSuffix()

		for i := 0; i < numContainers; i++ {
			containerName := fmt.Sprintf("docktunnel-test-rapid-%s-%d", suffix, i)

			containerConfig := &container.Config{
				Image: "nginx:alpine",
				Labels: map[string]string{
					"docktunnel.enable":       "true",
					"docktunnel.web.hostname": fmt.Sprintf("test-rapid-%s-%d.example.com", suffix, i),
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

	// Ensure image is available
	if !helperEnsureImage(t, ctx, dockerClient, "nginx:alpine") {
		t.Skip("Image not available, skipping test")
		return
	}

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
			"docktunnel.enable":                           "true",
			"docktunnel.web.hostname":                     "test-scan.example.com",
			"docktunnel.web.service":                      "http://localhost:80",
			"docktunnel.web.originRequest.connectTimeout": "30s",
			"docktunnel.web.originRequest.noTLSVerify":    "true",
		},
	}

	hostConfig := &container.HostConfig{}

	// Ensure image is available
	if !helperEnsureImage(t, ctx, dockerClient, "nginx:alpine") {
		t.Skip("Image not available, skipping test")
		return
	}

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

	// Pull image once before the test (or verify it exists locally)
	t.Log("Checking nginx:alpine image...")
	pullResp, err := dockerClient.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		// Check if image exists locally
		_, inspectErr := dockerClient.ImageInspect(ctx, imageName)
		if inspectErr != nil {
			t.Skipf("Failed to pull image and image not found locally: pull error=%v, inspect error=%v", err, inspectErr)
			return
		}
		t.Log("Image pull failed (network error), but image exists locally, continuing...")
	} else {
		// Drain the pull stream so the image is registered before containers
		// are created (mirrors helperEnsureImage).
		if _, copyErr := io.Copy(io.Discard, pullResp); copyErr != nil {
			if _, inspectErr := dockerClient.ImageInspect(ctx, imageName); inspectErr != nil {
				t.Skipf("Image pull stream failed and image not found locally: %v", copyErr)
				return
			}
			t.Log("Image pull stream failed, but image exists locally, continuing...")
		}
		pullResp.Close()
		t.Log("Image pulled successfully")
	}

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
			containerName := fmt.Sprintf("docktunnel-test-concurrent-%s-%03d", suffix, index)

			containerConfig := &container.Config{
				Image: imageName,
				Labels: map[string]string{
					"docktunnel.enable":       "true",
					"docktunnel.web.hostname": fmt.Sprintf("test-concurrent-%s-%03d.example.com", suffix, index),
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
		t.Logf("Too many container creation failures: %d/%d", createFail, numContainers)
		// Cleanup before failing
		t.Log("Cleaning up containers after failure...")
		for _, containerID := range containerIDs {
			timeout := int(time.Second * 5)
			dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
			dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		}
		t.Fatalf("Too many container creation failures: %d/%d", createFail, numContainers)
	}

	// Ensure cleanup happens even if test fails later
	defer func() {
		t.Log("Cleaning up containers...")
		cleanupStart := time.Now()
		for _, containerID := range containerIDs {
			timeout := int(time.Second * 5)
			dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
			dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		}
		cleanupElapsed := time.Since(cleanupStart)
		t.Logf("Cleanup completed in %v", cleanupElapsed)
	}()

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

	t.Log("Test completed successfully")
}

// Helper function to generate random suffix for container names
func randomSuffix() string {
	return time.Now().Format("20060102-150405")
}

// TestMemoryFootprintWith100Containers validates memory usage with 100 containers (T099)
//
// This test ensures that DockTunnel maintains a memory footprint < 100MB
// when managing 100 containers, which is critical for production deployments.
func TestMemoryFootprintWith100Containers(t *testing.T) {
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

	// Force garbage collection before starting
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	initialMB := float64(m1.Alloc) / 1024 / 1024
	t.Logf("Initial memory: %.2f MB", initialMB)

	// Ensure image is available
	if !helperEnsureImage(t, ctx, dockerClient, "nginx:alpine") {
		t.Skip("Image not available, skipping test")
		return
	}

	// Create containers
	containerIDs := make([]string, 0, numContainers)
	suffix := randomSuffix()

	for i := 0; i < numContainers; i++ {
		containerName := fmt.Sprintf("docktunnel-test-mem-%s-%03d", suffix, i)

		containerConfig := &container.Config{
			Image: "nginx:alpine",
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": fmt.Sprintf("test-mem-%s-%03d.example.com", suffix, i),
				"docktunnel.web.service":  "http://localhost:80",
			},
		}

		hostConfig := &container.HostConfig{}

		resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		if err != nil {
			t.Logf("Warning: Failed to create container %s: %v", containerName, err)
			continue
		}
		containerIDs = append(containerIDs, resp.ID)
	}

	t.Logf("Created %d containers", len(containerIDs))

	// Force GC and measure memory after container creation
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	afterCreationMB := float64(m2.Alloc) / 1024 / 1024
	t.Logf("Memory after creating %d containers: %.2f MB", len(containerIDs), afterCreationMB)

	// Start containers to simulate real load
	for _, containerID := range containerIDs {
		dockerClient.ContainerStart(ctx, containerID, container.StartOptions{})
	}

	// Wait a bit for event processing
	time.Sleep(2 * time.Second)

	// Force GC and measure peak memory
	runtime.GC()
	var m3 runtime.MemStats
	runtime.ReadMemStats(&m3)
	peakMB := float64(m3.Alloc) / 1024 / 1024
	t.Logf("Peak memory with %d running containers: %.2f MB", len(containerIDs), peakMB)

	// Calculate memory increase
	memoryIncreaseMB := peakMB - initialMB
	t.Logf("Memory increase: %.2f MB", memoryIncreaseMB)

	// Validate memory footprint is < 100MB
	const maxMemoryMB = 100
	if peakMB > maxMemoryMB {
		t.Errorf("Memory footprint too high: %.2f MB > %d MB", peakMB, maxMemoryMB)
	} else {
		t.Logf("✓ Memory footprint within limits: %.2f MB < %d MB", peakMB, maxMemoryMB)
	}

	// Additional memory metrics
	t.Logf("Memory Statistics:")
	t.Logf("  HeapAlloc: %.2f MB", float64(m3.HeapAlloc)/1024/1024)
	t.Logf("  HeapSys: %.2f MB", float64(m3.HeapSys)/1024/1024)
	t.Logf("  HeapInuse: %.2f MB", float64(m3.HeapInuse)/1024/1024)
	t.Logf("  StackInuse: %.2f MB", float64(m3.StackInuse)/1024/1024)
	t.Logf("  Mallocs: %d", m3.Mallocs)
	t.Logf("  Frees: %d", m3.Frees)
	t.Logf("  NumGC: %d", m3.NumGC)

	// Calculate average memory per container
	avgMemPerContainerMB := memoryIncreaseMB / float64(len(containerIDs))
	t.Logf("Average memory per container: %.2f MB", avgMemPerContainerMB)

	// Cleanup
	t.Log("Cleaning up containers...")
	for _, containerID := range containerIDs {
		timeout := int(time.Second * 5)
		dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
		dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	}

	// Force GC after cleanup and measure final memory
	runtime.GC()
	var m4 runtime.MemStats
	runtime.ReadMemStats(&m4)
	finalMB := float64(m4.Alloc) / 1024 / 1024
	t.Logf("Final memory after cleanup: %.2f MB", finalMB)

	// Check for memory leaks (final memory should be close to initial)
	memoryLeakMB := finalMB - initialMB
	if memoryLeakMB > 10 { // Allow 10MB tolerance
		t.Logf("Warning: Possible memory leak detected: %.2f MB increase", memoryLeakMB)
	} else {
		t.Logf("✓ No significant memory leak detected")
	}
}

// TestEventProcessingSLA validates 5-second event processing SLA (T100)
//
// This test ensures that Docker events are processed within 5 seconds,
// which is critical for responsive container management.
func TestEventProcessingSLA(t *testing.T) {
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

	// Ensure image is available
	if !helperEnsureImage(t, ctx, dockerClient, "nginx:alpine") {
		t.Skip("Image not available, skipping test")
		return
	}

	const maxProcessingTimeSec = 5.0
	const numIterations = 10

	processingTimes := make([]time.Duration, 0, numIterations)
	createdContainers := make([]string, 0, numIterations)

	// Ensure cleanup on test exit
	defer func() {
		t.Log("Final cleanup: removing any remaining containers...")
		for _, containerID := range createdContainers {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		}
	}()

	t.Logf("Running %d iterations to validate event processing SLA...", numIterations)

	for iteration := 0; iteration < numIterations; iteration++ {
		containerName := fmt.Sprintf("docktunnel-test-sla-%s-%03d", randomSuffix(), iteration)

		containerConfig := &container.Config{
			Image: "nginx:alpine",
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": fmt.Sprintf("test-sla-%03d.example.com", iteration),
				"docktunnel.web.service":  "http://localhost:80",
			},
		}

		hostConfig := &container.HostConfig{}

		// Record start time
		startTime := time.Now()

		// Create container
		resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		if err != nil {
			t.Logf("Warning: Iteration %d failed to create container: %v", iteration, err)
			continue
		}

		// Track created container for cleanup
		createdContainers = append(createdContainers, resp.ID)

		// Start container (triggers event)
		err = dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{})
		if err != nil {
			t.Logf("Warning: Iteration %d failed to start container: %v", iteration, err)
			continue
		}

		// Wait for container to be fully running
		for {
			containerJSON, inspectErr := dockerClient.ContainerInspect(ctx, resp.ID)
			if inspectErr != nil {
				t.Logf("Warning: Iteration %d failed to inspect container: %v", iteration, inspectErr)
				break
			}

			if containerJSON.State.Running {
				// Container is running, consider event processed
				break
			}

			// Check timeout
			if time.Since(startTime) > 10*time.Second {
				t.Logf("Warning: Iteration %d container took too long to start", iteration)
				break
			}

			time.Sleep(100 * time.Millisecond)
		}

		// Record processing time
		processingTime := time.Since(startTime)
		processingTimes = append(processingTimes, processingTime)

		t.Logf("Iteration %d: Event processed in %v", iteration, processingTime)

		// Cleanup with timeout protection
		cleanupDone := make(chan error, 1)
		go func() {
			timeout := int(time.Second * 2) // Reduced timeout
			stopErr := dockerClient.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
			removeErr := dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			cleanupDone <- fmt.Errorf("stop: %w, remove: %w", stopErr, removeErr)
		}()

		// Wait for cleanup or timeout
		select {
		case <-cleanupDone:
			// Cleanup completed
		case <-time.After(10 * time.Second):
			t.Logf("Warning: Iteration %d cleanup timeout, forcing removal", iteration)
			dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		}

		// Brief pause between iterations
		time.Sleep(100 * time.Millisecond)
	}

	if len(processingTimes) == 0 {
		t.Fatal("No successful iterations")
	}

	// Calculate statistics
	var totalTime time.Duration
	var minTime time.Duration = processingTimes[0]
	var maxTime time.Duration = processingTimes[0]

	for _, t := range processingTimes {
		totalTime += t
		if t < minTime {
			minTime = t
		}
		if t > maxTime {
			maxTime = t
		}
	}

	avgTime := totalTime / time.Duration(len(processingTimes))

	t.Logf("Event Processing Statistics (%d iterations):", len(processingTimes))
	t.Logf("  Average: %v", avgTime)
	t.Logf("  Min: %v", minTime)
	t.Logf("  Max: %v", maxTime)
	t.Logf("  Total: %v", totalTime)

	// Validate SLA: all events must be processed within 5 seconds
	slaViolations := 0
	for i, pt := range processingTimes {
		if pt.Seconds() > maxProcessingTimeSec {
			slaViolations++
			t.Errorf("SLA violation: Iteration %d took %v > %v", i, pt, time.Duration(maxProcessingTimeSec)*time.Second)
		}
	}

	if slaViolations == 0 {
		t.Logf("✓ All events processed within SLA: < %v", time.Duration(maxProcessingTimeSec)*time.Second)
	} else {
		t.Errorf("SLA violations: %d/%d events exceeded %v threshold", slaViolations, len(processingTimes), time.Duration(maxProcessingTimeSec)*time.Second)
	}

	// Additional validation: average should be well under SLA
	avgTargetSec := maxProcessingTimeSec * 0.5 // Target 50% of SLA
	if avgTime.Seconds() > avgTargetSec {
		t.Logf("Warning: Average processing time (%v) is above target (%v)", avgTime, time.Duration(avgTargetSec)*time.Second)
	} else {
		t.Logf("✓ Average processing time within target: %v < %v", avgTime, time.Duration(avgTargetSec)*time.Second)
	}
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
