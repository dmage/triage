package main

import (
	"fmt"
	"os"

	"github.com/dmage/triage/pkg/cmd/cleanup"
	"github.com/dmage/triage/pkg/cmd/discovertestgrid"
	"github.com/dmage/triage/pkg/cmd/exporttriage"
	"github.com/dmage/triage/pkg/cmd/serve"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "scraper",
	Short: "Scraper discovers and analyzes CI builds",
	Run: func(cmd *cobra.Command, args []string) {
	},
}

func init() {
	rootCmd.AddCommand(discovertestgrid.NewCmdDiscoverTestGrid())
	rootCmd.AddCommand(exporttriage.NewCmdExportTriage())
	rootCmd.AddCommand(serve.NewCmdServe())
	rootCmd.AddCommand(cleanup.NewCmdCleanup())
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
