package gitapi

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"

	log "github.com/msolo/go-bis/glug"
	"github.com/pkg/errors"
)

func GitWorkdir() string {
	//	args := []string{"git", "rev-parse", "--show-toplevel"}
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

type gitWorkDir struct {
	dir string
}

func NewGitWorkdir() *gitWorkDir {
	return &gitWorkDir{GitWorkdir()}
}

type GitConfig interface {
	Get(key string) string
}

type gitConfig map[string]string

// Normalize git config keys based on the `man git-config` - subsections are case-sensitive.
func (gc gitConfig) Get(key string) string {
	kf := strings.Split(key, ".")
	if len(kf) == 3 {
		kf[0] = strings.ToLower(kf[0])
		kf[2] = strings.ToLower(kf[2])
		key = strings.Join(kf, ".")
	} else {
		key = strings.ToLower(key)
	}
	return gc[key]
}

func (wd *gitWorkDir) GitConfig() (GitConfig, error) {
	gitCmd := wd.gitCommand("config", "-z", "-l")
	output, err := gitCmd.Output()
	if err != nil {
		return nil, errors.WithMessage(err, "git config failed")
	}
	entries := SplitNullTerminated(string(output))
	cfg := gitConfig(make(map[string]string))
	for _, ent := range entries {
		keyValTuple := strings.SplitN(ent, "\n", 2)
		if len(keyValTuple) != 2 {
			log.Warningf("invalid git config tuple: %d %v", len(keyValTuple), keyValTuple)
		}
		cfg[keyValTuple[0]] = keyValTuple[1]
	}
	return cfg, nil
}

func GetRestrictedEnv() []string {
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
	cmd.Env = GetRestrictedEnv()
	return cmd
}

func GetMergeBaseCommitHash(workdir string) (string, error) {
	gwd := gitWorkDir{workdir}
	gitCmd := gwd.gitCommand("merge-base", "origin/master", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

func GetHeadCommitHash(workdir string) (string, error) {
	gwd := gitWorkDir{workdir}
	gitCmd := gwd.gitCommand("rev-parse", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

func ParsePorcelainStatus(data []byte) (modifiedFiles []string, untrackedFiles []string, renamedFiles []string, unstagedFiles []string, err error) {
	entries := SplitNullTerminated(string(data))
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

func GetGitStatus(workdir string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("status", "-z", "--porcelain", "--untracked-files=all")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles, _, _, _, err = ParsePorcelainStatus(stdout)
	return changedFiles, err
}

// Return all files that were changed in a given commit.
func GetGitCommitChanges(workdir string, commitHash string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("diff-tree", "--no-commit-id", "-z", "-r", "--name-only", commitHash)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = SplitNullTerminated(string(stdout))
	return changedFiles, nil
}

// Return all files that have been changed on HEAD relative to the merge base.
func GetGitDiffChanges(workdir string, mergeBaseHash string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("diff", "-z", "--no-renames", "--name-only", "HEAD", mergeBaseHash)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = SplitNullTerminated(string(stdout))
	return changedFiles, nil
}

func GetGitStagedChanges(workdir string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("diff", "-z", "--no-renames", "--name-only", "--staged")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = SplitNullTerminated(string(stdout))
	return changedFiles, nil
}

func GetGitUnstagedChanges(workdir string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("diff", "-z", "--no-renames", "--name-only")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = SplitNullTerminated(string(stdout))
	return changedFiles, nil
}

// Return a list of ignored files.
func GitCheckIgnore(workdir string, filePaths []string) ([]string, error) {
	data := JoinNullTerminated(filePaths)
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
	return SplitNullTerminated(string(out)), nil
}

// Return a list of files that were renamed.
func GitRenamedFiles(workdir string, filePaths []string) ([]string, error) {
	gwd := &gitWorkDir{workdir}
	args := []string{"status", "-z", "--porcelain", "--untracked-files=normal"}
	args = append(args, filePaths...)
	cmd := gwd.gitCommand(args...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	_, _, renamedFiles, _, err := ParsePorcelainStatus(stdout)
	return renamedFiles, err
}

func GetGitRemoteNames(workdir string) (remoteNames []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.gitCommand("remote")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(stdout)), nil
}

func JoinNullTerminated(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return strings.Join(ss, "\000") + "\000"
}

func SplitNullTerminated(s string) []string {
	if s == "" {
		return nil
	}
	// Deal with buggy interpretations of null-terminated
	if s[len(s)-1] == '\000' {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\000")
}
