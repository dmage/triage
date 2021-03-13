package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/MakeNowJust/heredoc/v2"
	"github.com/dmage/triage/pkg/cache"
	"github.com/dmage/triage/pkg/kvcache"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type CleanupOptions struct {
	AgeLimit time.Duration

	createdAfter int64
}

func (opts *CleanupOptions) Run(ctx context.Context) error {
	db, err := cache.New()
	if err != nil {
		return err
	}
	defer db.Close()

	cache := kvcache.NewDefaultKVCache()

	builds, err := db.FindOldBuilds(opts.createdAfter)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Found %d builds", len(builds))

	for _, build := range builds {
		err := cache.Delete(fmt.Sprintf("%s/%s", build.Job, build.BuildID))
		if err != nil {
			return err
		}

		err = db.DeleteBuildFiles(&build)
		if err != nil {
			return err
		}

		err = db.DeleteBuild(build.Job, build.BuildID)
		if err != nil {
			return err
		}
	}

	return nil
}

func NewCmdCleanup() *cobra.Command {
	opts := &CleanupOptions{}

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete cached data",
		Long: heredoc.Doc(`
			Delete cached files that can be downloaded again from GCS.
		`),
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if opts.AgeLimit != 0 {
				opts.createdAfter = time.Now().Add(-opts.AgeLimit).Unix()
			}

			err := opts.Run(cmd.Context())
			if err != nil {
				klog.Exit(err)
			}
		},
	}

	cmd.Flags().DurationVar(&opts.AgeLimit, "age", 14*24*time.Hour, "delete only builds that are older than the theshold")

	return cmd
}
