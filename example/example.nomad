job "example" {
  group "example" {
    task "dotnet_test" {
      driver = "dotnet"

      config {
        sdk_path = "/usr/local/bin/dotnet"
        dll_path = "${NOMAD_TASK_DIR}/NancyService.dll"
        threading {
          min_threads = 10
          max_threads = 100
        }

      gc {
        enable = true
        concurrent = true
        heap_count = 16
        heap_limit = 209715200
        heap_limit_percent = 30
        no_affinity = true
      }

    globalization {}
        args = ["9090"]
      }

      artifact {
        source = "http://localhost:8000/test_nomad_task.zip"
        destination = "local"
      }

    }
  }
}
