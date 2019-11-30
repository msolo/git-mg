package main

import (
	"strings"

	"github.com/msolo/git-mg/gitapi"
	"github.com/pkg/errors"
)

type config struct {
	// sshControlPath is used to explicitly set the control socket for our usage.
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
	// ssh -G <host> | awk '/^controlpath/{print $2}'
	sshControlPath:  "/tmp/ssh_mux_%h_%p_%r",
	gitRemotePath:   "git",
	gitLocalPath:    "git",
	rsyncRemotePath: "rsync",
	rsyncLocalPath:  "rsync", // Assume a satisfactory rsync is in the path.
	remoteName:      "sync",
}

func readConfigFromGit(remoteName string) (*config, error) {
	wd := gitapi.NewGitWorkdir()
	gitConfig, err := wd.GitConfig()
	if err != nil {
		return nil, err
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
