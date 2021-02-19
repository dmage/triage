package discovertestgrid

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
	"github.com/MakeNowJust/heredoc/v2"
	"github.com/dmage/triage/pkg/artifacts"
	"github.com/dmage/triage/pkg/cache"
	"github.com/dmage/triage/pkg/config"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
)

type DiscoverTestGridOptions struct {
	ConfigPaths []string
	NumWorkers  int
	AgeLimit    time.Duration

	createdAfter int64
}

func (opts *DiscoverTestGridOptions) worker(ctx context.Context, db *cache.Storage, client *artifacts.Client, testGroups <-chan config.TestGroup) error {
	for testGroup := range testGroups {
		builds, err := client.FindBuilds(ctx, testGroup.Name, testGroup.GCSPrefix)
		if err != nil {
			return fmt.Errorf("unable to find builds for %s: %w", testGroup.Name, err)
		}

		for i, j := 0, len(builds)-1; i < j; i, j = i+1, j-1 {
			builds[i], builds[j] = builds[j], builds[i]
		}

		for _, build := range builds {
			_, startedAt, err := db.LoadBuild(build.Job, build.BuildID)
			if cache.IsNotFound(err) {
				klog.V(3).Infof("Discovered new build: %s @ %s", build.Job, build.BuildID)

				started, err := client.GetStartedJson(ctx, build)
				if artifacts.IsNotFound(err) {
					klog.V(3).Infof("%s @ %s does not have started.json, skipping...", build.Job, build.BuildID)
					continue
				} else if artifacts.IsInvalidJSON(err) {
					klog.V(3).Infof("%s @ %s has invalid started.json: %s", build.Job, build.BuildID, err)
					continue
				} else if err != nil {
					return fmt.Errorf("unable to get started.json: %w", err)
				}

				err = db.SaveBuild(build, started.Timestamp)
				if err != nil {
					return fmt.Errorf("unable to save build: %w", err)
				}

				startedAt = started.Timestamp
			} else if err != nil {
				return fmt.Errorf("unable to load build from cache: %w", err)
			}

			if opts.createdAfter != 0 && startedAt < opts.createdAfter {
				break
			}
		}
	}
	return nil
}

func (opts *DiscoverTestGridOptions) Run(ctx context.Context) error {
	db, err := cache.New()
	if err != nil {
		return err
	}
	defer db.Close()

	var testGroups []config.TestGroup
	for _, path := range opts.ConfigPaths {
		cfg, err := config.LoadFromFile(path)
		if err != nil {
			return err
		}
		testGroups = append(testGroups, cfg.TestGroups...)
	}

	gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return err
	}

	client := artifacts.NewClient(gcsClient)

	inputs := make(chan config.TestGroup)
	errs := make(chan error, opts.NumWorkers)

	for i := 0; i < opts.NumWorkers; i++ {
		go func() {
			errs <- opts.worker(ctx, db, client, inputs)
		}()
	}

	for _, testGroup := range testGroups {
		inputs <- testGroup
	}
	close(inputs)

	for i := 0; i < opts.NumWorkers; i++ {
		err := <-errs
		if err != nil {
			return err
		}
	}

	return nil
}

func NewCmdDiscoverTestGrid() *cobra.Command {
	opts := &DiscoverTestGridOptions{}

	cmd := &cobra.Command{
		Use:   "discover-testgrid <testgrid.yaml>...",
		Short: "Discover new builds from TestGrid configuration",
		Long: heredoc.Doc(`
			Scan GCS locations from TestGrid configuration to discover new builds.
		`),
		Run: func(cmd *cobra.Command, args []string) {
			if opts.AgeLimit != 0 {
				opts.createdAfter = time.Now().Add(-opts.AgeLimit).Unix()
			}

			opts.ConfigPaths = args

			err := opts.Run(cmd.Context())
			if err != nil {
				klog.Exit(err)
			}
		},
	}

	cmd.Flags().IntVarP(&opts.NumWorkers, "num_workers", "w", 10, "number of workers to spawn")
	cmd.Flags().DurationVar(&opts.AgeLimit, "age", 14*24*time.Hour, "index only builds that are younger than the theshold")

	return cmd
}
