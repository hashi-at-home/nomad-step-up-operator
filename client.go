package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StepUpClient provides a client for interacting with the step-up operator API
type StepUpClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewStepUpClient creates a new step-up API client
func NewStepUpClient(baseURL string) *StepUpClient {
	return &StepUpClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// UpdateStepStatus sends a step status update to the operator
func (c *StepUpClient) UpdateStepStatus(node, step, status, message string) (*StepStatus, error) {
	update := StepStatus{
		Node:      node,
		Step:      step,
		Status:    status,
		Message:   message,
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(update)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update: %w", err)
	}

	resp, err := c.httpClient.Post(
		fmt.Sprintf("%s/step-up", c.baseURL),
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var result StepStatus
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetNodeStatus retrieves the status of a node including all steps
func (c *StepUpClient) GetNodeStatus(node string) (*NodeStatus, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/step-up/%s", c.baseURL, node))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var status NodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &status, nil
}

// GetStepStatus retrieves the status of a specific step on a node
func (c *StepUpClient) GetStepStatus(node, step string) (*StepStatus, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/step-up/%s/%s", c.baseURL, node, step))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var status StepStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &status, nil
}

// GetAllNodes retrieves the status of all nodes
func (c *StepUpClient) GetAllNodes() ([]NodeStatus, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/nodes", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var nodes []NodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return nodes, nil
}

// Health checks the health status of the operator
func (c *StepUpClient) Health() error {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/health", c.baseURL))
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("service unhealthy: status %d", resp.StatusCode)
	}

	return nil
}

// Ready checks if the operator is ready to handle requests
func (c *StepUpClient) Ready() error {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/ready", c.baseURL))
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("service not ready: %s", string(body))
	}

	return nil
}

// StartStep marks a step as running and acquires a lock
func (c *StepUpClient) StartStep(node, step string) (*StepStatus, error) {
	return c.UpdateStepStatus(node, step, "running", fmt.Sprintf("Starting %s", step))
}

// CompleteStep marks a step as done and releases the lock
func (c *StepUpClient) CompleteStep(node, step, message string) (*StepStatus, error) {
	if message == "" {
		message = fmt.Sprintf("%s completed successfully", step)
	}
	return c.UpdateStepStatus(node, step, "done", message)
}

// FailStep marks a step as failed and releases the lock
func (c *StepUpClient) FailStep(node, step, errorMessage string) (*StepStatus, error) {
	return c.UpdateStepStatus(node, step, "failed", errorMessage)
}

// WaitForLockRelease waits until the node's lock is released or timeout occurs
func (c *StepUpClient) WaitForLockRelease(node string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			status, err := c.GetNodeStatus(node)
			if err != nil {
				return fmt.Errorf("failed to get node status: %w", err)
			}

			if !status.Locked {
				return nil
			}

			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for lock release on node %s", node)
			}
		}
	}
}
