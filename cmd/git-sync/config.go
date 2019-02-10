package main

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
)

type config struct {
	sshControlPath     string
	gitLocalPath       string
	gitRemotePath      string
	rsyncLocalPath     string
	rsyncRemotePath    string
	fsmonitorLocalPath string
	excludePaths       []string
	remoteName         string
	remoteURL          string
	gitConfig          map[string]string
}

func (cfg config) remoteSSHAddr() string {
	return strings.Split(cfg.remoteURL, ":")[0]
}

func (cfg config) remoteDir() string {
	return strings.Split(cfg.remoteURL, ":")[1]
}

func (cfg config) fsmonitorEnabled() bool {
	return cfg.fsmonitorLocalPath != ""
}

var defaultConfig = config{
	sshControlPath:  "/tmp/ssh_mux_%h_%p_%r",
	gitRemotePath:   "git",
	gitLocalPath:    "git",
	rsyncRemotePath: "rsync",
	rsyncLocalPath:  "/usr/local/bin/rsync", // Mac specific :(
	remoteName:      "sync",
}

func readConfigFromGit(remoteName string) (*config, error) {
	wd := gitWorkDir{}
	gitCmd := wd.gitCommand("config", "-z", "-l")
	output, err := gitCmd.Output()
	if err != nil {
		return nil, errors.WithMessage(err, "git config failed")
	}
	entries := splitNullTerminated(string(output))
	gitConfig := make(map[string]string)
	for _, ent := range entries {
		keyValTuple := strings.SplitN(ent, "\n", 2)
		if len(keyValTuple) != 2 {
			fmt.Println("invalid:", len(keyValTuple), keyValTuple)
		}
		gitConfig[keyValTuple[0]] = keyValTuple[1]
	}

	cfg := defaultConfig
	cfg.gitConfig = gitConfig

	if remoteName == "" {
		remoteName = gitConfig["sync.remoteName"]
	}
	if remoteName != "" {
		cfg.remoteName = remoteName
	}

	if excludePaths := gitConfig["sync.excludePaths"]; excludePaths != "" {
		cfg.excludePaths = strings.Split(strings.TrimSpace(excludePaths), ":")
	}

	if rpath := gitConfig["sync.rsyncRemotePath"]; rpath != "" {
		cfg.rsyncRemotePath = rpath
	}

	remoteURLKey := "remote." + cfg.remoteName + ".url"
	cfg.remoteURL = strings.TrimSpace(gitConfig[remoteURLKey])
	if cfg.remoteURL == "" {
		return nil, errors.Errorf("no url specified for remote name %q %#v", cfg.remoteName, gitConfig)
	}

	cfg.fsmonitorLocalPath = gitConfig["core.fsmonitor"]

	return &cfg, nil
}
