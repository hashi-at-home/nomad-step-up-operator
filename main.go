package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/log/v2"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Metrics
	apiRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "step_up_api_requests_total",
			Help: "Total number of API requests",
		},
		[]string{"method", "endpoint", "status"},
	)

	stepStatusUpdates = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "step_up_status_updates_total",
			Help: "Total number of step status updates",
		},
		[]string{"node", "step", "status"},
	)

	activeLocks = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "step_up_active_locks",
			Help: "Number of active locks per node",
		},
		[]string{"node"},
	)

	eventStreamReconnects = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "step_up_event_stream_reconnects_total",
			Help: "Total number of event stream reconnections",
		},
	)
)

// contextKey is used as the key for context values in this package
type contextKey string

const (
	nomadClientCtxKey contextKey = "nomadClient"
)

// StepStatus represents the status of a configuration step
type StepStatus struct {
	Node      string    `json:"node"`
	Step      string    `json:"step"`
	Status    string    `json:"status"` // pending, running, done, failed
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	LockID    string    `json:"lock_id,omitempty"`
}

// NodeStatus represents the overall status of a node
type NodeStatus struct {
	Node                  string                `json:"node"`
	NodeID                string                `json:"node_id,omitempty"`
	Locked                bool                  `json:"locked"`
	LockingStep           string                `json:"locking_step,omitempty"`
	LockID                string                `json:"lock_id,omitempty"`
	Steps                 map[string]StepStatus `json:"steps"`
	LastUpdated           time.Time             `json:"last_updated"`
	NomadStatus           string                `json:"nomad_status,omitempty"`
	SchedulingEligibility string                `json:"scheduling_eligibility,omitempty"`
	Datacenter            string                `json:"datacenter,omitempty"`
}

// StepUpOperator manages the step-up process
type StepUpOperator struct {
	consul       *consulapi.Client
	nomad        *nomadapi.Client
	logger       *log.Logger
	locks        map[string]*consulapi.Lock
	locksMux     sync.RWMutex
	httpServer   *http.Server
	eventStream  *nomadapi.EventStream
	stopCh       chan struct{}
	wg           sync.WaitGroup
	consulPrefix string
}

// Config holds the operator configuration
type Config struct {
	ConsulAddr      string
	ConsulToken     string
	ConsulPrefix    string
	NomadAddr       string
	NomadToken      string
	HTTPAddr        string
	MetricsAddr     string
	LogLevel        string
	RefreshInterval time.Duration
}

// NewStepUpOperator creates a new operator instance
func NewStepUpOperator(cfg *Config, logger *log.Logger) (*StepUpOperator, error) {
	// Initialize Consul client
	consulConfig := consulapi.DefaultConfig()
	if cfg.ConsulAddr != "" {
		consulConfig.Address = cfg.ConsulAddr
	}
	if cfg.ConsulToken != "" {
		consulConfig.Token = cfg.ConsulToken
	}

	consulClient, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Consul client: %w", err)
	}

	// Initialize Nomad client
	nomadConfig := nomadapi.DefaultConfig()
	if cfg.NomadAddr != "" {
		nomadConfig.Address = cfg.NomadAddr
	}
	if cfg.NomadToken != "" {
		nomadConfig.SecretID = cfg.NomadToken // #pragma: allowlist secret
	}

	nomadClient, err := nomadapi.NewClient(nomadConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Nomad client: %w", err)
	}

	op := &StepUpOperator{
		consul:       consulClient,
		nomad:        nomadClient,
		logger:       logger,
		locks:        make(map[string]*consulapi.Lock),
		stopCh:       make(chan struct{}),
		consulPrefix: cfg.ConsulPrefix,
	}

	// Setup HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", op.healthHandler)
	mux.HandleFunc("/ready", op.readyHandler)
	mux.HandleFunc("/nodes", op.nodesHandler)
	mux.HandleFunc("/step-up", op.stepUpHandler)
	mux.HandleFunc("/step-up/", op.stepUpQueryHandler)
	mux.HandleFunc("/openapi", op.openAPIHandler)
	mux.HandleFunc("/openapi.json", op.openAPIHandler)
	mux.HandleFunc("/openapi.yaml", op.openAPIYAMLHandler)
	mux.HandleFunc("/doc", op.swaggerUIHandler)

	op.httpServer = &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: mux,
	}

	return op, nil
}

