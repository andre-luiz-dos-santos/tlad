BINARY := tlad

.PHONY: build test clean

build:
	CGO_ENABLED=0 go build -o $(BINARY) .

test:
	CGO_ENABLED=0 go test ./...

clean:
	rm -f $(BINARY)
