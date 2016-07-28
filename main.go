package main

import (
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Luzifer/rconfig"
)

var (
	cfg = struct {
		ConfigFile     string `flag:"config-file,f" default:"files.yaml" description:"Configuration file"`
		Verbose        bool   `flag:"verbose,v" default:"false" description:"Show more debug output"`
		VersionAndExit bool   `flag:"version" default:"false" description:"Prints current version and exits"`
	}{}

	downloadConfig = &configFile{
		CommandShell: []string{"/bin/bash", "-c"},
		Files:        make(map[string]*configFileSource),
	}

	version = "dev"
)

func debug(format string, args ...interface{}) {
	if cfg.Verbose {
		log.Printf(format, args...)
	}
}

func init() {
	if err := rconfig.Parse(&cfg); err != nil {
		log.Fatalf("Unable to parse commandline options: %s", err)
	}

	if cfg.VersionAndExit {
		fmt.Printf("download-watch %s\n", version)
		os.Exit(0)
	}
}

func reloadConfig() error {
	debug("Reloading configuration")
	c, err := loadConfigFile(cfg.ConfigFile)
	if err != nil {
		return err
	}

	downloadConfig.Lock()
	defer downloadConfig.Unlock()

	return downloadConfig.Patch(c)
}

func main() {
	if err := reloadConfig(); err != nil {
		log.Fatalf("Initial load of config failed: %s", err)
	}

	hupChan := make(chan os.Signal)
	signal.Notify(hupChan, syscall.SIGHUP)

	for {
		select {
		case <-downloadConfig.WaitNextExecution():
			downloadConfig.ExecuteExpired()
		case <-hupChan:
			if err := reloadConfig(); err != nil {
				log.Printf("Reload of config failed: %s", err)
			}
		}
	}
}

func calculateFileSha256(filePath string) (string, bool) {
	if _, err := os.Stat(filePath); err != nil {
		return "", false
	}

	raw, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", false
	}

	return fmt.Sprintf("%x", sha256.Sum256(raw)), true
}
