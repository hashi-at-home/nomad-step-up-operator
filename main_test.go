package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthHandler tests the health endpoint
func TestHealthHandler(t *testing.T) {
	op := &StepUpOperator{
		logger: setupLogger("error"),
	}

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	op.healthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got '%s'", response["status"])
	}
}

// TestStepUpdateHandler tests the step update endpoint
func TestStepUpdateHandler(t *testing.T) {
	// Skip this test if Consul is not available
	t.Skip("Skipping test that requires Consul connection")
}

// TestStepUpQueryHandler tests the query endpoints
func TestStepUpQueryHandler(t *testing.T) {
	// Skip this test if Consul is not available
	t.Skip("Skipping test that requires Consul connection")
}

// TestNodesHandler tests the nodes endpoint
func TestNodesHandler(t *testing.T) {
	op := &StepUpOperator{
		logger: setupLogger("error"),
	}

	// Test invalid method (this doesn't require Consul)
	req := httptest.NewRequest("POST", "/nodes", nil)
	w := httptest.NewRecorder()
	op.nodesHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST, got %d", w.Code)
	}

	// Test valid GET method (skip if Consul not available)
	t.Run("GET request", func(t *testing.T) {
		t.Skip("Skipping test that requires Consul connection")
	})
}

// TestStepStatusValidation tests step status validation
func TestStepStatusValidation(t *testing.T) {
	validStatuses := []string{"pending", "running", "done", "failed"}

	for _, status := range validStatuses {
		s := StepStatus{
			Node:      "test-node",
			Step:      "test-step",
			Status:    status,
			Timestamp: time.Now(),
		}

		// Validate required fields
		if s.Node == "" || s.Step == "" || s.Status == "" {
			t.Errorf("validation failed for status '%s'", status)
		}
	}
}

// TestNodeStatusStructure tests the NodeStatus structure
func TestNodeStatusStructure(t *testing.T) {
	nodeStatus := NodeStatus{
		Node:        "test-node",
		Locked:      true,
		LockingStep: "install_mise",
		LockID:      "test-lock-123",
		Steps: map[string]StepStatus{
			"install_mise": {
				Node:      "test-node",
				Step:      "install_mise",
				Status:    "running",
				Timestamp: time.Now(),
				LockID:    "test-lock-123",
			},
			"configure_docker": {
				Node:      "test-node",
				Step:      "configure_docker",
				Status:    "pending",
				Timestamp: time.Now(),
			},
		},
		LastUpdated: time.Now(),
	}

	// Test JSON marshaling
	data, err := json.Marshal(nodeStatus)
	if err != nil {
		t.Errorf("failed to marshal NodeStatus: %v", err)
	}

	// Test JSON unmarshaling
	var decoded NodeStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("failed to unmarshal NodeStatus: %v", err)
	}

	if decoded.Node != nodeStatus.Node {
		t.Errorf("node mismatch: expected %s, got %s", nodeStatus.Node, decoded.Node)
	}

	if decoded.Locked != nodeStatus.Locked {
		t.Errorf("locked mismatch: expected %v, got %v", nodeStatus.Locked, decoded.Locked)
	}

	if len(decoded.Steps) != len(nodeStatus.Steps) {
		t.Errorf("steps count mismatch: expected %d, got %d", len(nodeStatus.Steps), len(decoded.Steps))
	}
}

