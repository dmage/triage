package exporttriage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/MakeNowJust/heredoc/v2"
	"github.com/dmage/triage/pkg/artifacts"
	"github.com/dmage/triage/pkg/cache"
	"github.com/dmage/triage/pkg/types"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
)

type jsonBuild struct {
	Path        string `json:"path"`
	Started     string `json:"started"`
	Elapsed     string `json:"elapsed"`
	TestsRun    string `json:"tests_run"`
	TestsFailed string `json:"tests_failed"`
	Job         string `json:"job"`
	Number      string `json:"number"`
}

type jsonFailure struct {
	Started     string `json:"started"`
	Path        string `json:"build"`
	Name        string `json:"name"`
	FailureText string `json:"failure_text"`
}

type ExportTriageOptions struct {
	Builds     string
	Tests      string
	NumWorkers int
	AgeLimit   time.Duration

	createdAfter int64
}

func (opts *ExportTriageOptions) startBuildsExporter(builds <-chan jsonBuild) <-chan error {
	done := make(chan error)
	go func() {
		f, err := os.Create(opts.Builds)
		if err != nil {
			done <- fmt.Errorf("unable to open %s: %w", opts.Builds, err)
		}

		var bs []jsonBuild
		t := time.NewTicker(30 * time.Second)
		for build := range builds {
			bs = append(bs, build)
			select {
			case <-t.C:
				klog.V(2).Infof("Processed %d builds", len(bs))
			default:
			}
		}
		t.Stop()
		klog.V(2).Infof("Processed %d builds", len(bs))
		err = json.NewEncoder(f).Encode(bs)
		if err != nil {
			done <- fmt.Errorf("unable to save builds into %s: %w", opts.Builds, err)
			return
		}
		done <- f.Close()
	}()
	return done
}

func (opts *ExportTriageOptions) startFailuresExporter(failures <-chan jsonFailure) <-chan error {
	done := make(chan error)
	go func() {
		f, err := os.Create(opts.Tests)
		if err != nil {
			done <- fmt.Errorf("unable to open %s: %w", opts.Tests, err)
		}

		for failure := range failures {
			buf, err := json.Marshal(failure)
			if err != nil {
				done <- fmt.Errorf("unable to marshal failure: %w", err)
			}
			buf = append(buf, '\n')
			_, err = f.Write(buf)
			if err != nil {
				done <- fmt.Errorf("unable to write failure into %s: %w", opts.Tests, err)
			}
		}
		done <- f.Close()
	}()
	return done
}

func (opts *ExportTriageOptions) worker(ctx context.Context, db *cache.Storage, client *artifacts.Client, builds <-chan types.Build, jsonBuilds chan<- jsonBuild, jsonFailures chan<- jsonFailure) error {
	for build := range builds {
		klog.V(3).Infof("Analyzing %s @ %s...", build.Job, build.BuildID)

		buildFiles, err := db.LoadBuildFiles(&build)
		if cache.IsNotFound(err) {
			buildFiles, err = client.GetBuildFiles(ctx, &build)
			if err != nil {
				return err
			}

			if !buildFiles.Has("finished.json") {
				klog.V(4).Infof("%s @ %s does not have finished.json, skipping...", build.Job, build.BuildID)
				continue
			}

			err = db.SaveBuildFiles(buildFiles)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		started, err := client.GetStartedJson(ctx, &build)
		if err != nil {
			return err
		}

		finished, err := client.GetFinishedJson(ctx, &build)
		if err != nil {
			return err
		}

		testResults, err := client.GetTestResults(ctx, buildFiles)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("%s/%s", build.GCSBucket, strings.TrimSuffix(build.GCSPrefix, "/"))

		testsRun := 0
		testsFailed := 0
		for _, r := range testResults {
			switch r.Status {
			case artifacts.TestStatusSuccess:
				testsRun++
			case artifacts.TestStatusFailure:
				testsRun++
				testsFailed++
				jsonFailures <- jsonFailure{
					Started:     fmt.Sprintf("%d", started.Timestamp),
					Path:        path,
					Name:        r.Test,
					FailureText: r.Summary,
				}
			}
		}

		jsonBuilds <- jsonBuild{
			Path:        path,
			Started:     fmt.Sprintf("%d", started.Timestamp),
			Elapsed:     fmt.Sprintf("%d", finished.Timestamp-started.Timestamp),
			TestsRun:    fmt.Sprintf("%d", testsRun),
			TestsFailed: fmt.Sprintf("%d", testsFailed),
			Job:         build.Job,
			Number:      build.BuildID,
		}
	}
	return nil
}

func (opts *ExportTriageOptions) Run(ctx context.Context) error {
	db, err := cache.New()
	if err != nil {
		return err
	}
	defer db.Close()

	gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return err
	}

	client := artifacts.NewClient(gcsClient)

	builds, err := db.FindBuilds(opts.createdAfter)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Found %d builds", len(builds))

	inputs := make(chan types.Build)
	jsonBuilds := make(chan jsonBuild)
	jsonFailures := make(chan jsonFailure)
	errs := make(chan error, opts.NumWorkers)

	buildsExporter := opts.startBuildsExporter(jsonBuilds)
	failuresExporter := opts.startFailuresExporter(jsonFailures)
	done := make(chan struct{})

	go func() {
		buildsExporterDone := false
		failuresExporterDone := false
		for {
			select {
			case err := <-buildsExporter:
				if err != nil {
					klog.Exit(err)
				}
				buildsExporterDone = true
			case err := <-failuresExporter:
				if err != nil {
					klog.Exit(err)
				}
				failuresExporterDone = true
			}
			if buildsExporterDone && failuresExporterDone {
				close(done)
				break
			}
		}
	}()

	for i := 0; i < opts.NumWorkers; i++ {
		go func() {
			errs <- opts.worker(ctx, db, client, inputs, jsonBuilds, jsonFailures)
		}()
	}

	for _, build := range builds {
		inputs <- build
	}
	close(inputs)

	for i := 0; i < opts.NumWorkers; i++ {
		err := <-errs
		if err != nil {
			return err
		}
	}
	close(jsonBuilds)
	close(jsonFailures)

	<-done

	return nil
}

func NewCmdExportTriage() *cobra.Command {
	opts := &ExportTriageOptions{}

	cmd := &cobra.Command{
		Use:   "export-triage",
		Short: "Generate files for triage",
		Long: heredoc.Doc(`
			Generate triage_builds.json and triage_tests.json for triage.
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

	cmd.Flags().StringVar(&opts.Builds, "builds", "./triage_builds.json", "file to save builds json")
	cmd.Flags().StringVar(&opts.Tests, "tests", "./triage_tests.json", "file to save tests json")
	cmd.Flags().IntVarP(&opts.NumWorkers, "num_workers", "w", 10, "number of workers to spawn")
	cmd.Flags().DurationVar(&opts.AgeLimit, "age", 14*24*time.Hour, "index only builds that are younger than the theshold")

	return cmd
}
