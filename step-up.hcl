job "node-config" {
  type = "batch"
  constraint {
    attribute = "${attr.kernel.name}"
    value = "linux"
  }

  constraint {
    attribute = "${attr.unique.hostname}"
    value = "turing2"
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
        args = ["-c", "${NOMAD_TASK_DIR}/step-up.sh"]
      }
      template {
        data = <<EOF
#!/bin/env bash
if ! command -v mise &> /dev/null; then
  apt update -y && sudo apt install -y curl
  install -dm 755 /etc/apt/keyrings
  curl -fSs https://mise.jdx.dev/gpg-key.pub | sudo tee /etc/apt/keyrings/mise-archive-keyring.pub 1> /dev/null
  echo "deb [signed-by=/etc/apt/keyrings/mise-archive-keyring.pub arch=arm64] https://mise.jdx.dev/deb stable main" | tee /etc/apt/sources.list.d/mise.list
  apt update -y
  apt install -y mise
fi
export MISE_PYTHON_DEFAULT_PACKAGES_FILE=${NOMAD_ALLOC_DIR}/mise/.default-python-packages
install -dm 755 /etc/apt/keyrings
cd ${NOMAD_ALLOC_DIR}/mise
pwd
mise trust
mise install
mise doctor
EOF
        destination = "${NOMAD_TASK_DIR}/step-up.sh"
        perms = 0755
      }

      template {
        data = <<EOF
[env]
_.python.venv = { path = ".venv", create = true }

[settings]
python.uv_venv_auto = true

[tools]
uv = "latest"
python = "latest"
EOF
        destination = "${NOMAD_ALLOC_DIR}/mise/mise.toml"
        perms = 0644
      }

      template {
        data = <<EOF
ansible
EOF
        destination = "${NOMAD_ALLOC_DIR}/mise/.default-python-packages"
        perms = 0755
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