// TestGetEnv tests the getEnv helper function
func TestGetEnv(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		defaultValue string
		expected     string
	}{
		{
			name:         "returns environment value",
			key:          "TEST_ENV_VAR",
			envValue:     "test_value",
			defaultValue: "default",
			expected:     "test_value",
		},
		{
			name:         "returns default when not set",
			key:          "UNSET_VAR",
			envValue:     "",
			defaultValue: "default",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv(tt.key, tt.envValue)
			}
			result := getEnv(tt.key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

// TestGetEnvDuration tests the getEnvDuration helper function
func TestGetEnvDuration(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		defaultValue time.Duration
		expected     time.Duration
	}{
		{
			name:         "parses valid duration",
			key:          "TEST_DURATION_VAR",
			envValue:     "10s",
			defaultValue: 5 * time.Second,
			expected:     10 * time.Second,
		},
		{
			name:         "returns default for invalid duration",
			key:          "TEST_DURATION_VAR",
			envValue:     "not_a_duration",
			defaultValue: 5 * time.Second,
			expected:     5 * time.Second,
		},
		{
			name:         "returns default when not set",
			key:          "UNSET_DURATION_VAR",
			envValue:     "",
			defaultValue: 5 * time.Second,
			expected:     5 * time.Second,
		},
		{
			name:         "parses complex duration",
			key:          "TEST_DURATION_VAR",
			envValue:     "1h30m",
			defaultValue: 5 * time.Second,
			expected:     90 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv(tt.key, tt.envValue)
			}
			result := getEnvDuration(tt.key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestSetupLogger tests logger configuration
func TestSetupLogger(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error", "invalid"}

	for _, level := range levels {
		logger := setupLogger(level)
		if logger == nil {
			t.Errorf("setupLogger returned nil for level '%s'", level)
		}
	}
}

// TestClientHelpers tests the client helper functions
func TestClientHelpers(t *testing.T) {
	client := NewStepUpClient("http://localhost:8080")

	if client == nil {
		t.Error("NewStepUpClient returned nil")
	}

	if client.baseURL != "http://localhost:8080" {
		t.Errorf("expected baseURL 'http://localhost:8080', got '%s'", client.baseURL)
	}

	if client.httpClient == nil {
		t.Error("httpClient is nil")
	}
}

// TestGetAllNodesClient tests the GetAllNodes client method
func TestGetAllNodesClient(t *testing.T) {
	// Create test server
	mux := http.NewServeMux()

	// Mock /nodes endpoint
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		nodes := []NodeStatus{
			{
				Node:        "node1",
				Locked:      false,
				Steps:       make(map[string]StepStatus),
				LastUpdated: time.Now(),
			},
			{
				Node:        "node2",
				Locked:      true,
				LockingStep: "install_docker",
				Steps: map[string]StepStatus{
					"install_docker": {
						Node:   "node2",
						Step:   "install_docker",
						Status: "running",
					},
				},
				LastUpdated: time.Now(),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewStepUpClient(server.URL)

	nodes, err := client.GetAllNodes()
	if err != nil {
		t.Errorf("GetAllNodes failed: %v", err)
	}

	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}

	if nodes[0].Node != "node1" {
		t.Errorf("expected first node to be 'node1', got '%s'", nodes[0].Node)
	}

	if !nodes[1].Locked {
		t.Error("expected second node to be locked")
	}
}

// TestStepUpClient tests the client methods
func TestStepUpClient(t *testing.T) {
	// Create test server
	mux := http.NewServeMux()

	// Mock /step-up POST endpoint
	mux.HandleFunc("/step-up", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var status StepStatus
		json.NewDecoder(r.Body).Decode(&status)
		status.Timestamp = time.Now()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	// Mock /step-up/<node> GET endpoint
	mux.HandleFunc("/step-up/test-node", func(w http.ResponseWriter, r *http.Request) {
		nodeStatus := NodeStatus{
			Node:        "test-node",
			Locked:      false,
			Steps:       make(map[string]StepStatus),
			LastUpdated: time.Now(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodeStatus)
	})

	// Mock /health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewStepUpClient(server.URL)

	t.Run("UpdateStepStatus", func(t *testing.T) {
		status, err := client.UpdateStepStatus("test-node", "install_mise", "running", "Installing mise")
		if err != nil {
			t.Errorf("UpdateStepStatus failed: %v", err)
		}
		if status == nil {
			t.Error("status is nil")
		}
	})

	t.Run("GetNodeStatus", func(t *testing.T) {
		status, err := client.GetNodeStatus("test-node")
		if err != nil {
			t.Errorf("GetNodeStatus failed: %v", err)
		}
		if status == nil {
			t.Error("status is nil")
		}
		if status.Node != "test-node" {
			t.Errorf("expected node 'test-node', got '%s'", status.Node)
		}
	})

	t.Run("Health", func(t *testing.T) {
		if err := client.Health(); err != nil {
			t.Errorf("Health check failed: %v", err)
		}
	})

	t.Run("StartStep", func(t *testing.T) {
		status, err := client.StartStep("test-node", "install_mise")
		if err != nil {
			t.Errorf("StartStep failed: %v", err)
		}
		if status.Status != "running" {
			t.Errorf("expected status 'running', got '%s'", status.Status)
		}
	})

	t.Run("CompleteStep", func(t *testing.T) {
		status, err := client.CompleteStep("test-node", "install_mise", "")
		if err != nil {
			t.Errorf("CompleteStep failed: %v", err)
		}
		if status.Status != "done" {
			t.Errorf("expected status 'done', got '%s'", status.Status)
		}
	})

	t.Run("FailStep", func(t *testing.T) {
		status, err := client.FailStep("test-node", "install_mise", "Installation failed")
		if err != nil {
			t.Errorf("FailStep failed: %v", err)
		}
		if status.Status != "failed" {
			t.Errorf("expected status 'failed', got '%s'", status.Status)
		}
	})
}

// TestWaitForLockRelease tests the lock waiting functionality
func TestWaitForLockRelease(t *testing.T) {
	// Create test server with changing lock status
	lockReleased := false
	mux := http.NewServeMux()

	mux.HandleFunc("/step-up/test-node", func(w http.ResponseWriter, r *http.Request) {
		nodeStatus := NodeStatus{
			Node:        "test-node",
			Locked:      !lockReleased,
			Steps:       make(map[string]StepStatus),
			LastUpdated: time.Now(),
		}

		// Release lock after first request
		lockReleased = true

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodeStatus)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewStepUpClient(server.URL)

	// Should succeed quickly since lock is released after first check
	err := client.WaitForLockRelease("test-node", 5*time.Second)
	if err != nil {
		t.Errorf("WaitForLockRelease failed: %v", err)
	}
}

// TestConfig tests configuration structure
func TestConfig(t *testing.T) {
	cfg := &Config{
		ConsulAddr:   "consul:8500",
		ConsulToken:  "test-token",
		ConsulPrefix: "test-prefix",
		NomadAddr:    "http://nomad:4646",
		NomadToken:   "nomad-token",
		HTTPAddr:     ":8080",
		MetricsAddr:  ":9090",
		LogLevel:     "debug",
	}

	if cfg.ConsulAddr != "consul:8500" {
		t.Errorf("expected ConsulAddr 'consul:8500', got '%s'", cfg.ConsulAddr)
	}

	if cfg.ConsulPrefix != "test-prefix" {
		t.Errorf("expected ConsulPrefix 'test-prefix', got '%s'", cfg.ConsulPrefix)
	}
}
