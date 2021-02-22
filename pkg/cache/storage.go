package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dmage/triage/pkg/types"
	_ "github.com/mattn/go-sqlite3"
	"k8s.io/klog/v2"
)

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

type Storage struct {
	db *sql.DB
}

func New() (*Storage, error) {
	db, err := sql.Open("sqlite3", "./cache/index.db")
	if err != nil {
		return nil, err
	}

	s := &Storage{
		db: db,
	}

	err = s.init()
	if err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, err
	}

	return s, nil
}

func (s *Storage) init() error {
	sqlStmt := `
	CREATE TABLE IF NOT EXISTS builds (
		job text,
		build_id text,
		started_at int,
		gcs_bucket text,
		gcs_prefix text
	);
	CREATE UNIQUE INDEX IF NOT EXISTS builds_idx ON builds (job, build_id);

	CREATE TABLE IF NOT EXISTS build_files (
		job text,
		build_id text,
		created_at int,
		files text
	);
	CREATE UNIQUE INDEX IF NOT EXISTS build_files_idx ON build_files (job, build_id);
	`
	_, err := s.db.Exec(sqlStmt)
	if err != nil {
		return fmt.Errorf("%w: %s", err, sqlStmt)
	}
	return nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}

func (s *Storage) LoadBuild(job, buildID string) (*types.Build, int64, error) {
	klog.V(5).Infof("Loading build %s @ %s from storage...", job, buildID)

	build := &types.Build{
		Job:     job,
		BuildID: buildID,
	}
	var startedAt int64
	err := s.db.QueryRow(
		"SELECT started_at, gcs_bucket, gcs_prefix FROM builds WHERE job = ? AND build_id = ?",
		job, buildID,
	).Scan(&startedAt, &build.GCSBucket, &build.GCSPrefix)
	if err != nil {
		return nil, 0, err
	}

	return build, startedAt, err
}

func (s *Storage) SaveBuild(build *types.Build, startedAt int64) error {
	klog.V(5).Infof("Saving build %s...", build)

	_, err := s.db.Exec(
		"INSERT INTO builds (job, build_id, started_at, gcs_bucket, gcs_prefix) VALUES (?, ?, ?, ?, ?)",
		build.Job, build.BuildID, startedAt, build.GCSBucket, build.GCSPrefix,
	)
	return err
}

func (s *Storage) FindBuilds(startedAt int64) ([]types.Build, error) {
	klog.V(5).Infof("Loading builds from storage...")

	var builds []types.Build
	rows, err := s.db.Query(
		"SELECT job, build_id, gcs_bucket, gcs_prefix FROM builds WHERE started_at >= ?",
		startedAt,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var build types.Build
		if err := rows.Scan(&build.Job, &build.BuildID, &build.GCSBucket, &build.GCSPrefix); err != nil {
			return builds, err
		}
		builds = append(builds, build)
	}

	return builds, nil
}

func (s *Storage) FindOldBuilds(startedAt int64) ([]types.Build, error) {
	klog.V(5).Infof("Loading old builds from storage...")

	var builds []types.Build
	rows, err := s.db.Query(
		"SELECT job, build_id, gcs_bucket, gcs_prefix FROM builds WHERE started_at < ?",
		startedAt,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var build types.Build
		if err := rows.Scan(&build.Job, &build.BuildID, &build.GCSBucket, &build.GCSPrefix); err != nil {
			return builds, err
		}
		builds = append(builds, build)
	}

	return builds, nil
}

func (s *Storage) DeleteBuild(job, buildID string) error {
	klog.V(5).Infof("Deleting build %s @ %s...", job, buildID)

	_, err := s.db.Exec(
		"DELETE FROM builds WHERE job = ? AND build_id = ?",
		job, buildID,
	)
	return err
}

func (s *Storage) LoadBuildFiles(build *types.Build) (*types.BuildFiles, error) {
	klog.V(5).Infof("Loading build files for %s @ %s from storage...", build.Job, build.BuildID)

	var filesBuf []byte
	err := s.db.QueryRow(
		"SELECT  files FROM build_files WHERE job = ? AND build_id = ?",
		build.Job, build.BuildID,
	).Scan(&filesBuf)
	if err != nil {
		return nil, err
	}

	buildFiles := &types.BuildFiles{
		Build: build,
	}
	err = json.Unmarshal(filesBuf, &buildFiles.Files)
	return buildFiles, err
}

func (s *Storage) SaveBuildFiles(buildFiles *types.BuildFiles) error {
	klog.V(5).Infof("Saving build files for %s @ %s...", buildFiles.Build.Job, buildFiles.Build.BuildID)

	filesBuf, err := json.Marshal(buildFiles.Files)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		"INSERT INTO build_files (job, build_id, created_at, files) VALUES (?, ?, strftime('%s', 'now'), ?)",
		buildFiles.Build.Job, buildFiles.Build.BuildID, filesBuf,
	)
	return err
}

func (s *Storage) DeleteBuildFiles(build *types.Build) error {
	klog.V(5).Infof("Deleting build files %s @ %s...", build.Job, build.BuildID)

	_, err := s.db.Exec(
		"DELETE FROM build_files WHERE job = ? AND build_id = ?",
		build.Job, build.BuildID,
	)
	return err
}
