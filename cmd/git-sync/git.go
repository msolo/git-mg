package main

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"

	log "github.com/msolo/go-bis/glug"
)

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

func (wd *gitWorkDir) gitCommand(args ...string) *Cmd {
	gitArgs := []string{}
	if wd.dir != "" {
		gitArgs = append(gitArgs, "-C", wd.dir)
	}
	gitArgs = append(gitArgs, args...)
	cmd := Command("git", gitArgs...)
	cmd.Stderr = os.Stderr
	cmd.Env = getRestrictedEnv()
	return cmd
}

func getGitWorkdir() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err) // This is fatal.
	}
	for wd != "/" {
		_, err := os.Stat(path.Join(wd, ".git"))
		if err == nil {
			return wd
		} else if os.IsNotExist(err) {
			wd = path.Dir(wd)
		} else {
			panic(err) // This is also fatal.
		}
	}
	return ""
}

func getMergeBaseCommitHash(workdir string) (string, error) {
	gwd := gitWorkDir{workdir}
	gitCmd := gwd.gitCommand("merge-base", "origin/master", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

func getHeadCommitHash(workdir string) (string, error) {
	gwd := gitWorkDir{workdir}
	gitCmd := gwd.gitCommand("rev-parse", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

func parsePorcelainStatus(data []byte) (modifiedFiles []string, untrackedFiles []string, renamedFiles []string, unstagedFiles []string, err error) {
	entries := splitNullTerminated(string(data))
	modifiedFiles = make([]string, 0, 16)
	unstagedFiles = make([]string, 0, 16)
	untrackedFiles = make([]string, 0, 16)
	renamedFiles = make([]string, 0, 16)
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		status, fname := entry[:2], entry[3:]
		if status == "UU" {
			// Ignore merge conflicts. They have to be resolved by hand
			// anyway, which will require another sync.
			log.Warningf("ignoring unmerged file: %s", fname)
			continue
		}

		modifiedFiles = append(modifiedFiles, fname)
		if status[0] == 'R' {
			// Rename is encoded strangely in null-terminated mode:
			// R  twinsies -> twinsies-2
			// R  twinsies-2\0twinsies\0
			i++
			renamedFile := entries[i]
			modifiedFiles = append(modifiedFiles, renamedFile)
			renamedFiles = append(renamedFiles, renamedFile)
		} else if status == "??" {
			untrackedFiles = append(untrackedFiles, fname)
		} else if status[1] != ' ' {
			unstagedFiles = append(unstagedFiles, fname)
		}
	}
	return modifiedFiles, untrackedFiles, renamedFiles, unstagedFiles, nil
}

func getGitStatus(workdir string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("status", "-z", "--porcelain", "--untracked-files=all")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles, _, _, _, err = parsePorcelainStatus(stdout)
	return changedFiles, err
}

// Return all files that have been changed on HEAD relative to the merge base.
func getGitDiffChanges(workdir string, mergeBaseHash string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("diff", "-z", "--no-renames", "--name-only", "HEAD", mergeBaseHash)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = splitNullTerminated(string(stdout))
	return changedFiles, nil
}

// Return a list of ignored files.
func gitCheckIgnore(workdir string, filePaths []string) ([]string, error) {
	data := joinNullTerminated(filePaths)
	// NOTE: --no-index makes this call ~5ms instead of 150ms, but we have
	// false positives due to what we store in the tree.
	gwd := gitWorkDir{workdir}
	cmd := gwd.gitCommand("check-ignore", "-z", "--stdin", "--no-index")
	cmd.Stdin = bytes.NewReader([]byte(data))
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			switch exitErr.ProcessState.Sys().(syscall.WaitStatus).ExitStatus() {
			case 0, 1:
			default:
				return nil, err
			}
		}
	}
	return splitNullTerminated(string(out)), nil
}

// Return a list of files that were renamed.
func gitRenamedFiles(workdir string, filePaths []string) ([]string, error) {
	gwd := &gitWorkDir{workdir}
	args := []string{"status", "-z", "--porcelain", "--untracked-files=normal"}
	args = append(args, filePaths...)
	cmd := gwd.gitCommand(args...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	_, _, renamedFiles, _, err := parsePorcelainStatus(stdout)
	return renamedFiles, err
}

func getGitRemoteNames(workdir string) (remoteNames []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("remote")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(stdout)), nil
}
