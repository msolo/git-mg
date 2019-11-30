package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	isatty "github.com/mattn/go-isatty"
	"github.com/msolo/git-mg/gitapi"
	"github.com/msolo/go-bis/flock"
	log "github.com/msolo/go-bis/glug"
	"github.com/tebeka/atexit"
)

func makeSSHArgs(cfg *config, addr string, bashCmdArgs []string) []string {
	sshOptions := map[string]string{
		"ConnectTimeout": "5",
		"ControlMaster":  "auto", // auto|no
		"ControlPath":    cfg.sshControlPath,
		"ControlPersist": "15m",
		// Forwarding the agent is almost certainly required since both local and remote
		// workdirs must talk to the same central server.
		"ForwardAgent": "yes",
		// If we use control sockets, we need to set loglevel=quiet so that we don"t
		// litter lines like "Shared connection to msolo-dbx closed." after every command.
		"LogLevel":              "quiet",
		"ServerAliveInterval":   "60",
		"StrictHostKeyChecking": "no",
		"TCPKeepAlive":          "yes",
		"UserKnownHostsFile":    "/dev/null",
	}

	sshArgs := []string{"-F", "/dev/null"}
	if os.Getenv("GIT_SYNC_DEBUG") != "" {
		sshArgs = append(sshArgs, "-vvv")
	}
	if isatty.IsTerminal(os.Stdout.Fd()) && len(bashCmdArgs) > 0 {
		// Force a TTY if we already have one and we are executing a command.
		sshArgs = append(sshArgs, "-t")
	}

	sshOptionsArgs := make([]string, 0, len(sshOptions))
	for k, v := range sshOptions {
		sshOptionsArgs = append(sshOptionsArgs, "-o"+k+"="+v)
	}
	sort.Strings(sshOptionsArgs)
	sshArgs = append(sshArgs, sshOptionsArgs...)

	if addr != "" {
		sshArgs = append(sshArgs, addr)
	}

	if len(bashCmdArgs) > 0 {
		bashCmd := "/bin/bash --noprofile --norc -c " + gitapi.BashQuote(strings.Join(bashCmdArgs, " "))
		sshArgs = append(sshArgs, bashCmd)
	}
	return sshArgs
}

func makeSSHCmd(cfg *config, addr string, bashCmdArgs []string) *gitapi.Cmd {
	cmd := gitapi.Command("ssh", makeSSHArgs(cfg, addr, bashCmdArgs)...)
	cmd.Env = gitapi.GetRestrictedEnv()
	return cmd
}

type syncCookie struct {
	LastHeadHash      string
	LastMergeBaseHash string
	LastSyncStartNs   int64 `json:",string"`
	headHash          string
	mergeBaseHash     string
	syncStartNs       int64
}

func (sc syncCookie) gitStateChanged() bool {
	return !(sc.LastHeadHash != "" && sc.LastHeadHash == sc.headHash && sc.LastMergeBaseHash == sc.mergeBaseHash)
}

