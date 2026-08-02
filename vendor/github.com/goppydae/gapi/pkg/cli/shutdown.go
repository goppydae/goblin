package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/client"
)

var (
	shutdownReboot bool
	shutdownHalt   bool
)

var shutdownCmd = &cobra.Command{
	Use:   "shutdown [--reboot|--halt]",
	Short: "Request a system shutdown from the daemon (poweroff by default)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if shutdownReboot && shutdownHalt {
			return fmt.Errorf("--reboot and --halt are mutually exclusive")
		}
		action := "poweroff"
		if shutdownReboot {
			action = "reboot"
		}
		if shutdownHalt {
			action = "halt"
		}

		cfg, err := controlConfig()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		c, err := client.New(cfg)
		if err != nil {
			return fmt.Errorf("init client: %w", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Shutdown(ctx, action); err != nil {
			return err
		}
		fmt.Printf("shutdown requested: %s\n", action)
		return nil
	},
}

func init() {
	shutdownCmd.Flags().BoolVar(&shutdownReboot, "reboot", false, "Reboot instead of powering off")
	shutdownCmd.Flags().BoolVar(&shutdownHalt, "halt", false, "Halt instead of powering off")
}
