package main

import "github.com/hashicorp/nomad/api"

type NodeConsumer struct {
	client *api.Client
	onNode func(eventType string, node *api.Node)
	stop   func()
}
