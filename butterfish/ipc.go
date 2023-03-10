package butterfish

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/bakks/butterfish/proto"
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

func getHost() string {
	const hostname = "localhost"
	const port = 8099
	return fmt.Sprintf("%s:%d", hostname, port)
}

type IPCClient struct {
	client proto.Butterfish_StreamsForWrappingClient
}

func (this *IPCClient) Recv() (*proto.ServerPush, error) {
	return this.client.Recv()
}

func (this *IPCClient) SendOutput(output []byte) error {
	msg := &proto.ClientPush{
		Msg: &proto.ClientPush_ClientOutput{
			ClientOutput: &proto.ClientOutput{
				Data: output,
			},
		},
	}

	return this.client.Send(msg)
}

func (this *IPCClient) SendInput(input []byte) error {
	msg := &proto.ClientPush{
		Msg: &proto.ClientPush_ClientInput{
			ClientInput: &proto.ClientInput{
				Data: input,
			},
		},
	}

	return this.client.Send(msg)
}

func (this *IPCClient) SendWrappedCommand(cmd string) error {
	msg := &proto.ClientPush{
		Msg: &proto.ClientPush_ClientOpen{
			ClientOpen: &proto.ClientOpen{
				WrappedCommand: cmd,
			},
		},
	}

	return this.client.Send(msg)
}

func runIPCClient(ctx context.Context) (*IPCClient, error) {
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
	client := proto.NewButterfishClient(conn)

	var streamClient proto.Butterfish_StreamsForWrappingClient

	log.Printf("Opening bidirectional stream...")

	// loop to wait until server is alive
	for {
		streamClient, err = client.StreamsForWrapping(ctx)
		if err != nil {
			st, _ := status.FromError(err)
			if st.Code() == codes.Unavailable {
				log.Printf("Failed to connect to server, waiting and retrying")
				time.Sleep(5 * time.Second)
				continue
			}

			// unknown error, bail out
			return nil, err
		}
		break // if successful we break out
	}

	log.Printf("Connected.")

	wrappedClient := &IPCClient{streamClient}

	return wrappedClient, nil
}

func packageRPCStream(
	client *IPCClient,
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
	proto.UnimplementedButterfishServer
	clientOut     chan *ClientOut
	output        io.Writer
	clientMutex   sync.Mutex
	clientCounter int
	clients       map[int]proto.Butterfish_StreamsForWrappingServer
	clientOpenCmd map[int]string
	clientLastCmd map[int]string
}

type ClientOut struct {
	Client int
	Data   []byte
}

type ClientController interface {
	Write(client int, data string) error
	GetReader() <-chan *ClientOut
	GetClientWithOpenCmdLike(cmd string) int
	GetClientOpenCommand(client int) (string, error)
	GetClientLastCommand(client int) (string, error)
}

func (this *IPCServer) GetReader() <-chan *ClientOut {
	return this.clientOut
}

func RunIPCServer(ctx context.Context, output io.Writer) ClientController {
	lis, err := net.Listen("tcp", getHost())
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	grpcServer := grpc.NewServer(opts...)
	srv := &IPCServer{
		output:        output,
		clientOut:     make(chan *ClientOut),
		clients:       make(map[int]proto.Butterfish_StreamsForWrappingServer),
		clientOpenCmd: make(map[int]string),
		clientLastCmd: make(map[int]string),
	}
	proto.RegisterButterfishServer(grpcServer, srv)

	go func() {
		grpcServer.Serve(lis)
	}()

	go func() {
		<-ctx.Done()
		grpcServer.Stop()
	}()

	return srv
}

func (this *IPCServer) GetClientWithOpenCmdLike(cmd string) int {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	for client, openCmd := range this.clientOpenCmd {
		if strings.Contains(openCmd, cmd) {
			return client
		}
	}

	return -1
}

func (this *IPCServer) Write(client int, data string) error {
	this.clientMutex.Lock()
	srv := this.clients[client]
	this.clientMutex.Unlock()

	msg := &proto.ServerPush{
		Data: []byte(data),
	}
	return srv.Send(msg)
}

func (this *IPCServer) clientGetServer(client int) (proto.Butterfish_StreamsForWrappingServer, error) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	srv, ok := this.clients[client]

	if !ok {
		return nil, fmt.Errorf("Client %d server instance not found", client)
	}

	return srv, nil
}

func (this *IPCServer) clientNew(srv proto.Butterfish_StreamsForWrappingServer) int {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	clientId := this.clientCounter
	this.clients[clientId] = srv
	this.clientCounter++
	return clientId
}

func (this *IPCServer) clientSetOpenCommand(client int, cmd string) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	this.clientOpenCmd[client] = cmd
}

func (this *IPCServer) clientSetLastCommand(client int, cmd string) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	this.clientLastCmd[client] = cmd
}

func (this *IPCServer) GetClientLastCommand(client int) (string, error) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	last, ok := this.clientLastCmd[client]

	if !ok {
		return "", fmt.Errorf("Client %d last command not found", client)
	}

	return last, nil
}

func (this *IPCServer) GetClientOpenCommand(client int) (string, error) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()
	cmd, ok := this.clientOpenCmd[client]

	if !ok {
		return "", fmt.Errorf("Client %d open command not found", client)
	}

	return cmd, nil
}

func (this *IPCServer) clientDelete(client int) {
	this.clientMutex.Lock()
	defer this.clientMutex.Unlock()

	delete(this.clients, client)
	delete(this.clientOpenCmd, client)
	delete(this.clientLastCmd, client)
}