// Run starts the operator
func (op *StepUpOperator) Run(ctx context.Context, cfg *Config) error {
	op.logger.Info("starting step-up operator")

	// Initialize node inventory from Nomad
	if err := op.initializeNodeInventory(); err != nil {
		op.logger.Error("failed to initialize node inventory", "error", err)
		// Continue anyway - nodes will be added as they register
	}

	// Start HTTP server
	op.wg.Add(1)
	go func() {
		defer op.wg.Done()
		op.logger.Info("starting HTTP server", "addr", op.httpServer.Addr)
		if err := op.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			op.logger.Error("HTTP server error", "error", err)
		}
	}()

	// Start Nomad event stream watcher
	op.wg.Add(1)
	go func() {
		defer op.wg.Done()
		op.watchEventStream(ctx)
	}()

	// Start periodic node inventory refresh
	op.wg.Add(1)
	go func() {
		defer op.wg.Done()
		// Pass config through context for refresh interval
		ctxWithConfig := context.WithValue(ctx, nomadClientCtxKey, cfg)
		op.periodicNodeRefresh(ctxWithConfig)
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	op.logger.Info("shutting down operator")

	// Shutdown HTTP server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := op.httpServer.Shutdown(shutdownCtx); err != nil {
		op.logger.Error("failed to shutdown HTTP server", "error", err)
	}

	// Release all locks
	op.releaseAllLocks()

	// Wait for goroutines to finish
	close(op.stopCh)
	op.wg.Wait()

	return nil
}

// initializeNodeInventory queries Nomad for existing nodes and initializes their status
func (op *StepUpOperator) initializeNodeInventory() error {
	op.logger.Info("initializing node inventory from Nomad")

	// Query all nodes from Nomad
	nodes, _, err := op.nomad.Nodes().List(&nomadapi.QueryOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes from Nomad: %w", err)
	}

	op.logger.Info("found nodes in Nomad cluster", "count", len(nodes))

	// Initialize status for each node
	for _, nodeStub := range nodes {
		// Get full node details
		node, _, err := op.nomad.Nodes().Info(nodeStub.ID, &nomadapi.QueryOptions{})
		if err != nil {
			op.logger.Error("failed to get node info", "node_id", nodeStub.ID, "error", err)
			continue
		}

		// Check if node already exists in Consul
		existingStatus, err := op.getNodeStatus(node.Name)
		if err == nil && existingStatus != nil && len(existingStatus.Steps) > 0 {
			op.logger.Debug("node already tracked", "node", node.Name, "steps", len(existingStatus.Steps))
			continue
		}

		// Initialize new node status
		status := NodeStatus{
			Node:                  node.Name,
			NodeID:                node.ID,
			Locked:                false,
			Steps:                 make(map[string]StepStatus),
			LastUpdated:           time.Now(),
			NomadStatus:           node.Status,
			SchedulingEligibility: node.SchedulingEligibility,
			Datacenter:            node.Datacenter,
		}

		// Add node metadata as initial step
		if node.Status == "ready" {
			status.Steps["node_initialized"] = StepStatus{
				Node:      node.Name,
				Step:      "node_initialized",
				Status:    "done",
				Message:   fmt.Sprintf("Node initialized from Nomad inventory (status: %s)", node.Status),
				Timestamp: time.Now(),
			}
		} else {
			status.Steps["node_initialized"] = StepStatus{
				Node:      node.Name,
				Step:      "node_initialized",
				Status:    "pending",
				Message:   fmt.Sprintf("Node found in Nomad inventory (status: %s)", node.Status),
				Timestamp: time.Now(),
			}
		}

		if err := op.saveNodeStatus(&status); err != nil {
			op.logger.Error("failed to save initial node status", "node", node.Name, "error", err)
			continue
		}

		op.logger.Info("initialized node from inventory",
			"node", node.Name,
			"id", node.ID,
			"status", node.Status,
			"schedulable", node.SchedulingEligibility == "eligible")
	}

	return nil
}

// periodicNodeRefresh periodically refreshes the node inventory from Nomad
func (op *StepUpOperator) periodicNodeRefresh(ctx context.Context) {
	// Get refresh interval from config, default to 5 minutes
	refreshInterval := 5 * time.Minute
	if config, ok := ctx.Value("config").(*Config); ok && config.RefreshInterval > 0 {
		refreshInterval = config.RefreshInterval
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	op.logger.Info("starting periodic node refresh", "interval", refreshInterval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-op.stopCh:
			return
		case <-ticker.C:
			op.logger.Debug("refreshing node inventory")
			if err := op.refreshNodeInventory(); err != nil {
				op.logger.Error("failed to refresh node inventory", "error", err)
			}
		}
	}
}

