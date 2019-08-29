GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

.PHONY: init
init:
	$(GOGET) -u github.com/golangci/golangci-lint/cmd/golangci-lint

.PHONY: build
build:
	$(GOBUILD)

.PHONY: lint
lint:
	golangci-lint run --exclude-use-default=false ./...

.PHONY: test
test:
	$(GOTEST) -v ./...

.PHONY: clean
clean:
	$(GOCLEAN)
