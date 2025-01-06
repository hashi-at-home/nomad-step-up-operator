/*
Nomad Event Stream handles the Nomad event stream.

This program reads events from the Nomad event stream and
*/
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/nomad/api"
)

func main() {

	if err := run(os.Args[:]); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// The run function set up new client with configuration

	// Address and SecretID are read from the environment variables
	client, err := api.NewClient(&api.Config{})
	// Handle any client errors, if they occur
	if err != nil {
		return err
	}

	// create a client to listen to the event stream
	eventsClient := client.EventStream()

	// Set up two event consumers - one for allocations and one for nodes
	// nodeConsumer :=
	// subscribe to specific events
	topics := map[api.Topic][]string{
		api.TopicAllocation: {"*"},
	}

	// nodeEvents := map[api.Topic][]string{api.TopicNode: {"*"}}

	// Begin stream
	ctx := context.Background()

	// allocCh is a channel which we can loop over, consuming events.
	// only the allocation events should be
	allocCh, err := eventsClient.Stream(ctx, topics, 0, &api.QueryOptions{})
	// nodeCh, err := eventsClient.Stream(ctx, nodeEvents, 0, &api.QueryOptions{})

	for {
		select {
		case <-ctx.Done():
			return nil

		case event := <-allocCh:
			if event.IsHeartbeat() {
				fmt.Print("Heartbeat Event")
			}
			fmt.Print(event)
		}

		if err != nil {
			fmt.Printf("Error: %s", err)
		}

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

		// Create an anonymous function to safely deal with shutdown signals.
		go func() {
			s := <-signals
			fmt.Printf("Received %s, stopping\n", s)

			os.Exit(0)
		}()
		client.Status()
	}
}