// refreshNodeInventory queries Nomad for nodes and updates their status
func (op *StepUpOperator) refreshNodeInventory() error {
	// Query all nodes from Nomad
	nodes, _, err := op.nomad.Nodes().List(&nomadapi.QueryOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes from Nomad: %w", err)
	}

	op.logger.Debug("refreshing nodes from Nomad", "count", len(nodes))

	// Track which nodes we've seen
	seenNodes := make(map[string]bool)

	for _, nodeStub := range nodes {
		seenNodes[nodeStub.Name] = true

		// Check if node already exists in Consul
		existingStatus, err := op.getNodeStatus(nodeStub.Name)
		if err == nil && existingStatus != nil && len(existingStatus.Steps) > 0 {
			// Node already tracked, just update if status changed
			continue
		}

		// Get full node details for new nodes
		node, _, err := op.nomad.Nodes().Info(nodeStub.ID, &nomadapi.QueryOptions{})
		if err != nil {
			op.logger.Error("failed to get node info", "node_id", nodeStub.ID, "error", err)
			continue
		}

		// Initialize new node status
		status := NodeStatus{
			Node:                  node.Name,
			NodeID:                node.ID,
			Locked:                false,
			Steps:                 make(map[string]StepStatus),
			LastUpdated:           time.Now(),
			NomadStatus:           node.Status,
			SchedulingEligibility: node.SchedulingEligibility,
			Datacenter:            node.Datacenter,
		}

		// Add discovery step
		status.Steps["discovered"] = StepStatus{
			Node:      node.Name,
			Step:      "discovered",
			Status:    "done",
			Message:   fmt.Sprintf("Node discovered via refresh (status: %s, eligible: %v)", node.Status, node.SchedulingEligibility == "eligible"),
			Timestamp: time.Now(),
		}

		if err := op.saveNodeStatus(&status); err != nil {
			op.logger.Error("failed to save node status", "node", node.Name, "error", err)
		} else {
			op.logger.Info("added new node from refresh", "node", node.Name)
		}
	}

	// Optional: Mark nodes that are no longer in Nomad
	allNodes, err := op.getAllNodes()
	if err == nil {
		for _, nodeStatus := range allNodes {
			if !seenNodes[nodeStatus.Node] {
				op.logger.Warn("node no longer in Nomad cluster", "node", nodeStatus.Node)
				// Could add a "removed" step or delete the node
			}
		}
	}

	return nil
}

// watchEventStream watches Nomad event stream for node events
func (op *StepUpOperator) watchEventStream(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-op.stopCh:
			return
		default:
			op.logger.Debug("connecting to Nomad event stream")

			topics := map[nomadapi.Topic][]string{
				nomadapi.TopicNode: {"*"},
			}

			eventCh, err := op.nomad.EventStream().Stream(ctx, topics, 0, &nomadapi.QueryOptions{})

			if err != nil {
				op.logger.Error("failed to start event stream", "error", err)
				eventStreamReconnects.Inc()
				time.Sleep(5 * time.Second)
				continue
			}

			for {
				select {
				case <-ctx.Done():
					return
				case <-op.stopCh:
					return
				case event, ok := <-eventCh:
					if !ok {
						op.logger.Warn("event stream closed, reconnecting")
						eventStreamReconnects.Inc()
						break
					}
					if event.Err != nil {
						op.logger.Error("event stream error", "error", event.Err)
						eventStreamReconnects.Inc()
						break
					}
					for _, e := range event.Events {
						if e.Type == "NodeRegistration" {
							op.handleNodeRegistration(&e)
						}
					}
				}
			}
		}
	}
}

// handleNodeRegistration handles new node registration events
func (op *StepUpOperator) handleNodeRegistration(event *nomadapi.Event) {
	// Extract node from the event payload
	if event.Payload == nil {
		return
	}

	// The payload for Node events contains the node information
	nodePayload, ok := event.Payload["Node"]
	if !ok {
		return
	}

	// Convert the payload to a Node structure
	nodeData, ok := nodePayload.(map[string]interface{})
	if !ok {
		return
	}

	nodeName, _ := nodeData["Name"].(string)
	nodeID, _ := nodeData["ID"].(string)

	if nodeName == "" {
		return
	}

	op.logger.Info("new node registered", "node", nodeName, "id", nodeID)

	// Initialize node status in Consul
	status := NodeStatus{
		Node:        nodeName,
		NodeID:      nodeID,
		Locked:      false,
		Steps:       make(map[string]StepStatus),
		LastUpdated: time.Now(),
	}

	if err := op.saveNodeStatus(&status); err != nil {
		op.logger.Error("failed to initialize node status", "node", nodeName, "error", err)
	}

	// Trigger initial configuration job if configured
	if jobSpec := os.Getenv("STEP_UP_JOB_SPEC"); jobSpec != "" {
		op.logger.Info("dispatching step-up job", "node", nodeName)
		// Job dispatch would happen here
	}
}

