package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/gapi/core/tui"
	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/version"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// RootCmd is goblinctl's root, built by the kernel's shared constructor
// so the control roots of all four binaries carry one definition of each
// flag's name, shorthand, default and help text (cli-contract.md).
//
// It also supplies the identity surface: `version`, `--version` and `-v`
// all render core/version.Summary(), and the block names the binary
// rather than printing cobra's anonymous one-liner (GOBLIN-DIV-052).
// The leading "goblin" is the product this process belongs to, and the
// control binary must agree with its daemon about it: goblinctl resolves
// the same environment namespace and the same config search path that
// goblind writes (GOBLIN-DIV-055).
var RootCmd, controlFlags = gapicli.NewControlRoot(
	"goblin", "goblinctl", version.Version, "Goblin distributed supervisor control")

// defaultAPIAddr is goblind's default listen address, and it lives here
// because the shared --api-addr default is EMPTY.
//
// The contract gives that empty default the meaning "resolve from
// config", which keeps config the source of truth and the flag an
// override. gapictl can honour that; goblinctl cannot, because goblin
// has no config package at all - there is nothing to resolve from. So
// empty resolves to the literal below, which is exactly the default this
// flag carried before it moved to the shared registrar, and the flag's
// NAME, shorthand and default still come from one definition as the
// contract requires. Only the interpretation of empty differs, and it
// differs because one binary has a config loader and the other does not.
// Recorded as a residual on GOBLIN-DIV-052 rather than papered over: a
// config source for goblinctl is its own piece of work.
const defaultAPIAddr = "127.0.0.1:29000"

// controlAddr is the daemon to talk to: the --api-addr override when
// given, the local node otherwise.
func controlAddr() string {
	if controlFlags.APIAddr != "" {
		return controlFlags.APIAddr
	}
	return defaultAPIAddr
}

// controlTLS is how to reach it. On a CONTROL binary --tls-ca is the
// client's trust root, not server-side material; the name is shared with
// the daemon role and the meaning is not (cli-contract.md).
func controlTLS() transport.TLSConfig {
	return transport.TLSConfig{
		CAFile:             controlFlags.TLSCA,
		InsecureSkipVerify: controlFlags.TLSInsecure,
	}
}

// dialControl opens an RPC client to the configured target.
//
// This exists because the same three-line construction appeared THIRTEEN
// times verbatim. A shared flag registrar makes the definitions agree and
// says nothing about whether anything reads them - the GAPI-DIV-034 shape
// that GAPI-DIV-058's residual warns about - so the read is funnelled
// through one function that is obviously exercised by every verb.
func dialControl() (*QUICRPCClient, error) {
	return NewQUICRPCClient(controlAddr(), controlTLS())
}

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Cluster management operations",
}

// closeClient joins a client close failure into the command's returned
// error unless a primary error is already set.
func closeClient(client *QUICRPCClient, err *error) {
	if cerr := client.Close(); cerr != nil && *err == nil {
		*err = fmt.Errorf("close rpc client: %w", cerr)
	}
}

// ... helper code ...

// goblinctl carries NO daemon-launching verb. There was a `start` here
// that built supervisor.Config with four of its twenty-two fields and
// called supervisor.New - so it brought up a node with no TLS material,
// no gossip encryption, no operator keys and not in production mode,
// which by goblind's own --operator-key contract refuses every mutating
// verb, and it did so with exit 0 and no warning.
//
// It is deleted rather than repaired. Repairing it means maintaining two
// constructions of supervisor.Config kept identical by discipline, and
// four-of-twenty-two is what that discipline actually produced. A control
// binary never starts a daemon; there is one way to bring up a
// supervisor and it is goblind's own start verb (cli-contract.md,
// GOBLIN-DIV-054).

