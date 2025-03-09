# nomad-step-up-operator

A Nomad operator to help nodes step up.

## Overview

The Nomad Step-up Operator watches the Nomad event stream for node registration events and automatically deploys configuration jobs to new nodes. It's designed to:

- Monitor Nomad cluster for new node registrations
- Validate node eligibility and readiness
- Deploy Ansible-based configuration jobs to eligible nodes
- Track deployment status and health through Prometheus metrics

## Features

- 🔄 Automatic node configuration on registration
- 📊 Prometheus metrics exposure
- 🏥 Health checks for operator status
- 🔍 Event stream monitoring
- 🎯 Node-targeted job deployment

## Prerequisites

- Nomad cluster (1.5.x or later)
- Nomad ACL token with appropriate permissions
- Node capabilities:
  - `raw_exec` driver enabled

## Deployment

Deploy the operator using the provided Nomad job specification:

```hcl
nomad job run operator.nomad.hcl
```

Required environment variables:
- `NOMAD_ADDR`: Nomad API address
- `NOMAD_TOKEN`: ACL token with required permissions

## Configuration

The operator uses a predefined Ansible playbook for node configuration, specified in `step-up.hcl`. This job:

1. Prepares the node environment
2. Installs required dependencies
3. Executes Ansible playbooks for node configuration

## Metrics

The operator exposes Prometheus metrics at `/metrics`:

| Metric | Description |
|--------|-------------|
| `nomad_stepup_event_stream_up` | Event stream connection status |
| `nomad_stepup_nodes_managed_total` | Total nodes configured |
| `nomad_stepup_jobs_deployed_total` | Total configuration jobs deployed |

## Health Checks

Health status is available through the `/metrics` Prometheus metrics endpoint

## Development

Built with:
- Go 1.21+
- Cloud Native Buildpacks
- GitHub Actions for CI/CD

Build multi-architecture images:
```bash
go mod tidy
go build ./...
```

## License

MIT License - See LICENSE file for details
