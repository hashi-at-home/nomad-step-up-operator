job "node-config" {
  type = "batch"
  constraint {
    attribute = "${attr.kernel.name}"
    value = "linux"
  }

  group "ansible" {
    task "prepare" {
      resources {
        cpu = 50
        memory = 25
      }
      driver = "raw_exec"

      lifecycle {
        hook = "prestart"
        sidecar = false
      }
      config {
        command = "/bin/bash"
        args = ["-c", "sleep 5 && echo hello world"]
      }
    }

    task "configure" {
      resources {
        cpu = 100
        memory = 256
      }

      driver = "raw_exec"
      config {
        command = "/bin/bash"
        args = ["-c", "sleep 5 && echo doing real shit"]
      }
    }
  }
}
