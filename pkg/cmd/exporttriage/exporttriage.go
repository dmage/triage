package exporttriage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/MakeNowJust/heredoc/v2"
	"github.com/dmage/triage/pkg/artifacts"
	"github.com/dmage/triage/pkg/cache"
	"github.com/dmage/triage/pkg/kvcache"
	"github.com/dmage/triage/pkg/testname"
	"github.com/dmage/triage/pkg/types"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
)

type testStats struct {
	Succeed int
	Failed  int
	Skipped int
	Error   int
}

type buildSummary struct {
	Job       string
	BuildID   string
	Started   int64
	Result    string
	TestStats map[string]*testStats
}

type jsonTestStats struct {
	Succeed []string
	Failed  []string
	Flaked  []string
	Skipped []string
}

func newJsonTestStats() *jsonTestStats {
	return &jsonTestStats{
		Succeed: []string{},
		Failed:  []string{},
		Flaked:  []string{},
		Skipped: []string{},
	}
}

type jsonSummary map[string]map[string]*jsonTestStats

type jsonTest struct {
	Path string `json:"build"`
	Name string `json:"name"`
}

type jsonBuild struct {
	Path        string `json:"path"`
	Started     string `json:"started"`
	Elapsed     string `json:"elapsed"`
	TestsRun    string `json:"tests_run"`
	TestsFailed string `json:"tests_failed"`
	Job         string `json:"job"`
	Number      string `json:"number"`

	// Additional info that is not required for triage dashboard
	Result string `json:"result"`
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
	Summary    string
	NumWorkers int
	AgeLimit   time.Duration

	createdAfter int64
	cache        *kvcache.KVCache
}