// Read sync cookie and current working directory state. Cookie may be a stupid name.
func readSyncCookie(workdir string) (sc *syncCookie, err error) {
	headHash, err := gitapi.GetHeadCommitHash(workdir)
	if err != nil {
		return nil, err
	}
	mergeBaseHash, err := gitapi.GetMergeBaseCommitHash(workdir)
	if err != nil {
		return nil, err
	}
	sc = &syncCookie{
		// Round down to seconds since that's what watchman uses internally.
		syncStartNs:   time.Now().Unix() * 1e9,
		headHash:      headHash,
		mergeBaseHash: mergeBaseHash,
	}
	fname := path.Join(workdir, ".git/git-sync-cookie.json")
	data, err := ioutil.ReadFile(fname)
	if err == nil {
		if err := json.Unmarshal(data, sc); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return sc, nil
}

func writeSyncCookie(workdir string, sc *syncCookie) error {
	fname := path.Join(workdir, ".git/git-sync-cookie.json")
	tmpSc := &syncCookie{LastHeadHash: sc.headHash,
		LastMergeBaseHash: sc.mergeBaseHash,
		LastSyncStartNs:   sc.syncStartNs,
	}
	data, err := json.Marshal(tmpSc)
	if err != nil {
		return errors.Wrap(err, "failed marshaling sync cookie")
	}
	return ioutil.WriteFile(fname, data, 0644)
}

func isDir(fname string) bool {
	fi, err := os.Stat(fname)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

// Use file system notifications to find changed files rather than git.
func getChangesViaFsMonitor(cfg *config, workdir string, sc *syncCookie) (changedFiles []string, err error) {
	// To catch fast edits, we have to rewind one full second - the internal
	// granularity of watchman.  The API to git-fsmonitor-watchman falsely suggests
	// nanosecond granularity.
	ts := ((sc.LastSyncStartNs / 1e9) * 1e9) - 1e9
	if ts < 0 {
		ts = 0
	}

	// Watchman has some awful performance characteristics in the wild.  It's unclear
	// if this is watchman, fseventsd, CPU overload or what.  We can limit expected
	// worst-case behavior, but realistically there are some people for whom we
	// should just shut off watchman altogether.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fsMonCmd := gitapi.CommandContext(ctx, cfg.fsmonitorLocalPath, "1", strconv.FormatInt(ts, 10))
	fsMonCmd.Env = gitapi.GetRestrictedEnv()
	fsMonCmd.Dir = workdir
	out, err := fsMonCmd.Output()
	if err != nil {
		log.Warningf("git fsmonitor failed: %s", err)
		return nil, err
	}

	filePaths := gitapi.SplitNullTerminated(string(out))
	// Too many changes, just do a full sync by pretending we couldn't get
	// results.
	if len(filePaths) > 100 {
		log.Warningf("git fsmonitor returned too many changes: %d", len(filePaths))
		return nil, nil
	}

	// The crazy git protocol can return / to mean "everything might have
	// changed".
	if len(filePaths) == 1 && filePaths[0] == "/" {
		return nil, nil
	}

	// This filter is expensive because of the directory checking.
	filteredFileSet := make(map[string]bool, len(filePaths))
	for _, fname := range filePaths {
		if fname != "" && fname != ".git" && !strings.HasPrefix(fname, ".git/") && !isDir(path.Join(workdir, fname)) {
			filteredFileSet[fname] = true
		}
	}
	if len(filteredFileSet) > 0 {
		ignoredFilePaths, err := gitapi.GitCheckIgnore(workdir, stringSet2Slice(filteredFileSet))
		if err != nil {
			return nil, err
		}
		for _, fname := range ignoredFilePaths {
			delete(filteredFileSet, fname)
		}
	}
	return stringSet2Slice(filteredFileSet), nil
}

func stringSet2Slice(ss map[string]bool) []string {
	if len(ss) == 0 {
		return nil
	}
	sl := make([]string, 0, len(ss))
	for x := range ss {
		sl = append(sl, x)
	}
	return sl
}

func gitSyncCmd(cfg *config, sc *syncCookie) (*gitapi.Cmd, error) {
	bashCmdArgs := make([]string, 0, 16)
	// Plumb some handy profiling variables through.
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "GIT_TRACE") {
			bashCmdArgs = append(bashCmdArgs, "export "+env)
		}
	}

	excludePaths := make([]string, 0, len(cfg.excludePaths))
	for _, xp := range cfg.excludePaths {
		excludePaths = append(excludePaths, "--exclude="+xp)
	}

	cmdFmt := remoteGitCmdFmt{
		GitRemotePath:    cfg.gitRemotePath,
		CheckoutRequired: "1",
		CleanRequired:    "1",
		RemoteDir:        cfg.remoteDir(),
		CommitHash:       sc.mergeBaseHash,
		ExcludePaths:     strings.Join(excludePaths, " "),
	}
	if !sc.gitStateChanged() {
		cmdFmt.CheckoutRequired = "0"
		cmdFmt.CleanRequired = "0"
	}

	buf := bytes.NewBuffer(make([]byte, 0, 2048))
	tmpl := template.Must(template.New("remoteGitCmd").Parse(remoteGitCmd))
	tmpl.Option("missingkey=error")
	if err := tmpl.Execute(buf, cmdFmt); err != nil {
		return nil, err
	}
	bashCmdArgs = append(bashCmdArgs, buf.String())
	sshCmd := makeSSHCmd(cfg, cfg.remoteSSHAddr(), bashCmdArgs)
	return sshCmd, nil
}

