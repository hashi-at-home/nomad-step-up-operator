package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/log"
	"github.com/hashicorp/nomad/api"
)

// NodeConsumer is a type which handles a Nomad API client.
type NodeConsumer struct {
	// A struct which handles the node events
	client *api.Client
	onNode func(eventType string, node *api.Node)
	stop   func()

	telemetry *Telemetry
}

// StepUp is a struct which defines a step-up job
type StepUp struct {
	client *api.Client
	jobHCL string // store the job declaration read from a file.
}

// NewStepUp is a function which returns a new stepup object
func NewStepUp(client *api.Client, jobFilePath string) (*StepUp, error) {
	// read job declaration from file
	jobBytes, err := os.ReadFile(jobFilePath)
	if err != nil {
		log.Errorf("Failed to read job file %v", err)
		return nil, err
	}

	return &StepUp{
		client: client,
		jobHCL: string(jobBytes),
	}, nil
}

/*
onNode is a function which takes a Node event and starts a Nomad job on it.
*/
func (s *StepUp) onNode(eventType string, node *api.Node) {
	log.Infof("Node %s\n", node.Name)
	meta := node.Meta
	drivers := node.Drivers
	log.Infof("Meta: %v\n", meta)
	log.Infof("Drivers: %v\n", drivers)
	for k, v := range meta {
		fmt.Printf("%s: %s\n", k, v)
	}
	// Check if docker is present
	dockerPresent, err := drivers["docker"]

	if !err {
		log.Warn("Error getting docker")
	}
	if !dockerPresent.Detected {
		log.Warn("Docker not detected")
	}

	if eventType != "NodeRegistration" {
		return
	}

	if node.SchedulingEligibility != "eligible" || node.Status != "ready" {
		log.Warn("Node %s is not eligible or ready\n", node.Name)
		return
	}

	// Check if Docker is available
	if driver, ok := node.Drivers["docker"]; !ok || !driver.Healthy {
		log.Warnf("Docker driver is not available or not healthy on node %s", node.Name)
		return
	}

	// schedule the job on the node
	if err := s.deployJobToNode(node.ID); err != nil {
		log.Errorf("Failed to deploy job to node %s: %v", node.ID, err)
		return
	}

	log.Infof("Successfully deployed job to node %s", node.ID)
}

func (s *StepUp) deployJobToNode(nodeID string) error {
	// Parse the HCL file
	job, err := s.client.Jobs().ParseHCL(s.jobHCL, true)
	if err != nil {
		log.Errorf("Failed to parse job HCL: %v", err)
		return err
	}

	constraint := &api.Constraint{
		LTarget: "${node.unique.id}",
		RTarget: nodeID,
		Operand: "=",
	}

	// Add the constraint to the job.
	for _, group := range job.TaskGroups {
		group.Constraints = append(group.Constraints, constraint)
	}

	// Register the job
	jobRegisterOpts := &api.RegisterOptions{
		PreserveCounts: true,
	}

	_, _, err = s.client.Jobs().RegisterOpts(job, jobRegisterOpts, nil)

	if err != nil {
		log.Errorf("Failed to register job: %v", err)
		return err
	}

	return nil
}

// NewNodeConsumer is a function which returns a new node consumer
// func NewNodeConsumer(client *api.Client, onNode func(eventType string, node *api.Node)) *NodeConsumer {
func NewNodeConsumer(client *api.Client, onNode func(eventType string, node *api.Node), telemetry *Telemetry) *NodeConsumer {
	return &NodeConsumer{
		client:    client,
		onNode:    onNode,
		telemetry: telemetry,
	}
}

// StopNodeConsumer is a function which stops the consumer
// It has a receiver type of NodeConsumer
func (nc *NodeConsumer) StopNodeConsumer() {
	if nc.stop != nil {
		nc.stop()
	}

}

// StartNodeConsumer is a function which receives a NodeConsumer type
// which starts the context in the background
func (nc *NodeConsumer) StartNodeConsumer() {
	ctx := context.Background()
	log.Info("NodeConsumer: started context in background")
	ctx, nc.stop = context.WithCancel(ctx)
	nc.consume(ctx)
}

