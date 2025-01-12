# Job definition for the operator
variable "version" {
  type        = string
  default     = "v1.0.2"
  description = "Version of the application to deploy."
}

job "step-up" {
  type = "service"
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
    task "operator" {
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
      }

      template  {
        data = <<EOF
        NOMAD_TOKEN={{ with secret "nomad/creds/mgmt" }}{{ .Data.secret_id }}{{ end }}
        NOMAD_ADDR={{ with service "http.nomad" }}{{ with index . 0 }}http://{{ .Address }}:{{ .Port }}{{ end }}{{ end }}
        EOF
        destination = "secrets/env"
        env         = true
      }
    }
  }
}
