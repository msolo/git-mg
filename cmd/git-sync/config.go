package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/pkg/errors"
)

type config struct {
	sshControlPath     string
	gitLocalPath       string
	gitRemotePath      string
	rsyncLocalPath     string
	rsyncRemotePath    string
	watchmanLocalPath  string
	fsmonitorLocalPath string
	excludePaths       []string
	remoteURL          string
	useTarSync         bool
	useFsNotify        bool
	remoteName         string
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

type gitWorkDir struct {
	dir string
}

func getRestrictedEnv() []string {
	// Any missing envionment variable is a problem.
	keys := []string{"PATH", "USER", "LOGNAME", "HOME", "SSH_AUTH_SOCK"}
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if val := os.Getenv(key); val == "" {
			panic("invalid env, missing key: " + key)
		} else {
			env = append(env, key+"="+val)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_TRACE") {
			env = append(env, kv)
		}
	}

	return env
}

func (wd *gitWorkDir) makeCmd(args ...string) *Cmd {
	gitArgs := []string{"-C", wd.dir}
	gitArgs = append(gitArgs, args...)
	cmd := Command("git", gitArgs...)
	cmd.Env = getRestrictedEnv()
	return cmd
}

func readConfigFromGit() (*config, error) {
	wd := gitWorkDir{"."}
	gitCmd := wd.makeCmd("config", "-z", "-l")
	output, err := gitCmd.Output()
	if err != nil {
		return nil, errors.WithMessage(err, "git config failed")
	}
	entries := splitNullTerminated(string(output))
	configMap := make(map[string]string)
	for _, ent := range entries {
		keyValTuple := strings.SplitN(ent, "\n", 2)
		if len(keyValTuple) != 2 {
			fmt.Println("invalid:", len(keyValTuple), keyValTuple)
		}
		configMap[keyValTuple[0]] = keyValTuple[1]
	}

	cfg := defaultConfig
	if remoteName := configMap["sync.remote_name"]; remoteName != "" {
		cfg.remoteName = remoteName
	}

	if excludePaths := configMap["sync.exclude_paths"]; excludePaths != "" {
		cfg.excludePaths = strings.Split(strings.TrimSpace(excludePaths), ":")
	}

	remoteURLKey := "remote." + cfg.remoteName + ".url"
	cfg.remoteURL = strings.TrimSpace(configMap[remoteURLKey])
	if cfg.remoteURL == "" {
		return nil, errors.Errorf("no url specified for remote name %q", cfg.remoteName)
	}
	cfg.fsmonitorLocalPath = configMap["core.fsmonitor"]
	return &cfg, nil
}
