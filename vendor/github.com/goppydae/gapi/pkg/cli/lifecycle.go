package cli

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/client"
	"github.com/goppydae/gapi/core/config"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// sendLifecycleCommand sends control commands in parallel to multiple agents
func sendLifecycleCommand(agentIDs []string, action protopkg.LifecycleControl_Action) {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	c, err := client.New(cfg)
	if err != nil {
		log.Fatalf("failed to init client: %v", err)
	}

	// Long timeout for lifecycle
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := c.Lifecycle(ctx, agentIDs, action)

	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("❌ %s: %v\n", r.AgentID, r.Err)
			continue
		}
		fmt.Printf("✅ %s → %s: %s\n", r.AgentID, r.Status.GetState(), r.Status.GetMessage())
	}
}

// Lifecycle CLI commands

var lifecycleStartCmd = &cobra.Command{
	Use:   "start AGENT...",
	Short: "Send start command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_START)
	},
}

var lifecycleStopCmd = &cobra.Command{
	Use:   "stop AGENT...",
	Short: "Send stop command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_STOP)
	},
}

var lifecycleRestartCmd = &cobra.Command{
	Use:   "restart AGENT...",
	Short: "Send restart command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_RESTART)
	},
}

var lifecycleReloadCmd = &cobra.Command{
	Use:   "reload AGENT...",
	Short: "Send reload command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_RELOAD)
	},
}

var lifecycleStatusCmd = &cobra.Command{
	Use:   "status AGENT...",
	Short: "Query lifecycle state of one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_ACTION_UNSPECIFIED)
	},
}

// Register the lifecycle command group
func init() {
	lifecycleCmd := &cobra.Command{
		Use:   "lifecycle",
		Short: "Control agent lifecycle",
	}
	lifecycleCmd.AddCommand(
		lifecycleStartCmd,
		lifecycleStopCmd,
		lifecycleRestartCmd,
		lifecycleReloadCmd,
		lifecycleStatusCmd,
	)
	rootCmd.AddCommand(lifecycleCmd)
}
