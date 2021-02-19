package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/testgrid/metadata/junit"
	"google.golang.org/api/iterator"
	"k8s.io/klog/v2"
)

var junitObject = regexp.MustCompile(`/junit.*\.xml$`)

func IsNotFound(err error) bool {
	return errors.Is(err, storage.ErrObjectNotExist)
}

type InvalidJSONError struct {
	msg string
	err error
}

func IsInvalidJSON(err error) bool {
	var e InvalidJSONError
	return errors.As(err, &e)
}

func (e InvalidJSONError) Error() string {
	return fmt.Sprintf("%s: %s", e.msg, e.err.Error())
}

func (e InvalidJSONError) Unwrap() error {
	return e.err
}

type Build struct {
	Job       string
	BuildID   string
	GCSBucket string
	GCSPrefix string
}

func (b Build) String() string {
	return fmt.Sprintf("%s @ %s (gs://%s/%s)", b.Job, b.BuildID, b.GCSBucket, b.GCSPrefix)
}

type BuildFiles struct {
	Build *Build              `json:"build"`
	Files map[string]struct{} `json:"files"`
}

func (f BuildFiles) Has(filename string) bool {
	_, ok := f.Files[f.Build.GCSPrefix+filename]
	return ok
}

type StartedJson struct {
	Timestamp int64 `json:"timestamp"`
}

type FinishedJson struct {
	Timestamp int64  `json:"timestamp"`
	Result    string `json:"result"`
}

type TestStatus string

const (
	TestStatusSkipped TestStatus = "Skipped"
	TestStatusError   TestStatus = "Error"
	TestStatusFailure TestStatus = "Failure"
	TestStatusSuccess TestStatus = "Success"
)

type TestResult struct {
	Test    string
	Status  TestStatus
	Output  string
	Summary string
}

type Client struct {
	cacheDir  string
	gcsClient *storage.Client
}

func NewClient(gcsClient *storage.Client) *Client {
	return &Client{
		cacheDir:  "./cache",
		gcsClient: gcsClient,
	}
}

func (c *Client) gcsListDir(ctx context.Context, bucket, prefix string) (dirs []string, files []string, err error) {
	klog.V(4).Infof("Listing gs://%s/%s...", bucket, prefix)

	bkt := c.gcsClient.Bucket(bucket)
	q := &storage.Query{
		Delimiter:  "/",
		Prefix:     prefix,
		Projection: storage.ProjectionNoACL,
	}
	q.SetAttrSelection([]string{"Name"})
	it := bkt.Objects(ctx, q)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list objects in gs://%s/%s: %w", bucket, prefix, err)
		}
		if attrs.Prefix == "" {
			files = append(files, attrs.Name)
		} else {
			dirs = append(dirs, attrs.Prefix)
		}
	}
	return dirs, files, nil
}

func (c *Client) gcsListFiles(ctx context.Context, bucket, prefix string) (files []string, err error) {
	klog.V(4).Infof("Listing recursively gs://%s/%s...", bucket, prefix)

	bkt := c.gcsClient.Bucket(bucket)
	it := bkt.Objects(ctx, &storage.Query{
		Prefix: prefix,
	})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list all objects in gs://%s/%s: %w", bucket, prefix, err)
		}
		files = append(files, attrs.Name)
	}
	return files, nil
}

func (c *Client) gcsOpen(ctx context.Context, bucket string, object string) (io.ReadCloser, error) {
	// object may have /, so this code won't work on Windows
	path := fmt.Sprintf("%s/%s/%s", c.cacheDir, bucket, object)

	f, err := os.Open(path)
	if err == nil {
		klog.V(4).Infof("Found gs://%s/%s in cache", bucket, object)
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	klog.V(4).Infof("Downloading gs://%s/%s...", bucket, object)

	err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		return nil, err
	}

	bkt := c.gcsClient.Bucket(bucket)
	r, err := bkt.Object(object).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open gs://%s/%s: %w", bucket, object, err)
	}
	defer r.Close()

	f, err = os.Create(path + ".part")
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(f, r)
	if err != nil {
		// Best effort cleanup
		_ = f.Close()
		_ = os.Remove(path + ".part")
		return nil, fmt.Errorf("failed to cache gs://%s/%s: %w", bucket, object, err)
	}

	err = f.Close()
	if err != nil {
		return nil, err
	}

	err = os.Rename(path+".part", path)
	if err != nil {
		return nil, err
	}

	return os.Open(path)
}

