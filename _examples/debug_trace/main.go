package main

import (
	"fmt"
	"github.com/quix-labs/flash/pkg/client"
	"github.com/quix-labs/flash/pkg/listeners"
	"github.com/quix-labs/flash/pkg/types"
	"github.com/rs/zerolog"
	"os"
)

func main() {
	postsListenerConfig := &types.ListenerConfig{Table: "public.posts"}
	postsListener, _ := listeners.NewListener(postsListenerConfig)

	// Registering your callbacks
	stop, err := postsListener.On(types.OperationInsert, func(event types.Event) {
		typedEvent := event.(*types.InsertEvent)
		fmt.Printf("Insert received - new: %+v\n", typedEvent.New)
	})
	if err != nil {
		fmt.Println(err)
	}
	defer stop()

	// Create custom logger with Level Trace <-> Default is Debug
	logger := zerolog.New(os.Stdout).Level(zerolog.TraceLevel).With().Stack().Timestamp().Logger()

	// Create client
	clientConfig := &types.ClientConfig{
		DatabaseCnx: "postgresql://devuser:devpass@localhost:5432/devdb",
		Logger:      &logger, // Define your custom zerolog.Logger here
	}
	flashClient, _ := client.NewClient(clientConfig)
	flashClient.Attach(postsListener)

	// Start listening
	go flashClient.Start() // Error Handling
	defer flashClient.Close()

	// Keep process running
	select {}
}