func (opts *ExportTriageOptions) buildsExporter(builds <-chan jsonBuild) (err error) {
	if opts.Builds == "" {
		for range builds {
		}
		return nil
	}

	f, err := os.Create(opts.Builds)
	if err != nil {
		return fmt.Errorf("unable to open %s: %w", opts.Builds, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

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
		return fmt.Errorf("unable to save builds into %s: %w", opts.Builds, err)
	}

	return nil
}

func (opts *ExportTriageOptions) failuresExporter(failures <-chan jsonFailure) (err error) {
	if opts.Tests == "" {
		for range failures {
		}
		return nil
	}

	f, err := os.Create(opts.Tests)
	if err != nil {
		return fmt.Errorf("unable to open %s: %w", opts.Tests, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	for failure := range failures {
		buf, err := json.Marshal(failure)
		if err != nil {
			return fmt.Errorf("unable to marshal failure: %w", err)
		}
		buf = append(buf, '\n')
		_, err = f.Write(buf)
		if err != nil {
			return fmt.Errorf("unable to write failure into %s: %w", opts.Tests, err)
		}
	}

	return nil
}

func (opts *ExportTriageOptions) summaryExporter(builds <-chan buildSummary) (err error) {
	if opts.Summary == "" {
		for range builds {
		}
		return nil
	}

	f, err := os.Create(opts.Summary)
	if err != nil {
		return fmt.Errorf("unable to open %s: %w", opts.Summary, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	summary := make(jsonSummary)
	for bs := range builds {
		for test, stats := range bs.TestStats {
			testSummary := summary[test]
			if testSummary == nil {
				testSummary = make(map[string]*jsonTestStats)
				summary[test] = testSummary
			}

			jobSummary := testSummary[bs.Job]
			if jobSummary == nil {
				jobSummary = newJsonTestStats()
				testSummary[bs.Job] = jobSummary
			}

			if stats.Failed > 0 || stats.Error > 0 {
				if stats.Succeed > 0 || bs.Result == "SUCCESS" {
					jobSummary.Flaked = append(jobSummary.Flaked, bs.BuildID)
				} else {
					jobSummary.Failed = append(jobSummary.Failed, bs.BuildID)
				}
			} else if stats.Succeed > 0 {
				jobSummary.Succeed = append(jobSummary.Succeed, bs.BuildID)
			} else if stats.Skipped > 0 {
				jobSummary.Skipped = append(jobSummary.Skipped, bs.BuildID)
			} else {
				panic(fmt.Errorf("unexpected result: %#+v", stats))
			}
		}
	}

	err = json.NewEncoder(f).Encode(summary)
	if err != nil {
		return fmt.Errorf("unable to save summary into %s: %w", opts.Summary, err)
	}

	return nil
}

type BuildData struct {
	StartedJson  artifacts.StartedJson
	FinishedJson artifacts.FinishedJson
	TestResults  []*artifacts.TestResult
}

func (opts *ExportTriageOptions) createBuildData(ctx context.Context, db *cache.Storage, client *artifacts.Client, build types.Build) (*BuildData, error) {
	klog.V(3).Infof("Getting data for %s @ %s...", build.Job, build.BuildID)

	buildFiles, err := db.LoadBuildFiles(&build)
	if cache.IsNotFound(err) {
		buildFiles, err = client.GetBuildFiles(ctx, &build)
		if err != nil {
			return nil, err
		}

		if !buildFiles.Has("finished.json") {
			klog.V(4).Infof("%s @ %s does not have finished.json, skipping...", build.Job, build.BuildID)
			return nil, nil
		}

		err = db.SaveBuildFiles(buildFiles)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	started, err := client.GetStartedJson(ctx, &build)
	if err != nil {
		return nil, err
	}

	finished, err := client.GetFinishedJson(ctx, &build)
	if artifacts.IsInvalidJSON(err) {
		klog.V(2).Infof("%s @ %s has corrupted finished.json: %s", build.Job, build.BuildID, err)
	} else if err != nil {
		return nil, err
	}

	testResults, err := client.GetTestResults(ctx, buildFiles)
	if err != nil {
		return nil, err
	}

	return &BuildData{
		StartedJson:  started,
		FinishedJson: finished,
		TestResults:  testResults,
	}, nil
}

func (opts *ExportTriageOptions) getBuildData(ctx context.Context, db *cache.Storage, client *artifacts.Client, build types.Build) (*BuildData, error) {
	buildData := &BuildData{}
	key := fmt.Sprintf("%s/%s", build.Job, build.BuildID)
	err := opts.cache.Load(key, buildData)
	if kvcache.IsNotFound(err) {
		buildData, err = opts.createBuildData(ctx, db, client, build)
		if buildData == nil || err != nil {
			return buildData, err
		}

		err = opts.cache.Save(key, buildData)
		if err != nil {
			return buildData, err
		}
	}
	return buildData, err
}

func (opts *ExportTriageOptions) handleBuild(ctx context.Context, db *cache.Storage, client *artifacts.Client, build types.Build, jsonBuilds chan<- jsonBuild, jsonFailures chan<- jsonFailure, buildSummaries chan<- buildSummary) error {
	klog.V(4).Infof("Analyzing %s @ %s...", build.Job, build.BuildID)

	buildData, err := opts.getBuildData(ctx, db, client, build)
	if err != nil {
		return err
	}
	if buildData == nil {
		return nil
	}

	path := fmt.Sprintf("%s/%s", build.GCSBucket, strings.TrimSuffix(build.GCSPrefix, "/"))

	bs := buildSummary{
		Job:       build.Job,
		BuildID:   build.BuildID,
		Started:   buildData.StartedJson.Timestamp,
		Result:    buildData.FinishedJson.Result,
		TestStats: make(map[string]*testStats),
	}

	testsRun := 0
	testsFailed := 0
	for _, r := range buildData.TestResults {
		normalizedName := testname.Normalize(r.Test)
		stats := bs.TestStats[normalizedName]
		if stats == nil {
			stats = new(testStats)
			bs.TestStats[normalizedName] = stats
		}

		switch r.Status {
		case artifacts.TestStatusSuccess:
			testsRun++
			stats.Succeed++
		case artifacts.TestStatusFailure:
			summary := r.Summary
			if idx := strings.Index(summary, "\n\n"); idx != -1 {
				summary = summary[:idx]
			}

			testsRun++
			testsFailed++
			jsonFailures <- jsonFailure{
				Started:     fmt.Sprintf("%d", buildData.StartedJson.Timestamp),
				Path:        path,
				Name:        r.Test,
				FailureText: summary,
			}
			stats.Failed++
		case artifacts.TestStatusSkipped:
			stats.Skipped++
		case artifacts.TestStatusError:
			stats.Error++
		}
	}

	jsonBuilds <- jsonBuild{
		Path:        path,
		Started:     fmt.Sprintf("%d", buildData.StartedJson.Timestamp),
		Elapsed:     fmt.Sprintf("%d", buildData.FinishedJson.Timestamp-buildData.StartedJson.Timestamp),
		TestsRun:    fmt.Sprintf("%d", testsRun),
		TestsFailed: fmt.Sprintf("%d", testsFailed),
		Job:         build.Job,
		Number:      build.BuildID,
		Result:      buildData.FinishedJson.Result,
	}

	buildSummaries <- bs

	return nil
}

func (opts *ExportTriageOptions) worker(ctx context.Context, db *cache.Storage, client *artifacts.Client, builds <-chan types.Build, jsonBuilds chan<- jsonBuild, jsonFailures chan<- jsonFailure, buildSummaries chan<- buildSummary) error {
	for build := range builds {
		if err := opts.handleBuild(ctx, db, client, build, jsonBuilds, jsonFailures, buildSummaries); err != nil {
			return err
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

	jsonBuilds := make(chan jsonBuild)
	jsonFailures := make(chan jsonFailure)
	buildSummaries := make(chan buildSummary)
	inputs := make(chan types.Build)
	errs := make(chan error, opts.NumWorkers)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := opts.buildsExporter(jsonBuilds); err != nil {
			klog.Exit(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := opts.failuresExporter(jsonFailures); err != nil {
			klog.Exit(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := opts.summaryExporter(buildSummaries); err != nil {
			klog.Exit(err)
		}
	}()

	for i := 0; i < opts.NumWorkers; i++ {
		go func() {
			errs <- opts.worker(ctx, db, client, inputs, jsonBuilds, jsonFailures, buildSummaries)
		}()
	}

	go func() {
		for _, build := range builds {
			inputs <- build
		}
		close(inputs)
	}()

	for i := 0; i < opts.NumWorkers; i++ {
		err := <-errs
		if err != nil {
			return err
		}
	}
	close(jsonBuilds)
	close(jsonFailures)
	close(buildSummaries)

	wg.Wait()

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

			opts.cache = kvcache.NewDefaultKVCache()

			err := opts.Run(cmd.Context())
			if err != nil {
				klog.Exit(err)
			}
		},
	}

	cmd.Flags().StringVar(&opts.Builds, "builds", "", "file to save builds json")
	cmd.Flags().StringVar(&opts.Tests, "tests", "", "file to save tests json")
	cmd.Flags().StringVar(&opts.Summary, "summary", "", "file to save summary json")
	cmd.Flags().IntVarP(&opts.NumWorkers, "num_workers", "w", 10, "number of workers to spawn")
	cmd.Flags().DurationVar(&opts.AgeLimit, "age", 14*24*time.Hour, "index only builds that are younger than the theshold")

	return cmd
}