func init() {
	RootCmd.AddCommand(clusterCmd)
	RootCmd.AddCommand(tuiCmd)
	RootCmd.AddCommand(operatorCmd)

	clusterCmd.AddCommand(statusCmd)
	clusterCmd.AddCommand(publishCmd)
	clusterCmd.AddCommand(runCmd)
	clusterCmd.AddCommand(drainCmd)
	clusterCmd.AddCommand(migrateCmd)
	clusterCmd.AddCommand(migrateInstanceCmd)

	// The persistent set is registered by NewControlRoot above; nothing
	// is defined locally here, which is the point - a flag added to one
	// control binary and not its peer is what the shared registrar
	// exists to prevent.

	// Node-local agent verbs, supplied by the embedded kernel.
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Local agent management operations",
	}
	RootCmd.AddCommand(agentCmd)

	// Mount the kernel's agent verbs under `agent`.
	for _, cmd := range gapicli.GetRoot().Commands() {
		if cmd.Name() == "agent" {
			// Flatten GAPI's agent namespace into Goblin's agent namespace
			for _, sub := range cmd.Commands() {
				rebrandMounted(sub)
				agentCmd.AddCommand(sub)
			}
			continue
		}
		if cmd.Name() == "tui" {
			cmd.Short = "Local Agent TUI"
		}
		if cmd.Name() == "version" {
			// Deliberately NOT mounted. The old comment here said
			// "Avoid conflict", which described a conflict that did not
			// exist: gapictl's version command would have landed at
			// `goblinctl agent version`, colliding with nothing, and
			// goblinctl had no version verb of its own to collide with -
			// so the conflict was avoided by having none at all
			// (GOBLIN-DIV-052).
			//
			// Now there IS a reason. NewControlRoot gives goblinctl its
			// own `version`, and a mounted copy would render the same
			// bytes - cobra resolves cmd.Root() to goblinctl once
			// mounted, so `goblinctl agent version` would print
			// goblinctl's block, not the kernel's. A second spelling of
			// one output is a surface to keep in step for no benefit.
			continue
		}
		rebrandMounted(cmd)
		agentCmd.AddCommand(cmd)
	}
}

var runCmd = &cobra.Command{
	Use:   "run <job-file.yaml>",
	Short: "Submit a job to the cluster (YAML or JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		// Read job spec from YAML/JSON file
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("failed to read job file: %w", err)
		}

		// Decode YAML/JSON -> map -> JSON -> Proto
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("failed to parse job spec: %w", err)
		}
		jsonBytes, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var job goblinv1.Job
		if err := json.Unmarshal(jsonBytes, &job); err != nil {
			return fmt.Errorf("failed to parse job spec: %w", err)
		}

		// Connect to QUIC RPC
		client, err := dialControl()
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", controlAddr(), err)
		}
		defer closeClient(client, &err)

		req := &goblinv1.SubmitJobRequest{Job: &job}
		var resp goblinv1.SubmitJobResponse
		if err := client.Call("SchedulerRPC.SubmitJob", req, &resp); err != nil {
			return fmt.Errorf("job submission failed: %w", err)
		}

		fmt.Printf("job %s submitted, assigned to node %s\n", resp.GetJobId(), resp.GetAssignedNode())
		return nil
	},
}

var drainCmd = &cobra.Command{
	Use:   "drain <node-id>",
	Short: "Drain all jobs from a node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		nodeID := args[0]

		// Connect to QUIC RPC
		client, err := dialControl()
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", controlAddr(), err)
		}
		defer closeClient(client, &err)

		req := &goblinv1.DrainNodeRequest{NodeId: nodeID}
		var resp goblinv1.DrainNodeResponse
		if err := client.Call("SchedulerRPC.DrainNode", req, &resp); err != nil {
			return fmt.Errorf("drain failed: %w", err)
		}

		fmt.Printf("[OK] Drained %d jobs from node %s\n", len(resp.GetMigratedJobIds()), nodeID)
		for _, jobID := range resp.GetMigratedJobIds() {
			fmt.Printf("  - %s\n", jobID)
		}
		return nil
	},
}