// consume is a function which consumes node events.
// It has a NodeConsumer receiver and takes a context as argument
// and returns an error
func (nc *NodeConsumer) consume(ctx context.Context) error {
	log.Info("Consuming node events")
	// this is the index of the event
	var index uint64 = 0
	// Check if there are nodes
	nodes, meta, err := nc.client.Nodes().List(nil)
	if err != nil {
		log.Errorf("Error listing nodes: %v", err)
		return fmt.Errorf("error listing nodes: %w", err)
	}

	if len(nodes) == 0 {
		log.Warn("No nodes found in cluster")
		return fmt.Errorf("no nodes found in cluster")
	}
	log.Infof("Found %d nodes in cluster", len(nodes))

	// increment the index from the
	index = meta.LastIndex + 1

	// specify the topics we want to consume
	topics := map[api.Topic][]string{
		api.TopicNode: {"*"},
	}
	// Start the event consumer
	nodeEventsClient := nc.client.EventStream()

	// signal successful connection
	nc.telemetry.RecordEventStreamStatus(ctx, true)
	defer nc.telemetry.RecordEventStreamStatus(ctx, false)

	log.Info("Establishing event stream")
	// create the channel for the events
	nodeEventCh, err := nodeEventsClient.Stream(ctx, topics, index, &api.QueryOptions{})
	if err != nil {
		log.Errorf("Failed to create event stream: %v", err)
		return fmt.Errorf("Failed to create event stream: %w", err)
	}

	lastEventTime := time.Now()
	heartbeatCheck := time.NewTicker(30 * time.Second)

	defer heartbeatCheck.Stop()
	// start an infinite loop to consume the events
	for {
		// Decide what to do when specific events occur
		select {
		case <-ctx.Done():
			// when the context is closed
			// stop the program and send no error
			return nil
		case <-heartbeatCheck.C:
			if time.Since(lastEventTime) > 60*time.Second {
				log.Error("No events received in the last minute")
				return fmt.Errorf("No events received in the last minute")

			}
		case nodeEvent := <-nodeEventCh: // receive a node event

			lastEventTime = time.Now()
			if nodeEvent == nil {
				log.Error("Received a nil event")
				return fmt.Errorf("Received a nil event")
			}

			if nodeEvent.Err != nil {
				log.Error("Received an error %s", nodeEvent.Err)
				return fmt.Errorf("Received an error %s", nodeEvent.Err)
			}
			// Ignore heartbeats

			if nodeEvent.IsHeartbeat() {
				log.Debug("received heartbeat")
				continue
			}
			// else, handle the event
			nc.handleNodeEvent(nodeEvent)
		}
	}
}

// handleNodeEvent is a function which receives a NodeConsumer and takes an Events struct as parameter.
// Handling the event consists of looping over all of the events passed and doing something with them when relevant.
func (nc *NodeConsumer) handleNodeEvent(nodeEvents *api.Events) {
	fmt.Println("Handling events")
	if nodeEvents.Err != nil {
		fmt.Printf("Received an error %s\n", nodeEvents.Err)
		return
	}

	// loop over the events
	for _, e := range nodeEvents.Events {
		// Event types are documented here:
		// https://developer.hashicorp.com/nomad/api-docs/events#event-types
		// We only listen to the NodeRegistration and NodeDeregistration
		if e.Type == "NodeRegistration" {
			n, err := e.Node()
			if err != nil {
				fmt.Printf("Received Node registration error %s\n", err)
				return
			} // handle node event assignment errors
			if n == nil {
				return
			}
			nc.onNode(e.Type, n)
			// fmt.Printf("Node %s has been registered -- bootstrapping\n", n.Name)
		} else {
			// Event of type that is not interesting to us
			return
		}
	}
}

func validateNomadConnection(client *api.Client) error {
	// try to list nodes - if there are no nodes, then the api connection is not valid.
	nodes, _, err := client.Nodes().List(&api.QueryOptions{})

	if err != nil {
		log.Errorf("Failed to list nodes: %v", err)
		return fmt.Errorf("Failed to list nodes: %w", err)
	}

	if len(nodes) == 0 {
		log.Error("No nodes found - check if NOMAD_TOKEN has correct permissions or is set.")
		return fmt.Errorf("no nodes found - check if NOMAD_TOKEN has correct permissions or is set")
	}

	// see if we can access all topics
	topics := map[api.Topic][]string{
		api.TopicNode: {"*"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	eventsCh, err := client.EventStream().Stream(ctx, topics, 0, &api.QueryOptions{})

	if err != nil {
		log.Errorf("Failed to validate nomad event stream: %v", err)
		return fmt.Errorf("Failed to validate nomad nomad event stream: %w", err)
	}

	// wait for at least one event, even a heartbeat
	select {
	case event := <-eventsCh:
		if event.Err != nil {
			log.Errorf("Failed to validate nomad event stream: %v", err)
			return fmt.Errorf("Failed to validate nomad nomad event stream: %w", err)
		}

	case <-ctx.Done():
		log.Errorf("Timeout waiting for event stream connection")
		return fmt.Errorf("Timeout waiting for event stream connection")
	}

	return nil
}
