package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/pergus/vaultsync"

	"github.com/chzyer/readline"
)

type redis struct {
	mu       sync.Mutex
	id       string
	user     string
	password string
}

func (r *redis) UpdateSecret(id string, fieldName string, value interface{}) {
	if r.id != id {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	switch fieldName {
	case "user":
		if name, ok := value.(string); ok {
			if r.user != name {
				r.user = name
			}
		}
	case "password":
		if pwd, ok := value.(string); ok {
			if r.password != pwd {
				r.password = pwd
			}
		}
	}
}

type netbox struct {
	mu       sync.Mutex
	id       string
	user     string
	password string
}

func (nb *netbox) UpdateSecret(id string, fieldName string, value interface{}) {
	if nb.id != id {
		return
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()

	switch fieldName {
	case "user":
		if name, ok := value.(string); ok {
			if nb.user != name {
				nb.user = name
			}
		}
	case "password":
		if pwd, ok := value.(string); ok {
			if nb.password != pwd {
				nb.password = pwd
			}
		}
	}
}

func main() {

	redis := &redis{id: "secrets/data/netpush/redis"}
	netbox := &netbox{id: "secrets/data/netpush/netbox"}

	logLevelVar := &slog.LevelVar{}
	loggerOpts := &slog.HandlerOptions{
		Level: logLevelVar,
	}
	logLevelVar.Set(slog.LevelInfo) // Set debug as default.

	// Open the log file for writing. Create it if it doesn't exist.
	file, err := os.OpenFile("example.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("Error opening log file:", err)
		return
	}
	defer file.Close()

	logger := slog.New(slog.NewJSONHandler(file, loggerOpts))
	vs, err := vaultsync.New(vaultsync.WithConfigFile("config.hcl"), vaultsync.WithLogLevel("info"), vaultsync.WithLogger(logger))
	if err != nil {
		logger.Error("main", slog.Any("error", err))
		return
	}

	logger.Info("main", slog.String("status", "starting"))

	vs.RegisterUpdateSecret(redis.id, redis)
	vs.RegisterUpdateSecret(netbox.id, netbox)
	ctx, cancel := context.WithCancel(context.Background())

	// Create a WaitGroup to wait for all workers to finish
	var wg sync.WaitGroup
	vs.Run(ctx, &wg)

	// After the call to Run() access to secrets has to be protected by mutex locks.

	redis.mu.Lock()
	netbox.mu.Lock()
	fmt.Printf("redis.user:%v\n", redis.user)
	fmt.Printf("redis.password:%v\n", redis.password)
	fmt.Printf("netbox.user:%v\n", netbox.user)
	fmt.Printf("nextbox.password:%v\n", netbox.password)
	fmt.Printf("\n")
	netbox.mu.Unlock()
	redis.mu.Unlock()

	rl, err := readline.New("> ")
	if err != nil {
		fmt.Println("Error creating readline instance:", err)
		os.Exit(1)
	}
	defer rl.Close()

	fmt.Println("Welcome to the REPL. Enter 'exit' to quit.")

	for {
		line, err := rl.Readline()
		if err != nil {
			fmt.Println("Error reading input:", err)
			break
		}
		if line == "redis" {
			redis.mu.Lock()
			fmt.Printf("redis.user:%v\n", redis.user)
			fmt.Printf("redis.password:%v\n", redis.password)
			redis.mu.Unlock()
			continue
		}
		if line == "netbox" {
			netbox.mu.Lock()
			fmt.Printf("netbox.user:%v\n", netbox.user)
			fmt.Printf("netbox.password:%v\n", netbox.password)
			netbox.mu.Unlock()
			continue
		}

		// Check if the user wants to exit
		if line == "exit" {
			cancel()
			fmt.Printf("waiting for go routines to terminate\n")
			logger.Info("main", slog.String("status", "waiting for go routines to terminate"))
			wg.Wait()
			logger.Info("main", slog.String("status", "all go routines has terminated"))
			fmt.Printf("go routines has terminated\n")

			break
		}

		// Evaluate and print the user input
		fmt.Println("You entered:", line)
	}

	fmt.Println("Goodbye!")
}
