package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

type config struct {
	sshControlPath      string
	gitLocalPath        string
	gitRemotePath       string
	rsyncLocalPath      string
	rsyncRemotePath     string
	watchmanLocalPath   string
	fsmonitorLocalPath  string
	gitSyncExcludePaths []string
	remoteURL           string
	useTarSync          bool
	useFsNotify         bool
	remoteName          string
}

func (cfg config) remoteSSHAddr() string {
	return strings.Split(cfg.remoteURL, ":")[0]
}

func (cfg config) remoteDir() string {
	return strings.Split(cfg.remoteURL, ":")[1]
}

var defaultConfig = config{
	sshControlPath: "/tmp/ssh_mux_%h_%p_%r",
	rsyncLocalPath: "/usr/local/bin/rsync",
	remoteName:     "sync",
}

type gitWorkDir struct {
	dir string
}

func handleCmdError(err error) {
	if exitErr, ok := err.(*exec.ExitError); ok {
		fmt.Fprintln(os.Stderr, "  cmd stderr:", string(exitErr.Stderr))
	}
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
	return env
}

func (wd *gitWorkDir) makeCmd(args ...string) *exec.Cmd {
	gitArgs := []string{"-C", wd.dir}
	gitArgs = append(gitArgs, args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Env = getRestrictedEnv()
	return cmd
}

func readConfigFromGit() (*config, error) {
	wd := gitWorkDir{"."}
	gitCmd := wd.makeCmd("config", "--get-regexp", "^sync\\.")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return nil, errors.WithMessage(err, "git config failed")
	}
	lines := strings.Split(string(output), "\n")
	configMap := make(map[string]string)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		keyValTuple := strings.SplitN(line, " ", 1)
		configMap[keyValTuple[0]] = keyValTuple[1]
	}

	cfg := defaultConfig
	cfg.remoteName = configMap["remote_name"]

	gitCmd = wd.makeCmd("remote", "get-url", cfg.remoteName)
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		return nil, errors.WithMessage(err, "git remote failed")
	}
	cfg.remoteURL = string(bytes.TrimSpace(output))
	return &cfg, nil
}
