BINARY := kubectl-sidecars

.PHONY: build clean test format

build:
	go build -o $(BINARY) .

clean:
	rm -f $(BINARY)
	go clean

test:
	go test -v -cover ./...

format:
	go fmt ./...