// HTTP Handlers

func (op *StepUpOperator) openAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	openAPISpec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       "Nomad Step-Up Operator API",
			"description": "API for managing node configuration steps with Consul KV and locking",
			"version":     "1.0.0",
		},
		"servers": []map[string]interface{}{
			{
				"url":         "http://localhost:8080",
				"description": "Default server",
			},
		},
		"paths": map[string]interface{}{
			"/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Health check",
					"description": "Check if the operator is healthy",
					"operationId": "getHealth",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Service is healthy",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"status": map[string]interface{}{
												"type":    "string",
												"example": "healthy",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"/ready": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Readiness check",
					"description": "Check if the operator is ready to handle requests",
					"operationId": "getReady",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Service is ready",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"status": map[string]interface{}{
												"type":    "string",
												"example": "ready",
											},
										},
									},
								},
							},
						},
						"503": map[string]interface{}{
							"description": "Service is not ready",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"status": map[string]interface{}{
												"type":    "string",
												"example": "not ready",
											},
											"error": map[string]interface{}{
												"type": "string",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"/nodes": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get all nodes",
					"description": "Retrieve status of all nodes and their configuration steps",
					"operationId": "getAllNodes",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "List of all nodes with their status",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"type": "array",
										"items": map[string]interface{}{
											"$ref": "#/components/schemas/NodeStatus",
										},
									},
								},
							},
						},
						"500": map[string]interface{}{
							"description": "Internal server error",
						},
					},
				},
			},
			"/step-up": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Update step status",
					"description": "Update the status of a configuration step for a node",
					"operationId": "updateStepStatus",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/StepStatus",
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Step status updated successfully",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"$ref": "#/components/schemas/StepStatus",
									},
								},
							},
						},
						"400": map[string]interface{}{
							"description": "Bad request - missing required fields",
						},
						"409": map[string]interface{}{
							"description": "Conflict - unable to acquire lock",
						},
						"500": map[string]interface{}{
							"description": "Internal server error",
						},
					},
				},
			},
			"/step-up/{node}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get node status",
					"description": "Retrieve the status of a specific node including all steps",
					"operationId": "getNodeStatus",
					"parameters": []map[string]interface{}{
						{
							"name":        "node",
							"in":          "path",
							"required":    true,
							"description": "Node name",
							"schema": map[string]interface{}{
								"type": "string",
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Node status retrieved successfully",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"$ref": "#/components/schemas/NodeStatus",
									},
								},
							},
						},
						"404": map[string]interface{}{
							"description": "Node not found",
						},
					},
				},
			},
			"/step-up/{node}/{step}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get step status",
					"description": "Retrieve the status of a specific step on a node",
					"operationId": "getStepStatus",
					"parameters": []map[string]interface{}{
						{
							"name":        "node",
							"in":          "path",
							"required":    true,
							"description": "Node name",
							"schema": map[string]interface{}{
								"type": "string",
							},
						},
						{
							"name":        "step",
							"in":          "path",
							"required":    true,
							"description": "Step name",
							"schema": map[string]interface{}{
								"type": "string",
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Step status retrieved successfully",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"$ref": "#/components/schemas/StepStatus",
									},
								},
							},
						},
						"404": map[string]interface{}{
							"description": "Step not found",
						},
					},
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"StepStatus": map[string]interface{}{
					"type":     "object",
					"required": []string{"node", "step", "status"},
					"properties": map[string]interface{}{
						"node": map[string]interface{}{
							"type":        "string",
							"description": "Node name",
							"example":     "worker-01",
						},
						"step": map[string]interface{}{
							"type":        "string",
							"description": "Step name",
							"example":     "install_docker",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"description": "Step status",
							"enum":        []string{"pending", "running", "done", "failed"},
							"example":     "running",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Optional status message",
							"example":     "Installing Docker CE",
						},
						"timestamp": map[string]interface{}{
							"type":        "string",
							"format":      "date-time",
							"description": "Timestamp of the status update",
						},
						"lock_id": map[string]interface{}{
							"type":        "string",
							"description": "Lock ID if step holds a lock",
						},
					},
				},
				"NodeStatus": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"node": map[string]interface{}{
							"type":        "string",
							"description": "Node name",
							"example":     "worker-01",
						},
						"node_id": map[string]interface{}{
							"type":        "string",
							"description": "Nomad node UUID",
							"example":     "12345-abcde-67890",
						},
						"locked": map[string]interface{}{
							"type":        "boolean",
							"description": "Whether the node is currently locked",
							"example":     false,
						},
						"locking_step": map[string]interface{}{
							"type":        "string",
							"description": "Name of the step holding the lock",
							"example":     "install_docker",
						},
						"lock_id": map[string]interface{}{
							"type":        "string",
							"description": "Lock ID if node is locked",
						},
						"steps": map[string]interface{}{
							"type":        "object",
							"description": "Map of step names to their status",
							"additionalProperties": map[string]interface{}{
								"$ref": "#/components/schemas/StepStatus",
							},
						},
						"last_updated": map[string]interface{}{
							"type":        "string",
							"format":      "date-time",
							"description": "Last update timestamp",
						},
						"nomad_status": map[string]interface{}{
							"type":        "string",
							"description": "Nomad node status",
							"example":     "ready",
						},
						"scheduling_eligibility": map[string]interface{}{
							"type":        "string",
							"description": "Nomad scheduling eligibility",
							"example":     "eligible",
						},
						"datacenter": map[string]interface{}{
							"type":        "string",
							"description": "Datacenter the node belongs to",
							"example":     "dc1",
						},
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(openAPISpec)
}

