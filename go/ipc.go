package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/bakks/butterfish/proto"
)

// The Butterfish console starts a local server which a 'wrap' client can
// connect to. A 'wrap' client starts a given child process, copies its
// stdout to the server, and allows the server to write to the process's stdin.

// The server and client both operate with a central multiplexer, responsible
// for selecting between incoming channels. stdin/stdout, gRPC messages, and
// server console commands are all written to channels for the multiplexers.

// Client stdin                                   Console Commands
// │ Child stdout                                 │ Done
// │ │                                            │ │
// ▼ ▼                                            ▼ ▼
//┌────────────────────┐                         ┌──────────────────────┐
//│                    │                         │                      │
//│ Client Multiplexer │ ◄────────────────────►  │ Server Multiplexer   │
//│                    │     bidirectional       │                      │
//└────────────────────┘     gRPC stream         └──────────────────────┘

type ClientController interface {
	Write(client int, data string) error
	GetReader() <-chan *ClientOut
}

func (this *IPCServer) GetReader() <-chan *ClientOut {
	return this.clientOut
}

type ClientOut struct {
	Client int
	Data   string
}

func (this *IPCServer) Write(client int, data string) error {
	this.mutex.Lock()
	srv := this.clients[client]
	this.mutex.Unlock()

	return srv.Send(&pb.StreamBlock{Data: []byte(data)})
}

func getHost() string {
	const hostname = "localhost"
	const port = 8099
	return fmt.Sprintf("%s:%d", hostname, port)
}

func runIPCClient(ctx context.Context) (pb.Butterfish_StreamBlocksClient, error) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	var conn grpc.ClientConnInterface
	var err error

	host := getHost()
	log.Printf("Connecting to server at %s...", host)
	conn, err = grpc.DialContext(ctx, host, opts...)
	if err != nil {
		log.Fatalf("fail to dial: %v", err)
	}

	//defer conn.Close()
	client := pb.NewButterfishClient(conn)

	var streamClient pb.Butterfish_StreamBlocksClient

	log.Printf("Opening bidirectional stream...")

	// loop to wait until server is alive
	for {
		streamClient, err = client.StreamBlocks(ctx)
		if err != nil {
			log.Printf("Failed to connect to server: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		break
	}

	log.Printf("Connected.")

	return streamClient, nil
}

func runIPCServer(ctx context.Context, output io.Writer) ClientController {
	lis, err := net.Listen("tcp", getHost())
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	grpcServer := grpc.NewServer(opts...)
	srv := NewIPCServer(output)
	pb.RegisterButterfishServer(grpcServer, srv)

	go func() {
		grpcServer.Serve(lis)
	}()

	go func() {
		<-ctx.Done()
		grpcServer.Stop()
	}()

	return srv
}

func packageRPCStream(
	client pb.Butterfish_StreamBlocksClient,
	c chan<- *byteMsg) {
	// Loop indefinitely
	for {
		// Read from stream
		streamBlock, err := client.Recv()

		// Check for error
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading from rpc stream: %s\n", err)
				close(c)
			}
			break
		}

		// Convert the bytes to a string and add it to the channel
		c <- NewByteMsg(streamBlock.Data)
	}
}

type IPCServer struct {
	pb.UnimplementedButterfishServer
	clientOut     chan *ClientOut
	mutex         sync.Mutex
	clientCounter int
	clients       map[int]pb.Butterfish_StreamBlocksServer
	output        io.Writer
}

func NewIPCServer(output io.Writer) *IPCServer {
	return &IPCServer{
		clients:   make(map[int]pb.Butterfish_StreamBlocksServer),
		clientOut: make(chan *ClientOut),
		output:    output,
	}
}

const batchWaitTime = 400 * time.Millisecond

// Receive messages from msgIn, write the data inside to a bytes buffer and
// write down the time, then send the buffer to the clientOut channel only
// if we haven't received any new messages in the last 100ms
func streamBatcher(msgIn chan *ClientOut, msgOut chan *ClientOut) {
	var buf bytes.Buffer
	var lastWrite time.Time

	for {
		select {
		case msg := <-msgIn:
			buf.WriteString(msg.Data)
			lastWrite = time.Now()

		case <-time.After(batchWaitTime):
			if time.Since(lastWrite) > batchWaitTime && buf.Len() > 0 {
				msgOut <- &ClientOut{
					Data: buf.String(),
				}
				buf.Reset()
			}
		}
	}
}

// Server-side StreamBlocks implementation, this receives data from the client
// and sends it to the clientOut channel
func (this *IPCServer) StreamBlocks(srv pb.Butterfish_StreamBlocksServer) error {

	// assign client number and this client/server pair
	this.mutex.Lock()
	clientNum := this.clientCounter
	this.clientCounter++
	this.clients[clientNum] = srv
	this.mutex.Unlock()

	batchingOut := newStreamBatcher(this.clientOut)
	fmt.Fprintf(this.output, "Client %d connected", clientNum)

	for {
		block, err := srv.Recv()
		if err != nil {
			return err
		}

		msg := &ClientOut{
			Client: clientNum,
			Data:   string(block.Data),
		}
		batchingOut <- msg
	}
}

// create a new channel and run streamBatcher in a goroutine, return the
// channel we created
func newStreamBatcher(msgOut chan *ClientOut) chan *ClientOut {
	msgIn := make(chan *ClientOut)
	go streamBatcher(msgIn, msgOut)
	return msgIn
}