func sshStageRemoteChangesCmd(cfg *config, changedFiles []string) (*gitapi.Cmd, error) {
	bashCmdArgs := make([]string, 0, 16)
	bashCmdArgs = append(bashCmdArgs, cfg.gitRemotePath, "-C", cfg.remoteDir(), "add", "$(")
	bashCmdArgs = append(bashCmdArgs, cfg.gitRemotePath, "-C", cfg.remoteDir(), "ls-files", "-c", "-o")
	bashCmdArgs = append(bashCmdArgs, changedFiles...)
	bashCmdArgs = append(bashCmdArgs, ")")
	sshCmd := makeSSHCmd(cfg, cfg.remoteSSHAddr(), bashCmdArgs)
	return sshCmd, nil
}

// Use git to find all files that have changed on top of the git merge base.
func getChangesViaStatus(workdir string, sc *syncCookie) (changedFiles []string, err error) {
	fileSet := make(map[string]bool)
	mu := &sync.Mutex{}
	updateSet := func(fnames []string) {
		mu.Lock()
		for _, fname := range fnames {
			fileSet[fname] = true
		}
		mu.Unlock()
	}

	x := func() error {
		files, err := gitapi.GetGitStatus(workdir)
		if err != nil {
			return err
		}
		updateSet(files)
		return nil
	}

	y := func() error {
		files, err := gitapi.GetGitDiffChanges(workdir, sc.mergeBaseHash)
		if err != nil {
			return err
		}
		updateSet(files)
		return nil
	}
	eg := &errgroup.Group{}
	eg.Go(x)
	eg.Go(y)
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	mu.Lock()
	changedFiles = stringSet2Slice(fileSet)
	mu.Unlock()

	sort.Strings(changedFiles)
	return changedFiles, nil
}

func remoteGitFetchCmd(cfg *config, workdir string) (*gitapi.Cmd, error) {
	shCmd := "flock --nonblock {{.RemoteDir}}/.git/FETCH_HEAD {{.GitRemotePath}} -C {{.RemoteDir}} fetch -q origin master < /dev/null > /dev/null 2>&1 &"
	tmpl := template.Must(template.New("remoteGitFetchCmd").Parse(shCmd)).Option("missingkey=error")
	shCmdFmt := struct {
		RemoteDir     string
		GitRemotePath string
	}{gitapi.BashQuote(cfg.remoteDir()), cfg.gitRemotePath}
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	if err := tmpl.Execute(buf, shCmdFmt); err != nil {
		return nil, err
	}
	cmd := makeSSHCmd(cfg, cfg.remoteSSHAddr(), []string{buf.String()})
	return cmd, nil
}

func topmostMissingDir(workdir string, fname string) (string, error) {
	dir := workdir
	names := strings.Split(strings.Trim(fname, "/"), "/")
	for _, name := range names {
		dir = path.Join(dir, name)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			// rsync wants relative paths.  We also append / to conform with
			// rsync's convention for directory names.
			relPath, err := filepath.Rel(workdir, dir)
			if err != nil {
				return "", err
			}
			return relPath + "/", nil
		}
	}
	// This is probably only possible during file system races where something
	// is modifying the directory structure while we are trying to sync it.
	panic("not missing components for path")
}

func tmpdir() string {
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return dir
}

