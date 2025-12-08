
# Job definition for the operator
variable "version" {
  type        = string
  default     = "v1.4.29"
  description = "Version of the application to deploy."
}

job "step-up" {
  type = "service"
  reschedule {
    delay          = "30s"
    delay_function = "constant"
    max_delay      = "120s"
    unlimited      = true
  }
  update {
    max_parallel      = 2
    min_healthy_time  = "10s"
    healthy_deadline  = "5m"
    progress_deadline = "10m"
    auto_revert       = true
    auto_promote      = true
    canary            = 1
    stagger           = "30s"
  }
  vault {
    env = true
  }
  group "operators" {
    restart {
      attempts = 3
      delay    = "30s"
    }
    network {
      port "metrics" {
        to = 8080
      }
    }

    service {
      name = "nomad-step-up-operator"
      port = "metrics"
      tags  = [
        "prometheus.io/scrape=true",
        "prometheus.io/port=${NOMAD_PORT_metrics}",
        "prometheus.io/path=/metrics"
      ]
      check {
        type     = "http"
        path     = "/metrics"
        interval = "30s"
        timeout  = "5s"
        check_restart {
          limit = 3
          grace = "60s"
          ignore_warnings = false
        }
        success_before_passing = 2
        failures_before_critical = 2
      }
    }

    task "operator" {
      resources {
        cores = 1
        memory = 1024
      }
      driver = "docker"
      identity {
        name        = "vault"
        aud         = ["vault.io"]
        env         = true
        file        = true
        change_mode = "restart"
        ttl         = "1h"
      }

      config {
        image = "ghcr.io/hashi-at-home/nomad-step-up-operator:${var.version}"
        ports = ["metrics"]  // expose the metrics port
      }

      artifact {
        source = "git::https://github.com/hashi-at-home/nomad-step-up-operator"
        destination = "local/repo"
      }

      template  {
        data = <<EOF
        NOMAD_TOKEN={{ with secret "nomad/creds/mgmt" }}{{ .Data.secret_id }}{{ end }}
        NOMAD_ADDR={{ with service "http.nomad" }}{{ with index . 0 }}http://{{ .Address }}:{{ .Port }}{{ end }}{{ end }}
        NOMAD_HTTP_ADDR={{ with service "http.nomad" }}{{ with index . 0 }}http://{{ .Address }}:{{ .Port }}{{ end }}{{ end }}
        NOMAD_JOB_FILE="/local/repo/step-up.hcl"
        EOF
        destination = "secrets/env"
        env         = true
      }
    }
  }
}