func (op *StepUpOperator) openAPIYAMLHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// For YAML, we'll return a simple YAML representation
	// In production, you'd use a proper YAML library
	yamlSpec := `openapi: 3.0.0
info:
  title: Nomad Step-Up Operator API
  description: API for managing node configuration steps with Consul KV and locking
  version: 1.0.0
servers:
  - url: http://localhost:8080
    description: Default server
paths:
  /health:
    get:
      summary: Health check
      operationId: getHealth
      responses:
        '200':
          description: Service is healthy
  /ready:
    get:
      summary: Readiness check
      operationId: getReady
      responses:
        '200':
          description: Service is ready
        '503':
          description: Service is not ready
  /nodes:
    get:
      summary: Get all nodes
      operationId: getAllNodes
      responses:
        '200':
          description: List of all nodes
  /step-up:
    post:
      summary: Update step status
      operationId: updateStepStatus
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/StepStatus'
      responses:
        '200':
          description: Step updated
        '400':
          description: Bad request
        '409':
          description: Lock conflict
  /step-up/{node}:
    get:
      summary: Get node status
      operationId: getNodeStatus
      parameters:
        - name: node
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: Node status
        '404':
          description: Not found
  /step-up/{node}/{step}:
    get:
      summary: Get step status
      operationId: getStepStatus
      parameters:
        - name: node
          in: path
          required: true
          schema:
            type: string
        - name: step
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: Step status
        '404':
          description: Not found
`

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(yamlSpec))
}

func (op *StepUpOperator) swaggerUIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Serve Swagger UI HTML page
	swaggerHTML := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Nomad Step-Up Operator - API Documentation</title>
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.9.0/swagger-ui.css">
    <style>
        body {
            margin: 0;
            padding: 0;
        }
        #swagger-ui {
            background: #fafafa;
        }
        .topbar {
            display: none;
        }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.9.0/swagger-ui-bundle.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.9.0/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            const ui = SwaggerUIBundle({
                url: window.location.origin + "/openapi.json",
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout",
                validatorUrl: null,
                tryItOutEnabled: true,
                defaultModelsExpandDepth: 1,
                defaultModelExpandDepth: 1,
                docExpansion: "list",
                filter: true,
                showRequestHeaders: true,
                showCommonExtensions: true
            });
            window.ui = ui;
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(swaggerHTML))
}

