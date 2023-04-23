package butterfish

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/bakks/butterfish/proto"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type commandExecution struct {
	Done     bool
	Output   []byte
	ExitCode int
}

type PluginClient struct {
	streamClient         proto.Ibodai_StreamClient
	CommandChan          chan string
	CommandExecutionChan chan *commandExecution
}

func (this *PluginClient) HandleCommandExecution(ctx context.Context, id string) {
	for {
		select {
		case <-ctx.Done():
			log.Printf("Plugin mux context done")
			return

		case ex := <-this.CommandExecutionChan:
			var newMsg *proto.ClientMessage

			if ex.Done {
				newMsg = &proto.ClientMessage{
					Payload: &proto.ClientMessage_CommandDone{
						CommandDone: &proto.CommandDone{
							CommandId: id,
							ExitCode:  0,
						},
					},
				}
			} else {
				newMsg = &proto.ClientMessage{
					Payload: &proto.ClientMessage_CommandOutput{
						CommandOutput: &proto.CommandOutput{
							CommandId:     id,
							ResponseChunk: ex.Output,
						},
					},
				}
			}

			err := this.streamClient.Send(newMsg)
			if err != nil {
				log.Fatalf("Error sending to plugin stream: %s", err)
				break
			}

			if ex.Done {
				return
			}
		}
	}
}

func (this *PluginClient) Mux(ctx context.Context) error {
	for {
		msg, err := this.streamClient.Recv()
		if err == io.EOF {
			log.Printf("Plugin stream closed")
			return err
		} else if err != nil {
			log.Printf("Error reading from plugin stream: %s", err)
			return err
		}

		log.Printf("Plugin message: %s", msg.Command)

		this.CommandChan <- msg.Command
		this.HandleCommandExecution(ctx, msg.Id)
	}
}

func (this *ButterfishCtx) PluginFrontend(pluginClient *PluginClient) {
	output := os.Stdout
	log.Printf("Starting plugin frontend")

	for {
		select {
		case <-this.Ctx.Done():
			log.Printf("Plugin frontend context done")
			return

		case cmd := <-pluginClient.CommandChan:
			log.Printf("Plugin command: %s", cmd)
			fmt.Fprintf(output, "> %s\n", cmd)

			result, err := executeCommand(this.Ctx, cmd, output)
			if err != nil {
				log.Printf("Error executing command: %s", err)
				continue
			}
			log.Printf("Command finished with exit code %d", result.Status)

			pluginClient.CommandExecutionChan <- &commandExecution{
				Done:   false,
				Output: result.LastOutput,
			}

			pluginClient.CommandExecutionChan <- &commandExecution{
				Done:     true,
				ExitCode: result.Status,
			}
		}
	}
}

func (this *ButterfishCtx) StartPluginClient(hostname string, port int) (*PluginClient, error) {
	// generate a random uuid token
	token := uuid.New().String()
	log.Printf("Starting plugin client with token: %s", token)
	this.Printf("Token: %s\n", token)

	// start the ibodai grpc plugin client
	var opts []grpc.DialOption
	// set 1s timeout for dialing
	opts = append(opts, grpc.WithBlock())
	opts = append(opts, grpc.WithTimeout(5*time.Second))

	if hostname != "" {
		opts = append(opts, grpc.WithAuthority(hostname))
	}

	if hostname == "localhost" || hostname == "0.0.0.0" {
		opts = append(opts, grpc.WithInsecure())
	} else {
		systemRoots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		cred := credentials.NewTLS(&tls.Config{
			RootCAs: systemRoots,
		})
		opts = append(opts, grpc.WithTransportCredentials(cred))
	}

	var conn grpc.ClientConnInterface
	var err error

	host := fmt.Sprintf("%s:%d", hostname, port)
	this.Printf("Connecting to server at %s...\n", host)
	log.Printf("Connecting to server at %s...", host)

	conn, err = grpc.DialContext(this.Ctx, host, opts...)
	if err != nil {
		this.Printf("fail to dial: %v\n", err)
		log.Fatalf("fail to dial: %v", err)
	}

	//defer conn.Close()
	client := proto.NewIbodaiClient(conn)

	var streamClient proto.Ibodai_StreamClient

	log.Printf("Opening bidirectional stream...")

	// loop to wait until server is alive
	for {
		streamClient, err = client.Stream(this.Ctx)
		if err != nil {
			//	st, _ := status.FromError(err)
			//	if st.Code() == codes.Unavailable {
			//		log.Printf("Failed to connect to server, waiting and retrying")
			//		time.Sleep(5 * time.Second)
			//		continue
			//	}

			// unknown error, bail out
			return nil, err
		}
		break // if successful we break out
	}

	helloMsg := proto.ClientMessage{
		Payload: &proto.ClientMessage_ClientHello{
			ClientHello: &proto.ClientHello{
				ClientToken: token,
			},
		},
	}

	err = streamClient.Send(&helloMsg)
	if err != nil {
		return nil, err
	}

	log.Printf("Hello message sent")
	this.Printf("Connected.\n")

	pluginClient := &PluginClient{
		streamClient:         streamClient,
		CommandChan:          make(chan string),
		CommandExecutionChan: make(chan *commandExecution),
	}
	return pluginClient, nil
}
