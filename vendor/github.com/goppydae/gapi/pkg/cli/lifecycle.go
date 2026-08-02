package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/client"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// sendLifecycleCommand sends control commands in parallel to multiple agents
func sendLifecycleCommand(agentIDs []string, action protopkg.LifecycleControl_Action) {
	cfg, err := controlConfig()
	if err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to load config", logattr.Err(err))
		os.Exit(1)
	}

	c, err := client.New(cfg)
	if err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to init client", logattr.Err(err))
		os.Exit(1)
	}

	// Long timeout for lifecycle
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := c.Lifecycle(ctx, agentIDs, action)

	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("[FAIL] %s: %v\n", r.AgentID, r.Err)
			continue
		}
		fmt.Printf("[OK] %s -> %s: %s\n", r.AgentID, r.Status.GetState(), r.Status.GetMessage())
	}
}

// Lifecycle CLI commands

var lifecycleStartCmd = &cobra.Command{
	Use:   "start AGENT...",
	Short: "Send start command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_ACTION_START)
	},
}

var lifecycleStopCmd = &cobra.Command{
	Use:   "stop AGENT...",
	Short: "Send stop command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_ACTION_STOP)
	},
}

var lifecycleRestartCmd = &cobra.Command{
	Use:   "restart AGENT...",
	Short: "Send restart command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_ACTION_RESTART)
	},
}

var lifecycleReloadCmd = &cobra.Command{
	Use:   "reload AGENT...",
	Short: "Send reload command to one or more agents",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sendLifecycleCommand(args, protopkg.LifecycleControl_ACTION_RELOAD)
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