// migrateInstanceCmd live-migrates a RUNNING process, memory intact.
// Deliberately a separate verb from "migrate": that one reassigns a
// job, which stops the work in one place and starts it in another. This
// one moves the process itself, and the instance UUID never changes.
var migrateInstanceCmd = &cobra.Command{
	Use:   "migrate-instance <instance-uuid> <to-node>",
	Short: "Live-migrate a running instance to another node (CRIU)",
	Long: `Live-migrate a running instance to another node.

The instance is checkpointed on its current node, its memory image is
transferred, and it is restored on the destination under the same
instance UUID. The process keeps its state; only its location changes.

If any step fails the instance is restored on its source and the
migration is reported as rolled back. If the rollback also fails the
instance is running nowhere, and the error says so explicitly.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		req := &goblinv1.MigrateInstanceRequest{
			InstanceId: args[0],
			ToNode:     args[1],
		}

		client, err := dialControl()
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", controlAddr(), err)
		}
		defer closeClient(client, &err)

		var resp goblinv1.MigrateInstanceResponse
		if err := client.Call("SchedulerRPC.MigrateInstance", req, &resp); err != nil {
			// The coordinator's outcomes are materially different for an
			// operator, so they are surfaced rather than flattened into
			// one "migration failed".
			return fmt.Errorf("live migration failed: %w", err)
		}

		fmt.Printf("instance %s migrated from %s to %s\n", resp.GetInstanceId(), resp.GetFromNode(), resp.GetToNode())
		return nil
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate <job-id> <to-node>",
	Short: "Migrate a job to another node",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		req := &goblinv1.MigrateJobRequest{
			JobId:  args[0],
			ToNode: args[1],
		}

		// Connect to QUIC RPC
		client, err := dialControl()
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", controlAddr(), err)
		}
		defer closeClient(client, &err)

		// Call SchedulerRPC.MigrateJob
		var resp goblinv1.MigrateJobResponse
		if err := client.Call("SchedulerRPC.MigrateJob", req, &resp); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}

		fmt.Printf("job %s migrated to %s\n", resp.GetJobId(), resp.GetToNode())
		return nil
	},
}

// ... generateDocsCert ...

// ... quicRequest ...

var publishCmd = &cobra.Command{
	Use:   "publish [event-name] [payload]",
	Short: "Publish user event to cluster",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := dialControl()
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		topic := args[0]
		payload := []byte(args[1])

		// TODO: handle tags/namespace if we want to mimic Serf details precisely,
		// but the new RPC method just takes topic and payload.
		// Historically publishCmd used Serf directly.

		req := &goblinv1.PublishEventRequest{Topic: topic, Payload: payload}
		var resp goblinv1.PublishEventResponse
		if err := client.Call("SchedulerRPC.PublishEvent", req, &resp); err != nil {
			return err
		}

		fmt.Printf("event '%s' published\n", resp.GetTopic())
		return nil
	},
}

// statusCmd queries cluster status via Serf RPC
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cluster status",
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		client, err := dialControl()
		if err != nil {
			return err
		}
		defer closeClient(client, &err)

		// Call SchedulerRPC.Members
		var resp goblinv1.MembersResponse
		if err := client.Call("SchedulerRPC.Members", &goblinv1.MembersRequest{}, &resp); err != nil {
			return fmt.Errorf("failed to get members: %w", err)
		}

		fmt.Printf("[OK] Connected to %s (QUIC)\n", controlAddr())
		fmt.Printf("Cluster Members: %d\n", len(resp.GetMembers()))
		fmt.Println("NAME\t\tADDRESS\t\t\tSTATUS\t\tROLE\t\tTAGS")
		fmt.Println("----\t\t-------\t\t\t------\t\t----\t\t----")
		for _, m := range resp.GetMembers() {
			tags := ""
			for k, v := range m.GetTags() {
				tags += fmt.Sprintf("%s=%s ", k, v)
			}
			role := "follower"
			if m.GetLeader() {
				role = "LEADER"
			}
			fmt.Printf("%s\t%s\t\t%s\t%s\t\t%s\n", m.GetName(), m.GetAddr(), m.GetStatus(), role, tags)
		}
		return nil
	},
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Unified cluster and agent TUI",
	RunE: func(cmd *cobra.Command, args []string) error {
		// One address for both GAPI and Cluster RPC: they share the QUIC endpoint.
		ctrl := NewUnifiedController(controlAddr(), controlAddr())
		return tui.Run(ctrl)
	},
}

// rebrandMounted rewrites a mounted command's help so its examples name
// the binary the operator actually invoked.
//
// The kernel's verbs carry examples written for gapictl - "gapictl agent
// build ...". Mounted here they run as "goblinctl agent build ...", so
// printed verbatim they instruct an operator to use a binary that is not
// part of their installation, and the example does not work as written
// (GOBLIN-DIV-056).
//
// Rewriting at MOUNT time rather than fixing the strings in the kernel is
// what makes this correct in both products: gapictl keeps examples that
// name gapictl, goblinctl shows examples that name goblinctl, from one
// definition. The flattening in the loop above lines the paths up
// exactly - the kernel's `agent build` becomes goblinctl's `agent build`
// - so substituting the binary name is the whole transformation.
func rebrandMounted(cmd *cobra.Command) {
	const from, to = "gapictl ", "goblinctl "
	cmd.Long = strings.ReplaceAll(cmd.Long, from, to)
	cmd.Example = strings.ReplaceAll(cmd.Example, from, to)
	for _, sub := range cmd.Commands() {
		rebrandMounted(sub)
	}
}
