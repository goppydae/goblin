// Sleeper is the cluster-e2e fixture agent: it describes itself for
// GAPI discovery and then sleeps until signaled, so scheduled instances
// are real processes whose lifetime the harness can observe and kill.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

var describe = flag.Bool("describe", false, "Print agent metadata")

func main() {
	flag.Parse()

	if *describe {
		metadata := map[string]interface{}{
			"describe": map[string]interface{}{
				"id":           "sleeper",
				"type":         "service",
				"version":      "1.0.0",
				"language":     "go",
				"description":  "cluster e2e fixture: sleeps until signaled",
				"capabilities": []string{"start", "stop"},
			},
		}
		if err := json.NewEncoder(os.Stdout).Encode(metadata); err != nil {
			fmt.Fprintln(os.Stderr, "encode describe metadata:", err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("[sleeper] started")
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("[sleeper] stopped")
}