func rsyncPushCmd(cfg *config, workdir string, filePaths []string) (*gitapi.Cmd, error) {
	// Replace file paths that are children of deleted directories with the top-most deleted
	// directory below the workdir.  It's not clear that this is always safe behavior for rsync,
	// but it should be safe for our use case.  This is related to an rsync bug, but the patch
	// attached to the report does not look correct.
	// See https://bugzilla.samba.org/show_bug.cgi?id=12569.
	sanitizedFileSet := make(map[string]bool)
	for _, fpath := range filePaths {
		if _, err := os.Stat(path.Join(workdir, fpath)); os.IsNotExist(err) {
			fpath, err = topmostMissingDir(workdir, fpath)
			if err != nil {
				return nil, err
			}
		}
		sanitizedFileSet[fpath] = true
	}
	sanitizedFilePaths := stringSet2Slice(sanitizedFileSet)
	sort.Strings(sanitizedFilePaths)

	tmpFile, err := ioutil.TempFile(tmpdir(), "git-sync-file-manifest-")
	if err != nil {
		return nil, err
	}
	atexit.Register(func() {
		_ = os.Remove(tmpFile.Name())
	})

	_, err = tmpFile.WriteString(gitapi.JoinNullTerminated(sanitizedFilePaths))
	if err != nil {
		return nil, err
	}

	sshArgs := []string{"ssh"}
	sshArgs = append(sshArgs, makeSSHArgs(cfg, "", nil)...)
	for i, arg := range sshArgs {
		sshArgs[i] = gitapi.BashQuote(arg)
	}
	rsyncCmdArgs := []string{
		"-czlptgo",
		"-e", strings.Join(sshArgs, " "),
		"--delete-missing-args",
		// Sanitized files can be non-empty directories on the remote side.
		"--force",
		"--from0",
		"--files-from", tmpFile.Name(),
	}
	if cfg.rsyncRemotePath != "" {
		rsyncCmdArgs = append(rsyncCmdArgs, "--rsync-path", cfg.rsyncRemotePath)
	}
	rsyncCmdArgs = append(rsyncCmdArgs, workdir, cfg.remoteURL)

	cmd := gitapi.Command(cfg.rsyncLocalPath, rsyncCmdArgs...)
	cmd.Env = gitapi.GetRestrictedEnv()
	return cmd, nil
}

func rsyncPullCmd(cfg *config, workdir string, filePaths []string) (*gitapi.Cmd, error) {
	// Replace file paths that are children of deleted directories with the top-most deleted
	// directory below the workdir.  It's not clear that this is always safe behavior for rsync,
	// but it should be safe for our use case.  This is related to an rsync bug, but the patch
	// attached to the report does not look correct.
	// See https://bugzilla.samba.org/show_bug.cgi?id=12569.
	sanitizedFileSet := make(map[string]bool)
	for _, fpath := range filePaths {
		sanitizedFileSet[fpath] = true
	}
	sanitizedFilePaths := stringSet2Slice(sanitizedFileSet)
	sort.Strings(sanitizedFilePaths)

	tmpFile, err := ioutil.TempFile(tmpdir(), "git-sync-file-manifest-")
	if err != nil {
		return nil, err
	}
	atexit.Register(func() {
		_ = os.Remove(tmpFile.Name())
	})

	_, err = tmpFile.WriteString(gitapi.JoinNullTerminated(sanitizedFilePaths))
	if err != nil {
		return nil, err
	}

	sshArgs := []string{"ssh"}
	sshArgs = append(sshArgs, makeSSHArgs(cfg, "", nil)...)
	for i, arg := range sshArgs {
		sshArgs[i] = gitapi.BashQuote(arg)
	}
	rsyncCmdArgs := []string{
		"-czlptgo",
		"-e", strings.Join(sshArgs, " "),
		"--delete-missing-args",
		"--from0",
		"--files-from", tmpFile.Name(),
	}
	if cfg.rsyncRemotePath != "" {
		rsyncCmdArgs = append(rsyncCmdArgs, "--rsync-path", cfg.rsyncRemotePath)
	}
	rsyncCmdArgs = append(rsyncCmdArgs, cfg.remoteURL, workdir)

	cmd := gitapi.Command(cfg.rsyncLocalPath, rsyncCmdArgs...)
	cmd.Env = gitapi.GetRestrictedEnv()
	return cmd, nil
}