// Server-side StreamsForWrapping implementation, this receives data from the client
// and sends it to the clientOut channel
func (this *IPCServer) StreamsForWrapping(srv proto.Butterfish_StreamsForWrappingServer) error {
	// assign client number
	clientNum := this.clientNew(srv)

	batchingOut := newStreamBatcher(this.clientOut)
	fmt.Fprintf(this.output, "Client %d connected\n", clientNum)

	for {
		msgIn, err := srv.Recv()
		if err != nil {
			this.clientDelete(clientNum)
			return err
		}

		switch msg := msgIn.Msg.(type) {
		case *proto.ClientPush_ClientOpen:
			this.clientSetOpenCommand(clientNum, msg.ClientOpen.WrappedCommand)

		case *proto.ClientPush_ClientInput:
			this.clientSetLastCommand(clientNum, string(msg.ClientInput.Data))

		case *proto.ClientPush_ClientOutput:
			// data received from the client's stdout
			// package it in a ClientOut and send to the batchingOut channel
			msgOut := &ClientOut{
				Client: clientNum,
				Data:   msg.ClientOutput.Data,
			}
			batchingOut <- msgOut

		default:
			panic(fmt.Sprintf("Unknown message type: %T", msg))
		}

	}
}

const batchWaitTime = 400 * time.Millisecond

// Receive messages from msgIn, write the data inside to a bytes buffer and
// write down the time, then send the buffer to the clientOut channel only
// if we haven't received any new messages in the last 100ms
func streamBatcher(msgIn <-chan *ClientOut, msgOut chan<- *ClientOut) {
	var buf bytes.Buffer
	var lastWrite time.Time

	for {
		select {
		case msg := <-msgIn:
			buf.Write(msg.Data)
			lastWrite = time.Now()

		case <-time.After(batchWaitTime):
			if time.Since(lastWrite) > batchWaitTime && buf.Len() > 0 {
				msgOut <- &ClientOut{
					Data: buf.Bytes(),
				}
				buf.Reset()
			}
		}
	}
}

// create a new channel and run streamBatcher in a goroutine, return the
// channel we created
func newStreamBatcher(msgOut chan<- *ClientOut) chan *ClientOut {
	msgIn := make(chan *ClientOut)
	go streamBatcher(msgIn, msgOut)
	return msgIn
}

// We're multiplexing here between the stdin/stdout of this
// process, stdin/stdout of the wrapped process, and
// input/output of the connection to the butterfish server.
func wrappingMultiplexer(
	ctx context.Context,
	cancel context.CancelFunc,
	remoteClient *IPCClient,
	childIn io.Writer,
	parentIn, remoteIn, childOut <-chan *byteMsg) {

	buf := bytes.NewBuffer(nil)

	for {
		select {

		// Receive data from this process's stdin and write it to the child
		// process' stdin, add it to a local buffer, send to remote when
		// we hit a carriage return
		case s1 := <-parentIn:
			if bytes.Contains(s1.Data, []byte{'\r'}) {
				// the CR might be in the middle of a longer byte array, so we concat
				// with the existing butter and add any others to the cleared buffer
				concatted := append(buf.Bytes(), s1.Data...)
				split := bytes.Split(concatted, []byte{'\r'})

				if len(split) > 0 && len(split[0]) > 0 {
					buf.Reset()
					if len(split) > 1 {
						buf.Write(split[1])
					}
					// Send to remote
					remoteClient.SendInput(sanitizeTTYData(concatted))
				}
			} else {
				buf.Write(s1.Data)
			}

			_, err := childIn.Write(s1.Data)
			if err != nil {
				log.Printf("Error writing to child process: %s\n", err)
			}

		// Receive data from the child process stdout and write it
		// to this process's stdout and the remote server
		case s2 := <-childOut:
			if s2 == nil {
				return // child process is done, let's bail out
			}

			// Write to this process's stdout
			os.Stdout.Write(s2.Data)

			// Filter out characters and send to server
			printed := sanitizeTTYData(s2.Data)
			err := remoteClient.SendOutput(printed)
			if err != nil {
				if err == io.EOF {
					log.Printf("Remote server closed connection, exiting...\n")
					cancel()
					return
				} else {
					log.Fatalf("Error sending to remote server: %s\n", err)
				}
			}

		// Receive data from the remote server and write it to the
		// child process' stdin
		case s3 := <-remoteIn:
			if s3 == nil {
				cancel()
				return
			}

			_, err := fmt.Fprint(childIn, string(s3.Data))
			if err != nil {
				log.Printf("Error writing to child process: %s\n", err)
			}

		case <-ctx.Done():
			break
		}
	}
}

func (this *ButterfishCtx) serverMultiplexer() {
	clientInput := this.ClientController.GetReader()
	fmt.Fprintln(this.Out, "Butterfish server active...")

	for {
		select {
		case cmd := <-this.ConsoleCmdChan:
			err := this.Command(cmd)
			if err != nil {
				this.printError(err)
			}

		case clientData := <-clientInput:
			// Find the client's open/wrapping command, e.g. "zsh"
			client := clientData.Client
			wrappedCommand, err := this.ClientController.GetClientOpenCommand(client)
			if err != nil {
				this.printError(err, "on client input")
				continue
			}

			// If we think the client is wrapping a shell and the clientdata is
			// greater than 35 bytes (a pretty dumb predicate), we'll check
			// for unexpected / error output and try to be helpful
			if strings.Contains(wrappedCommand, "sh") && len(clientData.Data) > 35 {
				this.checkClientOutputForError(client, wrappedCommand, clientData.Data)
			}

		case <-this.Ctx.Done():
			return
		}
	}
}
