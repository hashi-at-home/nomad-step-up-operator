/*
Nomad Event Stream handles the Nomad event stream.

This program reads events from the Nomad event stream and
*/
package main

import (
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
	client, err := api.NewClient(&api.Config{
		Address:  os.Getenv("NOMAD_HTTP_ADDR"),
		SecretID: os.Getenv("NOMAD_TOKEN"),
	})
	// Handle any client errors, if they occur
	if err != nil {
		return err
	}
	fmt.Println("Starting consumer")
	consumer := NewNodeConsumer(client)
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
