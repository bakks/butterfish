
gofiles := $(shell find . -name '*.go' -type f -not -path "./vendor/*")
buildtime := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
commit := $(shell bash ./bin/gitcommit.sh)
flags := -X main.BuildVersion=dev -X main.BuildCommit=${commit} -X main.BuildTimestamp=${buildtime}

all: build test

proto/butterfish.pb.go: proto/butterfish.proto
	cd proto && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative butterfish.proto

proto/ibodai.pb.go: proto/ibodai.proto
	cd proto && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ibodai.proto

bin/butterfish: proto/butterfish.pb.go proto/ibodai.pb.go $(gofiles) Makefile go.mod go.sum
	mkdir -p bin
	go build -ldflags "${flags}" -o ./bin/butterfish ./cmd/butterfish

clean:
	rm -f bin/butterfish proto/*.go

watch: Makefile
	find . -name "*.go" -o -name "*.proto" | entr -c make

test: proto/butterfish.pb.go
	go test ./...

build: bin/butterfish

licenses:
	go-licenses report ./... 2>/dev/null | awk -F"," '{printf "|[%s](https://%s)|[%s](%s)|\n",$$1,$$1,$$3,$$2}'


.PHONY: all clean watch test build licenses

