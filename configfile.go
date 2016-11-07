package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/net/context/ctxhttp"

	yaml "gopkg.in/yaml.v2"
)

const (
	defaultFetchTimeout = 30 * time.Second
)

type configFile struct {
	sync.RWMutex

	Files        map[string]*configFileSource `yaml:"files"`
	CommandShell []string                     `yaml:"command_shell"`
}

type configFileSource struct {
	BasicAuth      string        `yaml:"basic_auth"`
	SuccessCommand string        `yaml:"success_command"`
	Timeout        time.Duration `yaml:"timeout"`
	FetchInterval  time.Duration `yaml:"fetch_interval"`
	IgnoreETag     bool          `yaml:"ignore_etag"`
	SHA256         string        `yaml:"sha256"`
	URL            string        `yaml:"url"`

	lastCall     time.Time
	lastSeenETag string
	inProgress   time.Time
}

func (c *configFileSource) Lock() {
	c.inProgress = time.Now()
}

func (c *configFileSource) Unlock() {
	c.inProgress = time.Time{}
}

func (c configFileSource) IsLocked() bool {
	return c.inProgress.Add(c.Timeout).After(time.Now())
}

func (c configFileSource) Equals(in *configFileSource) bool {
	return c.Timeout == in.Timeout &&
		c.FetchInterval == in.FetchInterval &&
		c.IgnoreETag == in.IgnoreETag &&
		c.SHA256 == in.SHA256 &&
		c.URL == in.URL
}

func (c *configFileSource) Finish(eTag string) {
	c.lastCall = time.Now()
	c.lastSeenETag = eTag
	c.Unlock()
}

func loadConfigFile(filePath string) (*configFile, error) {
	raw, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	res := &configFile{}
	if err := yaml.Unmarshal(raw, res); err != nil {
		return nil, err
	}

	return res, nil
}

func (c *configFile) Patch(in *configFile) error {
	for _, k := range excessKeys(c.Files, in.Files) {
		delete(c.Files, k)
	}

	for _, k := range excessKeys(in.Files, c.Files) {
		c.Files[k] = in.Files[k]
	}

	for k := range c.Files {
		if !c.Files[k].Equals(in.Files[k]) {
			c.Files[k] = in.Files[k]
		}
	}

	return nil
}

func excessKeys(a, b map[string]*configFileSource) (excess []string) {
	for aKey := range a {
		found := false
		for bKey := range b {
			if aKey == bKey {
				found = true
			}
		}
		if !found {
			excess = append(excess, aKey)
		}
	}

	return
}

func (c configFile) WaitNextExecution() <-chan time.Time {
	res := make(chan time.Time)

	go func() {
		for {
			sleep := 720 * time.Hour

			c.RLock()
			for _, v := range c.Files {
				if w := v.lastCall.Add(v.FetchInterval).Sub(time.Now()); w < sleep {
					sleep = w
				}
			}
			c.RUnlock()

			if sleep < 0 {
				sleep = 100 * time.Millisecond
			}

			debug("Sleeping for %s until next event (wakeup at %s)...", sleep, time.Now().Add(sleep))
			res <- <-time.After(sleep)
		}
	}()

	return res
}

func (c *configFile) ExecuteExpired() error {
	c.RLock()
	defer c.RUnlock()

	for filePath, fc := range c.Files {
		if fc.lastCall.Add(fc.FetchInterval).After(time.Now()) || fc.IsLocked() {
			continue
		}

		fc.Lock()

		go func(filePath string) {
			debug("Starting fetch of file '%s'", filePath)
			if err := c.executeDownload(filePath); err != nil {
				log.Printf("Could not fetch file '%s': %s", filePath, err)
				return
			}
			debug("File '%s' successfully fetched", filePath)
		}(filePath)
	}

	return nil
}

func (c *configFile) executeDownload(targetPath string) error {
	c.RLock()
	targetConfig := c.Files[targetPath]
	c.RUnlock()

	if targetConfig.SHA256 != "" {
		currentSHA, ok := calculateFileSha256(targetPath)
		if ok && currentSHA == targetConfig.SHA256 {
			return nil
		}
	}

	timeout := targetConfig.Timeout
	if timeout == 0 {
		timeout = defaultFetchTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequest("GET", targetConfig.URL, nil)
	if err != nil {
		return err
	}

	if targetConfig.BasicAuth != "" {
		ba := strings.SplitN(targetConfig.BasicAuth, ":", 2)
		if len(ba) != 2 {
			return errors.New("Invalid auth configuration, needs format user:pass")
		}
		req.SetBasicAuth(ba[0], ba[1])
	}

	if !targetConfig.IgnoreETag && targetConfig.lastSeenETag != "" {
		req.Header.Set("If-None-Match", targetConfig.lastSeenETag)
	}

	res, err := ctxhttp.Do(ctx, http.DefaultClient, req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode >= 400:
		return fmt.Errorf("Got error status code %d", res.StatusCode)
	case res.StatusCode == 304:
		c.Files[targetPath].Finish(c.Files[targetPath].lastSeenETag)
		return nil
	case res.StatusCode == 200:
		// Exclude from default, handle later
	default:
		return fmt.Errorf("Got unexpected status code %d", res.StatusCode)
	}

	if err := os.MkdirAll(path.Dir(targetPath), 0755); err != nil {
		return err
	}

	t, err := ioutil.TempFile(path.Dir(targetPath), path.Base(targetPath))
	if err != nil {
		return err
	}

	if _, err := io.Copy(t, res.Body); err != nil {
		return err
	}

	if targetConfig.SHA256 != "" {
		if newSha, ok := calculateFileSha256(targetPath); !ok || newSha != targetConfig.SHA256 {
			return errors.New("Downloaded file does not have expected SHA256")
		}
	}

	if err := os.Rename(t.Name(), targetPath); err != nil {
		return err
	}

	c.Files[targetPath].Finish(res.Header.Get("ETag"))

	go func(targetPath string) {
		if err := c.executeSuccessCommand(targetPath); err != nil {
			log.Printf("Could not execute success-command for '%s': %s", targetPath, err)
		}
	}(targetPath)

	return nil
}

func (c *configFile) executeSuccessCommand(targetPath string) error {
	c.RLock()
	defer c.RUnlock()

	if c.Files[targetPath].SuccessCommand == "" {
		return nil
	}

	cmd := exec.Command(c.CommandShell[0], append(c.CommandShell, c.Files[targetPath].SuccessCommand)[1:]...)
	return cmd.Run()
}
