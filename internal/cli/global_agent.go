package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/goblin/internal/ident"
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
		if err := client.CallJSON("SchedulerRPC.RegisterGlobalAgent", &spec, &resp); err != nil {
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

		var resp goblinv1.ListGlobalAgentsResponse
		if err := client.Call("SchedulerRPC.ListGlobalAgents", &goblinv1.ListGlobalAgentsRequest{}, &resp); err != nil {
			return err
		}

		fmt.Println("NAME\t\tUUID\t\t\t\t\tTYPE\t\tREPLICAS\tSTRATEGY")
		fmt.Println("----\t\t----\t\t\t\t\t----\t\t--------\t--------")
		for _, s := range resp.GetAgents() {
			fmt.Printf("%s\t%s\t%s\t%d\t\t%s\n", s.Name, ident.String(s.SpecUuid), s.Type, s.Replicas, s.Strategy)
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
		req := &goblinv1.GetGlobalAgentRequest{AgentId: id}
		var resp goblinv1.GetGlobalAgentResponse
		if err := client.Call("SchedulerRPC.GetGlobalAgent", req, &resp); err != nil {
			return err
		}

		// Print JSON or YAML
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(resp.GetSpec())
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
		if err := client.CallJSON("SchedulerRPC.DeleteGlobalAgent", id, &resp); err != nil {
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

		req := &goblinv1.ScaleAgentRequest{
			AgentId:  id,
			Replicas: int32(replicas),
		}
		var resp goblinv1.ScaleAgentResponse
		if err := client.Call("SchedulerRPC.ScaleAgent", req, &resp); err != nil {
			return err
		}
		fmt.Printf("agent %s scaled to %d replicas\n", id, resp.GetReplicas())
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

		req := &goblinv1.ListAgentInstancesRequest{}
		if len(args) == 1 {
			req.SpecId = args[0]
		}
		var resp goblinv1.ListAgentInstancesResponse
		if err := client.Call("SchedulerRPC.ListAgentInstances", req, &resp); err != nil {
			return err
		}

		fmt.Println("INSTANCE\t\t\t\t\tSPEC\t\t\t\t\tNODE\t\tSTATE")
		fmt.Println("--------\t\t\t\t\t----\t\t\t\t\t----\t\t-----")
		for _, inst := range resp.GetInstances() {
			fmt.Printf("%s\t%s\t%s\t%s\n", ident.String(inst.InstanceUuid), ident.String(inst.SpecUuid), inst.NodeId, stateLabel(inst.State))
		}
		return nil
	},
}

// stateLabel renders an InstanceState for table output: the enum name
// without its prefix, lowercased ("running", "admitted", ...).
func stateLabel(s goblinv1.InstanceState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "INSTANCE_STATE_"))
}

var globalAgentSignalCmd = &cobra.Command{
	Use:   "signal <instance-uuid> <signum>",
	Short: "Signal an instance (authorized and audited through Raft)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		signum := 0
		if _, err := fmt.Sscanf(args[1], "%d", &signum); err != nil {
			return fmt.Errorf("invalid signal number: %w", err)
		}

		client, err := NewQUICRPCClient(apiAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		req := supervisor.SignalAgentInstanceRequest{
			InstanceID: args[0],
			Signum:     int32(signum),
		}
		var resp string
		if err := client.CallJSON("SchedulerRPC.SignalAgentInstance", &req, &resp); err != nil {
			return err
		}
		fmt.Println(resp)
		return nil
	},
}

func init() {
	globalAgentCmd.AddCommand(globalAgentRegisterCmd)
	globalAgentCmd.AddCommand(globalAgentSignalCmd)
	globalAgentCmd.AddCommand(globalAgentInstancesCmd)
	globalAgentCmd.AddCommand(globalAgentListCmd)
	globalAgentCmd.AddCommand(globalAgentGetCmd)
	globalAgentCmd.AddCommand(globalAgentDeleteCmd)
	globalAgentCmd.AddCommand(globalAgentScaleCmd)

	clusterCmd.AddCommand(globalAgentCmd)
}
