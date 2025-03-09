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

	"github.com/charmbracelet/log"
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
	// We need the following environment variables otherwise the operator will  not work
	requireEnvVars := []string{"NOMAD_HTTP_ADDR", "NOMAD_TOKEN", "NOMAD_JOB_FILE"}

	for _, env := range requireEnvVars {
		if os.Getenv(env) == "" {
			log.Fatalf("%s environment variable is not set", env)
		}
	}
	// Get the job file path from the environment.
	jobFilePath := os.Getenv("NOMAD_JOB_FILE")

	if jobFilePath == "" {
		log.Fatal("NOMAD_JOB_FILE environment variable is not set")
	}
	// Address and SecretID are read from the environment variables
	client, err := api.NewClient(&api.Config{
		Address:  os.Getenv("NOMAD_HTTP_ADDR"),
		SecretID: os.Getenv("NOMAD_TOKEN"),
	})
	log.Info("Client created")
	// Handle any client errors, if they occur
	if err != nil {
		log.Errorf("Failed to create stepup: %v", err)
		return err
	}

	log.Info("Validating Nomad connection")

	if err := validateNomadConnection(client); err != nil {
		log.Errorf("Failed to validate Nomad connection: %v", err)
		return fmt.Errorf("failed to validate Nomad connection %w", err)
	}

	log.Info("Nomad connection valid.")
	ctx := context.Background()
	telemetry, err := NewTelemetry(ctx)
	if err != nil {
		log.Errorf("Failed to create telemetry: %v", err)
		return fmt.Errorf("failed to create telemetry %w", err)
	}
	defer telemetry.Shutdown(ctx)
	log.Info("Starting consumer")
	stepup, err := NewStepUp(client, jobFilePath)
	consumer := NewNodeConsumer(client, stepup.onNode, telemetry)
	signals := make(chan os.Signal, 1) // Make a channel which we can send signals to. One signal at a time. Signal type is os.Signal.
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	// start an anonymous function to consume the eventstream
	go func() {
		// receive signals if any
		s := <-signals
		fmt.Printf("Received signal %s, stopping\n", s)
		consumer.StopNodeConsumer() // stop the consumer gracefully
		os.Exit(0)                  // exit the program with no errors
	}()
	consumer.StartNodeConsumer()
	return nil
}
