package kvcache

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

const tmpSuffix = ".part"

type ErrNotFound struct {
	Key string
}

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("key %s not found", e.Key)
}

func IsNotFound(err error) bool {
	var e ErrNotFound
	return errors.As(err, &e)
}

type KVCache struct {
	dir string
}

func NewDefaultKVCache() *KVCache {
	return &KVCache{
		dir: "./cache/builds",
	}
}

func (c *KVCache) pathFor(key string) string {
	// key may have /, so this code won't work on Windows
	return fmt.Sprintf("%s/%s", c.dir, key)
}

func (c *KVCache) Save(key string, value interface{}) error {
	klog.V(4).Infof("Saving %s in cache...", key)

	if strings.HasSuffix(key, tmpSuffix) {
		return fmt.Errorf("key should not end with %q: %s", tmpSuffix, key)
	}

	buf, err := json.Marshal(value)
	if err != nil {
		return err
	}

	path := c.pathFor(key)

	err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		return err
	}

	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}

	w := gzip.NewWriter(f)

	_, err = w.Write(buf)
	if err != nil {
		// Best effort cleanup
		_ = f.Close()
		_ = os.Remove(path + ".part")
		return fmt.Errorf("failed to save %s: %w", key, err)
	}

	err = w.Close()
	if err != nil {
		return err
	}

	err = f.Close()
	if err != nil {
		return err
	}

	err = os.Rename(path+".part", path)
	if err != nil {
		return err
	}

	return nil
}

func (c *KVCache) Load(key string, value interface{}) error {
	path := c.pathFor(key)

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return ErrNotFound{
			Key: key,
		}
	} else if err != nil {
		return err
	}
	defer f.Close()

	klog.V(4).Infof("Found %s in cache", key)

	r, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("unable to read %s: %w", key, err)
	}

	return json.NewDecoder(r).Decode(value)
}

func (c *KVCache) Delete(key string) error {
	path := c.pathFor(key)

	klog.V(4).Infof("Deleting %s...", path)
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}