// A full sync means resetting the remote workdir to the last shared
// commit and rsyncing any subsequent local commits and local
// modifications.
func fullSync(cfg *config, workdir string) (changedFiles []string, err error) {
	// Use a lock file to guard against git races on the remote side.
	flock, err := flock.Open(path.Join(workdir, ".git/git-sync.mutex"))
	if err != nil {
		return nil, err
	}
	defer flock.Close()

	sc, err := readSyncCookie(workdir)
	if err != nil {
		return nil, err
	}
	foundResults := false
	if !sc.gitStateChanged() && cfg.fsmonitorEnabled() {
		// If the git state changed, we cannot rely on the fast list of changes
		// because the remote mirror working directory will need its state reset.
		changedFiles, err = getChangesViaFsMonitor(cfg, workdir, sc)
		if err != nil {
			log.Warningf("git fsmonitor failed to return results: %s", err)
		} else {
			foundResults = true
		}
	}
	bgGroup := &errgroup.Group{}
	if len(changedFiles) > 0 {
		// If we are going to ship some files, do a speculative fetch to
		// improve performance.
		cmd, err := remoteGitFetchCmd(cfg, workdir)
		if err != nil {
			return nil, err
		}
		bgGroup.Go(func() error {
			_, err := cmd.Output()
			return err
		})
	}

	if !foundResults {
		// This is hiding the implementation of sync for peformance.
		syncCmd, err := gitSyncCmd(cfg, sc)
		if err != nil {
			return nil, err
		}

		syncErr := make(chan error)
		go func() {
			_, err := syncCmd.Output()
			syncErr <- err
		}()

		changedFiles, err = getChangesViaStatus(workdir, sc)
		if err != nil {
			// At this point if we are unable to get changes, it's fatal.
			return nil, err
		}

		if err = <-syncErr; err != nil {
			if rc, rcErr := gitapi.ExitStatus(err); rcErr == nil && rc == 255 {
				// SSH transport errors are common enough to need handling.
				return nil, errors.Errorf("ssh unable to connect to host %s", cfg.remoteSSHAddr())
			}
			return nil, err
		}
	}

	if len(changedFiles) > 0 {
		cmd, err := rsyncPushCmd(cfg, workdir, changedFiles)
		if err == nil {
			_, err = cmd.Output()
		}
		if err != nil {
			return nil, err
		}
		cmd, err = sshStageRemoteChangesCmd(cfg, changedFiles)
		if err == nil {
			_, err = cmd.Output()
		}
		if err != nil {
			return nil, err
		}
	}

	// Only update the sync cookie if we actually sent some changes.
	updateSyncCookie := (len(changedFiles) > 0 || sc.gitStateChanged())
	if updateSyncCookie {
		if err := writeSyncCookie(workdir, sc); err != nil {
			log.Warningf("failed to write sync cookie: %s", err)
		}
	}
	if err := bgGroup.Wait(); err != nil {
		// If we scheduled a background fetch, just wait to prevent zombies.
		// We don't care if there was an error.
		log.Warningf("background remote fetch failed: %s", err)
	}

	if len(changedFiles) > 0 {
		NoisyPrintf("git-sync %d files\n", len(changedFiles))
		log.Infof("file manifest %s", strings.Join(changedFiles, ", "))
	}

	// Return all changed files. This can be used to detect files
	// that changed on remote back to the checked-in version.
	return changedFiles, nil
}

