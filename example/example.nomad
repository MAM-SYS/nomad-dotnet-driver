job "example" {
  group "example" {
    task "dotnet_test" {
      driver = "dotnet"

      config {
        dll_path = "${NOMAD_TASK_DIR}/TestNomadTask.dll"
        threading {
          min_threads = 10
          max_threads = 100
        }
        args = ["9090"]
      }

      artifact {
        source = "https://github.com/MAM-SYS/nomad-dotnet-driver.git/test-resources/TestNomadTask/bin/Debug/net8.0/TestNomadTask.dll"
        destination = "local"
      }
    }
  }
}