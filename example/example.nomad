job "example" {
  group "example" {
    task "dotnet_test" {
      driver = "dotnet"

      config {
        dll_path = "${NOMAD_TASK_DIR}/HelloWorldService.dll"
        runtime_version = "6.0.33"
        threading {
          min_threads = 10
          max_threads = 100
        }
        args = ["9090"]
      }

      artifact {
     source = "http://localhost:8000/Archive.zip"
     destination = "local"
      }
    }
  }
}