func (op *StepUpOperator) nodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	nodes, err := op.getAllNodes()
	if err != nil {
		apiRequestsTotal.WithLabelValues("GET", "/nodes", "500").Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiRequestsTotal.WithLabelValues("GET", "/nodes", "200").Inc()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

func (op *StepUpOperator) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

func (op *StepUpOperator) readyHandler(w http.ResponseWriter, r *http.Request) {
	// Check Consul connectivity
	_, _, err := op.consul.KV().Get("health-check", nil)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "not ready", "error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

func (op *StepUpOperator) stepUpHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		op.handleStepUpdate(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (op *StepUpOperator) handleStepUpdate(w http.ResponseWriter, r *http.Request) {
	var status StepStatus
	if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
		apiRequestsTotal.WithLabelValues("POST", "/step-up", "400").Inc()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status.Timestamp = time.Now()

	// Validate required fields
	if status.Node == "" || status.Step == "" || status.Status == "" {
		apiRequestsTotal.WithLabelValues("POST", "/step-up", "400").Inc()
		http.Error(w, "node, step, and status are required", http.StatusBadRequest)
		return
	}

	op.logger.Info("updating step status",
		"node", status.Node,
		"step", status.Step,
		"status", status.Status)

	// Handle locking based on status
	if status.Status == "running" {
		lockID, err := op.acquireStepLock(status.Node, status.Step)
		if err != nil {
			apiRequestsTotal.WithLabelValues("POST", "/step-up", "409").Inc()
			http.Error(w, fmt.Sprintf("failed to acquire lock: %v", err), http.StatusConflict)
			return
		}
		status.LockID = lockID
	} else if status.Status == "done" || status.Status == "failed" {
		if err := op.releaseStepLock(status.Node, status.Step); err != nil {
			op.logger.Error("failed to release lock", "node", status.Node, "step", status.Step, "error", err)
		}
	}

	// Save step status to Consul
	if err := op.saveStepStatus(&status); err != nil {
		apiRequestsTotal.WithLabelValues("POST", "/step-up", "500").Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	stepStatusUpdates.WithLabelValues(status.Node, status.Step, status.Status).Inc()
	apiRequestsTotal.WithLabelValues("POST", "/step-up", "200").Inc()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (op *StepUpOperator) stepUpQueryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/step-up/")
	parts := strings.Split(path, "/")

	switch len(parts) {
	case 1:
		// GET /step-up/<node>
		op.handleNodeStatus(w, r, parts[0])
	case 2:
		// GET /step-up/<node>/<step>
		op.handleStepStatus(w, r, parts[0], parts[1])
	default:
		apiRequestsTotal.WithLabelValues("GET", r.URL.Path, "404").Inc()
		http.Error(w, "invalid path", http.StatusNotFound)
	}
}

func (op *StepUpOperator) handleNodeStatus(w http.ResponseWriter, r *http.Request, node string) {
	status, err := op.getNodeStatus(node)
	if err != nil {
		apiRequestsTotal.WithLabelValues("GET", r.URL.Path, "404").Inc()
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	apiRequestsTotal.WithLabelValues("GET", r.URL.Path, "200").Inc()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (op *StepUpOperator) handleStepStatus(w http.ResponseWriter, r *http.Request, node, step string) {
	status, err := op.getStepStatus(node, step)
	if err != nil {
		apiRequestsTotal.WithLabelValues("GET", r.URL.Path, "404").Inc()
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	apiRequestsTotal.WithLabelValues("GET", r.URL.Path, "200").Inc()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// Consul KV operations

func (op *StepUpOperator) saveStepStatus(status *StepStatus) error {
	key := fmt.Sprintf("%s/nodes/%s/steps/%s", op.consulPrefix, status.Node, status.Step)
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}

	_, err = op.consul.KV().Put(&consulapi.KVPair{
		Key:   key,
		Value: data,
	}, nil)

	// Also update node status
	if err == nil {
		op.updateNodeStatus(status.Node)
	}

	return err
}

func (op *StepUpOperator) getStepStatus(node, step string) (*StepStatus, error) {
	key := fmt.Sprintf("%s/nodes/%s/steps/%s", op.consulPrefix, node, step)
	pair, _, err := op.consul.KV().Get(key, nil)
	if err != nil {
		return nil, err
	}
	if pair == nil {
		return nil, fmt.Errorf("step not found")
	}

	var status StepStatus
	if err := json.Unmarshal(pair.Value, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

func (op *StepUpOperator) saveNodeStatus(status *NodeStatus) error {
	key := fmt.Sprintf("%s/nodes/%s/status", op.consulPrefix, status.Node)
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}

	_, err = op.consul.KV().Put(&consulapi.KVPair{
		Key:   key,
		Value: data,
	}, nil)
	return err
}

func (op *StepUpOperator) getNodeStatus(node string) (*NodeStatus, error) {
	// Get node status
	key := fmt.Sprintf("%s/nodes/%s/status", op.consulPrefix, node)
	pair, _, err := op.consul.KV().Get(key, nil)
	if err != nil {
		return nil, err
	}

	var status NodeStatus
	if pair != nil {
		if err := json.Unmarshal(pair.Value, &status); err != nil {
			return nil, err
		}
	} else {
		status = NodeStatus{
			Node:        node,
			Steps:       make(map[string]StepStatus),
			LastUpdated: time.Now(),
		}
	}

	// Get all steps for this node
	stepsPrefix := fmt.Sprintf("%s/nodes/%s/steps/", op.consulPrefix, node)
	pairs, _, err := op.consul.KV().List(stepsPrefix, nil)
	if err != nil {
		return nil, err
	}

	for _, pair := range pairs {
		var stepStatus StepStatus
		if err := json.Unmarshal(pair.Value, &stepStatus); err != nil {
			op.logger.Warn("failed to unmarshal step status", "key", pair.Key, "error", err)
			continue
		}
		status.Steps[stepStatus.Step] = stepStatus
	}

	// Check lock status
	op.locksMux.RLock()
	if lock, ok := op.locks[node]; ok && lock != nil {
		status.Locked = true
		// Find which step holds the lock
		for _, step := range status.Steps {
			if step.Status == "running" {
				status.LockingStep = step.Step
				status.LockID = step.LockID
				break
			}
		}
	}
	op.locksMux.RUnlock()

	return &status, nil
}

// getAllNodes retrieves all nodes and their statuses
func (op *StepUpOperator) getAllNodes() ([]NodeStatus, error) {
	// List all node status keys
	nodePrefix := fmt.Sprintf("%s/nodes/", op.consulPrefix)
	pairs, _, err := op.consul.KV().List(nodePrefix, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	// Track unique nodes
	nodeMap := make(map[string]*NodeStatus)

	// Get current Nomad node list for enrichment
	nomadNodes := make(map[string]*nomadapi.NodeListStub)
	if nodeList, _, err := op.nomad.Nodes().List(&nomadapi.QueryOptions{}); err == nil {
		for _, n := range nodeList {
			nomadNodes[n.Name] = n
		}
	}

	for _, pair := range pairs {
		// Parse the key to extract node name
		keyParts := strings.Split(pair.Key, "/")
		if len(keyParts) < 3 {
			continue
		}

		nodeName := keyParts[2]

		// Skip if we've already processed this node's full status
		if _, exists := nodeMap[nodeName]; exists && strings.HasSuffix(pair.Key, "/status") {
			var status NodeStatus
			if err := json.Unmarshal(pair.Value, &status); err != nil {
				op.logger.Warn("failed to unmarshal node status", "key", pair.Key, "error", err)
				continue
			}
			nodeMap[nodeName] = &status
		} else if !exists {
			// Initialize node if we haven't seen it yet
			nodeMap[nodeName] = &NodeStatus{
				Node:        nodeName,
				Steps:       make(map[string]StepStatus),
				LastUpdated: time.Now(),
			}
		}

		// Handle step data
		if strings.Contains(pair.Key, "/steps/") && len(keyParts) >= 5 {
			var stepStatus StepStatus
			if err := json.Unmarshal(pair.Value, &stepStatus); err != nil {
				op.logger.Warn("failed to unmarshal step status", "key", pair.Key, "error", err)
				continue
			}
			if nodeMap[nodeName].Steps == nil {
				nodeMap[nodeName].Steps = make(map[string]StepStatus)
			}
			nodeMap[nodeName].Steps[stepStatus.Step] = stepStatus
		}
	}

	// Check lock status for each node
	op.locksMux.RLock()
	for nodeName, lock := range op.locks {
		if status, exists := nodeMap[nodeName]; exists && lock != nil {
			status.Locked = true
			// Find which step holds the lock
			for _, step := range status.Steps {
				if step.Status == "running" {
					status.LockingStep = step.Step
					status.LockID = step.LockID
					break
				}
			}
		}
	}
	op.locksMux.RUnlock()

	// Convert map to slice and enrich with Nomad data
	nodes := make([]NodeStatus, 0, len(nodeMap))
	for _, status := range nodeMap {
		// Enrich with current Nomad data if available
		if nomadNode, exists := nomadNodes[status.Node]; exists {
			status.NodeID = nomadNode.ID
			status.NomadStatus = nomadNode.Status
			status.SchedulingEligibility = nomadNode.SchedulingEligibility
			status.Datacenter = nomadNode.Datacenter
		}
		nodes = append(nodes, *status)
	}

	// Sort nodes by name for consistent output
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Node < nodes[j].Node
	})

	return nodes, nil
}

func (op *StepUpOperator) updateNodeStatus(node string) {
	status, err := op.getNodeStatus(node)
	if err != nil {
		op.logger.Error("failed to get node status", "node", node, "error", err)
		return
	}

	status.LastUpdated = time.Now()
	if err := op.saveNodeStatus(status); err != nil {
		op.logger.Error("failed to save node status", "node", node, "error", err)
	}
}

// Lock management

func (op *StepUpOperator) acquireStepLock(node, step string) (string, error) {
	op.locksMux.Lock()
	defer op.locksMux.Unlock()

	// Check if node already has a lock
	if existingLock, ok := op.locks[node]; ok && existingLock != nil {
		return "", fmt.Errorf("node %s already locked", node)
	}

	// Create lock
	lockOpts := &consulapi.LockOptions{
		Key:         fmt.Sprintf("%s/locks/%s", op.consulPrefix, node),
		Value:       []byte(fmt.Sprintf(`{"node":"%s","step":"%s","timestamp":"%s"}`, node, step, time.Now().Format(time.RFC3339))),
		SessionTTL:  "30s",
		SessionName: fmt.Sprintf("step-up-%s-%s", node, step),
	}

	lock, err := op.consul.LockOpts(lockOpts)
	if err != nil {
		return "", err
	}

	// Try to acquire lock
	lockCh, err := lock.Lock(nil)
	if err != nil {
		return "", err
	}

	if lockCh == nil {
		return "", fmt.Errorf("unable to acquire lock")
	}

	op.locks[node] = lock
	activeLocks.WithLabelValues(node).Set(1)

	op.logger.Info("acquired lock", "node", node, "step", step)
	// Get session ID from lock
	sessionID := ""
	if lockCh != nil {
		// The session ID is stored internally in the lock
		// We'll return a placeholder since we can't access it directly
		sessionID = fmt.Sprintf("lock-%s-%s", node, step)
	}
	return sessionID, nil
}

func (op *StepUpOperator) releaseStepLock(node, step string) error {
	op.locksMux.Lock()
	defer op.locksMux.Unlock()

	lock, ok := op.locks[node]
	if !ok || lock == nil {
		return nil // No lock to release
	}

	err := lock.Unlock()
	delete(op.locks, node)
	activeLocks.WithLabelValues(node).Set(0)

	op.logger.Info("released lock", "node", node, "step", step)
	return err
}

func (op *StepUpOperator) releaseAllLocks() {
	op.locksMux.Lock()
	defer op.locksMux.Unlock()

	for node, lock := range op.locks {
		if lock != nil {
			if err := lock.Unlock(); err != nil {
				op.logger.Error("failed to release lock", "node", node, "error", err)
			}
			activeLocks.WithLabelValues(node).Set(0)
		}
	}
	op.locks = make(map[string]*consulapi.Lock)
}

// setupLogger configures the charm logger
func setupLogger(level string) *log.Logger {
	logger := log.New(os.Stderr)

	switch level {
	case "debug":
		logger.SetLevel(log.DebugLevel)
	case "warn":
		logger.SetLevel(log.WarnLevel)
	case "error":
		logger.SetLevel(log.ErrorLevel)
	default:
		logger.SetLevel(log.InfoLevel)
	}

	logger.SetReportCaller(true)
	logger.SetReportTimestamp(true)

	return logger
}

func main() {
	// Configuration from environment
	config := &Config{
		ConsulAddr:      getEnv("CONSUL_HTTP_ADDR", "127.0.0.1:8500"),
		ConsulToken:     os.Getenv("CONSUL_HTTP_TOKEN"),
		ConsulPrefix:    getEnv("CONSUL_PREFIX", "nomad-step-up"),
		NomadAddr:       getEnv("NOMAD_ADDR", "http://127.0.0.1:4646"),
		NomadToken:      os.Getenv("NOMAD_TOKEN"),
		HTTPAddr:        getEnv("HTTP_ADDR", ":8080"),
		MetricsAddr:     getEnv("METRICS_ADDR", ":9090"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		RefreshInterval: getEnvDuration("REFRESH_INTERVAL", 5*time.Minute),
	}

	logger := setupLogger(config.LogLevel)

	logger.Info("starting nomad-step-up-operator",
		"consul_addr", config.ConsulAddr,
		"nomad_addr", config.NomadAddr,
		"http_addr", config.HTTPAddr,
		"metrics_addr", config.MetricsAddr,
	)

	// Setup metrics server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		logger.Info("starting metrics server", "addr", config.MetricsAddr)
		if err := http.ListenAndServe(config.MetricsAddr, nil); err != nil {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	// Create operator
	operator, err := NewStepUpOperator(config, logger)
	if err != nil {
		logger.Fatal("failed to create operator", "error", err)
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("received signal", "signal", sig)
		cancel()
	}()

	// Run operator
	if err := operator.Run(ctx, config); err != nil {
		logger.Error("operator error", "error", err)
		os.Exit(1)
	}

	logger.Info("operator shutdown complete")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvDuration returns environment variable as duration or default
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}
