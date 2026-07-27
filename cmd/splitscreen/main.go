// Command splitscreen is the whole system: gateway, runner, and the local
// helpers a runner spawns. One binary, one subcommand per role.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	// Registers the Claude Code harness adapter.
	_ "github.com/avarant/splitscreen/internal/harness/claudecode"
)

// Version is stamped at build time.
var Version = "dev"

// allCommands is the full subcommand set, in one place so the tree can be
// asserted rather than trusted.
func allCommands() []*cobra.Command {
	return []*cobra.Command{
		gatewayCmd(),
		runnerCmd(),
		enrollCmd(),
		configCmd(),
		certCmd(),
		credentialHelperCmd(),
		permissionShimCmd(),
		mcpShimCmd(),
		sendFileCmd(),
	}
}

func main() {
	root := &cobra.Command{
		Use:   "splitscreen",
		Short: "A multiplayer agent harness: one gateway, many runners",
		Long: `Splitscreen runs coding agents that teams drive from chat.

The gateway owns the chat surfaces, every credential, the routing table, and the
audit log. Runners own working trees and execute agents, holding nothing secret
at rest.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}

	root.AddCommand(allCommands()...)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