// We send a complex bash script to the remote git workdir. The complexity comes from
// trying to avoid costly operations. For instance, git fetch is slow and frequently not required
// on incremental changes.
//
// If there were local changes to the index (or a fetch was required) a forced checkout is needed,
// even if the commit hash has not changed. This prevents git checkout -- <modified file> from
// getting ignored.
//
// In most cases, a clean is needed even if the merge-base is unchanged. This keeps max_power mode
// correct, up until there is out-of-band tampering with the remote workdir. Unfortunately, there is
// no simple, cheap way to detect tampering.
const remoteGitCmd = `
set -u
set -o pipefail

CHECKOUT_REQUIRED={{.CheckoutRequired}}
CLEAN_REQUIRED={{.CleanRequired}}
SERIALIZED_CHECKOUT_REQUIRED=0

head_hash=$({{.GitRemotePath}} -C {{.RemoteDir}} rev-parse HEAD)
if [[ $head_hash == "" ]]; then
  echo "ERROR: unable to find HEAD revision on remote workdir" >&2
  exit 1
fi

if [[ $head_hash != {{.CommitHash}} ]]; then
  if ! {{.GitRemotePath}} -C {{.RemoteDir}} cat-file -e {{.CommitHash}}; then
    {{.GitRemotePath}} -C {{.RemoteDir}} fetch -q origin master || exit 1
    # If the hash still does not exist, we try to error out with a nice error message
    if ! {{.GitRemotePath}} -C {{.RemoteDir}} cat-file -e {{.CommitHash}}; then
      echo "ERROR: {{.CommitHash}} does not exist on {{.RemoteDir}}. Did you link your local repo to the correct remote repo?" >&2
      exit 1
    fi
  fi
  # If the remote hash does not match we need to serialize the clean operation until
  # after checkout returns.
  SERIALIZED_CHECKOUT_REQUIRED=1
fi

pids=""
if [[ $SERIALIZED_CHECKOUT_REQUIRED == 1 ]]; then
  {{.GitRemotePath}} -C {{.RemoteDir}} checkout -qf {{.CommitHash}} || exit
elif [[ $CHECKOUT_REQUIRED == 1 ]]; then
  {{.GitRemotePath}} -C {{.RemoteDir}} checkout -qf {{.CommitHash}} &
  pids+=" $!"
fi

if [[ $CLEAN_REQUIRED == 1 ]]; then
  # git clean can get significantly if the index is not "tidy" - which is
  # difficult to quantify. Usually an update-index improves performance.
  {{.GitRemotePath}} -C {{.RemoteDir}} clean -qfdx {{.ExcludePaths}} &
  pids+=" $!"
fi
rc=0
for pid in $pids; do
  if ! wait $pid; then
    rc=254
  fi
done

exit $rc
`

// Fill this out to correctly materialize the remote git cmd.
type remoteGitCmdFmt struct {
	CheckoutRequired string
	CleanRequired    string
	GitRemotePath    string
	RemoteDir        string
	CommitHash       string
	ExcludePaths     string
}

// Pull unstaged changes from the remote workdir into the local workdir.
func syncPull(cfg *config, workdir string) (changedFiles []string, err error) {
	// Use a lock file to guard against git races on the remote side.
	flock, err := flock.Open(path.Join(workdir, ".git/git-sync.mutex"))
	if err != nil {
		return nil, err
	}
	defer flock.Close()

	cmd := makeSSHCmd(cfg, cfg.remoteSSHAddr(), []string{
		cfg.gitRemotePath, "-C", cfg.remoteDir(), "status",
		"-z", "--porcelain", "--untracked-file=all",
	})

	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	_, untrackedFiles, _, unstagedFiles, err := gitapi.ParsePorcelainStatus(stdout)
	if err != nil {
		return nil, err
	}

	changedFiles = make([]string, 0, len(untrackedFiles)+len(unstagedFiles))
	changedFiles = append(changedFiles, untrackedFiles...)
	changedFiles = append(changedFiles, unstagedFiles...)

	cmd, err = rsyncPullCmd(cfg, workdir, changedFiles)
	if err != nil {
		return nil, err
	}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return changedFiles, nil
}
