package cache

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

type FSCache struct {
	dir string
}

func NewDefaultFSCache() *FSCache {
	return &FSCache{
		dir: "./cache",
	}
}

func (c *FSCache) Open(key string, init func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	// key may have /, so this code won't work on Windows
	path := fmt.Sprintf("%s/%s", c.dir, key)

	f, err := os.Open(path)
	if err == nil {
		klog.V(4).Infof("Found %s in cache", key)
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		return nil, err
	}

	r, err := init()
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("failed to cache %s: %w", key, err)
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

func (c *FSCache) DeleteByPrefix(prefix string) error {
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("cannot delete %q: prefix is expected to end with /", prefix)
	}
	prefix = prefix[:len(prefix)-1]
	if prefix == "" {
		return fmt.Errorf("cannot delete %q: prefix cannot be empty", prefix)
	}

	path := fmt.Sprintf("%s/%s", c.dir, prefix)
	klog.V(4).Infof("Deleting %s...", path)
	return os.RemoveAll(path)
}
