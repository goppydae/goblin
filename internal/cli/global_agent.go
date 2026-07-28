package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// globalAgentCmd represents the parent command for global agent operations
var globalAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Global agent specification management (specs, scheduling)",
}

var globalAgentRegisterCmd = &cobra.Command{
	Use:   "register <spec-file>",
	Short: "Register or update a global agent specification",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		// Read spec
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// Decode YAML/JSON -> map -> JSON -> Proto
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("failed to parse YAML: %w", err)
		}
		jsonBytes, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var spec goblinv1.AgentSpec
		if err := json.Unmarshal(jsonBytes, &spec); err != nil {
			return fmt.Errorf("failed to parse agent spec: %w", err)
		}

		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		var resp string
		if err := client.Call("SchedulerRPC.RegisterGlobalAgent", &spec, &resp); err != nil {
			return err
		}
		fmt.Println(resp)
		return nil
	},
}

var globalAgentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all global agent specifications",
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		var specs []*goblinv1.AgentSpec
		if err := client.Call("SchedulerRPC.ListGlobalAgents", struct{}{}, &specs); err != nil {
			return err
		}

		fmt.Println("ID\t\tTYPE\t\tREPLICAS\tSTRATEGY")
		fmt.Println("--\t\t----\t\t--------\t--------")
		for _, s := range specs {
			fmt.Printf("%s\t%s\t%d\t\t%s\n", s.Id, s.Type, s.Replicas, s.Strategy)
		}
		return nil
	},
}

var globalAgentGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get details of a global agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		id := args[0]
		var spec goblinv1.AgentSpec
		if err := client.Call("SchedulerRPC.GetGlobalAgent", id, &spec); err != nil {
			return err
		}

		// Print JSON or YAML
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(&spec)
	},
}

var globalAgentDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a global agent specification",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		id := args[0]
		var resp string
		if err := client.Call("SchedulerRPC.DeleteGlobalAgent", id, &resp); err != nil {
			return err
		}
		fmt.Println(resp)
		return nil
	},
}

var globalAgentScaleCmd = &cobra.Command{
	Use:   "scale <id> <replicas>",
	Short: "Scale a global agent",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		id := args[0]
		replicas := 0
		if _, err := fmt.Sscanf(args[1], "%d", &replicas); err != nil {
			return fmt.Errorf("invalid replicas: %w", err)
		}

		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		req := supervisor.ScaleAgentRequest{
			AgentID:  id,
			Replicas: int32(replicas),
		}
		var resp string
		if err := client.Call("SchedulerRPC.ScaleAgent", &req, &resp); err != nil {
			return err
		}
		fmt.Println(resp)
		return nil
	},
}

var globalAgentInstancesCmd = &cobra.Command{
	Use:   "instances [spec-id]",
	Short: "List scheduled instances (all specs when no id is given)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		req := supervisor.ListAgentInstancesRequest{}
		if len(args) == 1 {
			req.SpecID = args[0]
		}
		var instances []*goblinv1.AgentInstance
		if err := client.Call("SchedulerRPC.ListAgentInstances", &req, &instances); err != nil {
			return err
		}

		fmt.Println("INSTANCE\t\t\tSPEC\t\tNODE\t\tSTATE")
		fmt.Println("--------\t\t\t----\t\t----\t\t-----")
		for _, inst := range instances {
			fmt.Printf("%s\t%s\t%s\t%s\n", inst.InstanceId, inst.SpecId, inst.NodeId, inst.State)
		}
		return nil
	},
}

func init() {
	globalAgentCmd.AddCommand(globalAgentRegisterCmd)
	globalAgentCmd.AddCommand(globalAgentInstancesCmd)
	globalAgentCmd.AddCommand(globalAgentListCmd)
	globalAgentCmd.AddCommand(globalAgentGetCmd)
	globalAgentCmd.AddCommand(globalAgentDeleteCmd)
	globalAgentCmd.AddCommand(globalAgentScaleCmd)

	clusterCmd.AddCommand(globalAgentCmd)
}
