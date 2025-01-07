package main

import (
	"context"
	"fmt"

	"github.com/hashicorp/nomad/api"
)

type NodeConsumer struct {
	// A struct which handles the node events
	client *api.Client
	// onNode func(eventType string, node *api.Node)
	stop func()
}

// NewNodeConsumer is a function which returns a new node consumer
// func NewNodeConsumer(client *api.Client, onNode func(eventType string, node *api.Node)) *NodeConsumer {
func NewNodeConsumer(client *api.Client) *NodeConsumer {
	return &NodeConsumer{
		client: client,
		// onNode: onNode,
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
	fmt.Println("started context in background")
	ctx, nc.stop = context.WithCancel(ctx)
	nc.consume(ctx)
}

// consume is a function which consumes node events.
// It has a NodeConsumer receiver and takes a context as argument
// and returns an error
func (nc *NodeConsumer) consume(ctx context.Context) error {
	fmt.Println("Consuming node event")
	// this is the index of the event
	var index uint64 = 0
	// Check if there are nodes
	_, meta, err := nc.client.NodePools().List(nil)
	if err != nil {
		return err
	}

	// increment the index from the
	index = meta.LastIndex + 1

	// specif the topics we want to consume
	topics := map[api.Topic][]string{
		api.TopicNode: {"*"},
	}
	fmt.Println("Set Topics")
	fmt.Println(topics)
	// Start the event consumer
	nodeEventsClient := nc.client.EventStream()
	// create the channel for the events
	nodeEventCh, err := nodeEventsClient.Stream(ctx, topics, index, &api.QueryOptions{})
	if err != nil {
		return err
	}

	// start an infinite loop to consume the events
	for {
		// Decide what to do when specific events occur
		select {
		case <-ctx.Done():
			// when the context is closed
			// stop the program and send no error
			return nil
		case nodeEvent := <-nodeEventCh: // receive a node event
			// Ignore heartbeats
			if nodeEvent.IsHeartbeat() {
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
		// Event types are here:
		// https://developer.hashicorp.com/nomad/api-docs/events#event-types
		// We only listen to the NodeRegistration and NodeDeregistration
		if e.Type == "NodeRegistration" {
			n, err := e.Node()
			if err != nil {
				fmt.Print("Received Node registration error %s\n", err)
				return
			} // handle node event assignment errors
			if n == nil {
				return
			}
			fmt.Printf("Node %s has been registered -- bootstrapping\n", n.Name)

		} else {
			fmt.Printf("Event type %s, do nothing\n", e.Type)
			return
		}
	}
}
