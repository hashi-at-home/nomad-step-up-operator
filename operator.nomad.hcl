# Job definition for the operator
variable "version" {
  type        = string
  default     = "v1.2.1"
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

      artifact {
        source = "git::https://github.com/hashi-at-home/nomad-step-up-operator//step-up.hcl"
        destination = "local/jobs/"
        mode = "file"
      }

      config {
        image = "ghcr.io/hashi-at-home/nomad-step-up-operator:${var.version}"
      }

      artifact {
        source = "git::https://github.com/hashi-at-home/nomad-step-up-operator//step-up.hcl"
        destination = "local/jobs/"
        mode = "file"
      }

      template  {
        data = <<EOF
        NOMAD_TOKEN={{ with secret "nomad/creds/mgmt" }}{{ .Data.secret_id }}{{ end }}
        NOMAD_ADDR={{ with service "http.nomad" }}{{ with index . 0 }}http://{{ .Address }}:{{ .Port }}{{ end }}{{ end }}
        NOMAD_JOB_FILE=/local/jobs/step-up.hcl"
        EOF
        destination = "secrets/env"
        env         = true
      }
    }
  }
}
