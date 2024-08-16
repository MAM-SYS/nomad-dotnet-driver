PLUGIN_BINARY=nomad-dotnet-driver
export GO111MODULE=on

default: build

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf ${PLUGIN_BINARY}

build:
	go build -o ${PLUGIN_BINARY} .

build-test:
	dotnet build ./test-resources/NancyService
	cd ./test-resources/NancyService/bin/Debug/net8.0/
	zip -r ../../../test_nomad_task.zip ./*