func (c *Client) FindBuilds(ctx context.Context, name, gcsBucketPrefix string) ([]*Build, error) {
	if !strings.HasSuffix(gcsBucketPrefix, "/") {
		gcsBucketPrefix += "/"
	}
	parts := strings.SplitN(gcsBucketPrefix, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid gcs prefix for %s: %s", name, gcsBucketPrefix)
	}
	bucket, prefix := parts[0], parts[1]

	klog.V(2).Infof("Searching for %s builds (gs://%s/%s)...", name, bucket, prefix)

	var builds []*Build
	dirs, _, err := c.gcsListDir(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if len(dir) <= len(prefix)+1 {
			panic(fmt.Errorf("unexpected object from gcs: object is expected to have prefix %q, got %q", prefix, dir))
		}
		buildID := dir[len(prefix) : len(dir)-1]
		build := &Build{
			Job:       name,
			BuildID:   buildID,
			GCSBucket: bucket,
			GCSPrefix: dir,
		}
		builds = append(builds, build)
	}
	return builds, nil
}

func (c *Client) GetBuildFiles(ctx context.Context, build *Build) (*BuildFiles, error) {
	files, err := c.gcsListFiles(ctx, build.GCSBucket, build.GCSPrefix)
	if err != nil {
		return nil, err
	}

	m := make(map[string]struct{})
	for _, f := range files {
		m[f] = struct{}{}
	}

	return &BuildFiles{
		Build: build,
		Files: m,
	}, nil
}

func (c *Client) GetStartedJson(ctx context.Context, build *Build) (StartedJson, error) {
	var j StartedJson
	f, err := c.gcsOpen(ctx, build.GCSBucket, build.GCSPrefix+"started.json")
	if err != nil {
		return j, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&j)
	if err != nil {
		return j, InvalidJSONError{
			msg: fmt.Sprintf("unable to decode gs://%s/%s", build.GCSBucket, build.GCSPrefix+"started.json"),
			err: err,
		}
	}
	return j, nil
}

func (c *Client) GetFinishedJson(ctx context.Context, build *Build) (FinishedJson, error) {
	var j FinishedJson
	f, err := c.gcsOpen(ctx, build.GCSBucket, build.GCSPrefix+"finished.json")
	if err != nil {
		return j, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&j)
	if err != nil {
		return j, InvalidJSONError{
			msg: fmt.Sprintf("unable to decode gs://%s/%s", build.GCSBucket, build.GCSPrefix+"finished.json"),
			err: err,
		}
	}
	return j, nil
}

func analyzeSuite(suite junit.Suite) []*TestResult {
	var results []*TestResult
	for _, result := range suite.Results {
		var output string
		summary := result.Message(1 << 20) // 1 MiB
		if result.Output != nil {
			output = *result.Output
			if !utf8.ValidString(output) {
				output = fmt.Sprintf("invalid utf8: %s", strings.ToValidUTF8(output, "?"))
			}
		} else {
			output = summary
		}

		status := TestStatusSuccess
		if result.Failure != nil {
			status = TestStatusFailure
		} else if result.Error != nil {
			status = TestStatusError
		} else if result.Skipped != nil {
			status = TestStatusSkipped
		}

		results = append(results, &TestResult{
			Test:    result.Name,
			Status:  status,
			Output:  output,
			Summary: summary,
		})
	}
	return results
}

func analyzeSuites(suites []junit.Suite) []*TestResult {
	var results []*TestResult
	for _, suite := range suites {
		results = append(results, analyzeSuite(suite)...)
	}
	return results
}

func (c *Client) GetTestResults(ctx context.Context, buildFiles *BuildFiles) ([]*TestResult, error) {
	var results []*TestResult
	for objectName := range buildFiles.Files {
		if junitObject.MatchString(objectName) {
			f, err := c.gcsOpen(ctx, buildFiles.Build.GCSBucket, objectName)
			if err != nil {
				return results, err
			}
			suites, err := junit.ParseStream(f)
			if err != nil {
				return results, fmt.Errorf("unable to parse gs://%s/%s: %w", buildFiles.Build.GCSBucket, objectName, err)
			}
			testResults := analyzeSuites(suites.Suites)
			f.Close()
			results = append(results, testResults...)
		}
	}
	return results, nil
}
