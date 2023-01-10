
gofiles := $(shell find . -name '*.go' -type f -not -path "./vendor/*")

all: bin/butterfish

proto/butterfish.pb.go: proto/butterfish.proto
	cd proto && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative butterfish.proto

bin/butterfish: proto/butterfish.pb.go $(gofiles)
	mkdir -p bin
	go build -o ./bin/butterfish

clean:
	rm -f bin/* proto/*.go

.PHONY: all clean
