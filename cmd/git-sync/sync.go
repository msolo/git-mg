package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	log "github.com/amoghe/distillog"
	isatty "github.com/mattn/go-isatty"
	"github.com/msolo/go-bis/flock"
	"github.com/tebeka/atexit"
)

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
	gitCmd := gwd.makeCmd("merge-base", "origin/master", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

func getHeadCommitHash(workdir string) (string, error) {
	gwd := gitWorkDir{workdir}
	gitCmd := gwd.makeCmd("rev-parse", "HEAD")
	out, err := gitCmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

const safeUnquoted = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%_-+=:,./"

func bashQuote(s string) string {
	// Double escaping ~ neuters expansion and ~ is implicit.
	if strings.HasPrefix(s, "~/") {
		return s
	}
	if s == "" {
		return "''"
	}
	hasUnsafe := false
	for _, r := range s {
		if !strings.ContainsRune(safeUnquoted, r) {
			hasUnsafe = true
			break
		}
	}
	if !hasUnsafe {
		return s
	}
	return "'" + strings.Replace(s, "'", "'\"'\"'", -1) + "'"
}

func makeSSHArgs(cfg *config, addr string, bashCmdArgs []string) []string {
	sshOptions := map[string]string{
		"ConnectTimeout": "5",
		"ControlMaster":  "auto", // auto|no
		"ControlPath":    cfg.sshControlPath,
		"ControlPersist": "15m",
		"ForwardAgent":   "yes",
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

	for k, v := range sshOptions {
		sshArgs = append(sshArgs, "-o"+k+"="+v)
	}
	if addr != "" {
		sshArgs = append(sshArgs, addr)
	}

	if len(bashCmdArgs) > 0 {
		bashCmd := "/bin/bash --noprofile --norc -c " + bashQuote(strings.Join(bashCmdArgs, " "))
		sshArgs = append(sshArgs, bashCmd)
	}
	return sshArgs
}

func makeSSHCmd(cfg *config, addr string, bashCmdArgs []string) *Cmd {
	cmd := Command("ssh", makeSSHArgs(cfg, addr, bashCmdArgs)...)
	cmd.Env = getRestrictedEnv()
	return cmd

}

type syncCookie struct {
	LastHeadHash      string
	LastMergeBaseHash string
	LastSyncStartNs   int64 `json:",string"`
	headHash          string
	mergeBaseHash     string
	syncStartNs       int64
	filePaths         []string
	foundResults      bool
}

func (sc syncCookie) gitStateChanged() bool {
	return !(sc.LastHeadHash != "" && sc.LastHeadHash == sc.headHash && sc.LastMergeBaseHash == sc.mergeBaseHash)
}

// This is more like "read sync cookie and current state". Cookie may be a stupid name.
func readSyncCookie(workdir string) (sc *syncCookie, err error) {
	headHash, err := getHeadCommitHash(workdir)
	if err != nil {
		return nil, err
	}
	mergeBaseHash, err := getMergeBaseCommitHash(workdir)
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

type fileStatus struct {
	fname  string
	status int
}

// Use file system notifications to find changed files rather than git.
func getChangesViaFsMonitor(cfg *config, workdir string, sc *syncCookie) (changedFiles []string, err error) {
	// FIXME(msolo) Figure out what do with Linux, if anything.
	// config.use_fsnotify and sys.platform == 'darwin':

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
	fsMonCmd := CommandContext(ctx, cfg.fsmonitorLocalPath, "1", strconv.FormatInt(ts, 10))
	fsMonCmd.Env = getRestrictedEnv()
	fsMonCmd.Dir = workdir

	out, err := fsMonCmd.Output()
	if err != nil {
		log.Warningln("git fsmonitor failed:", err)
		return nil, err
	}

	filePaths := splitNullTerminated(string(out))
	// Too many changes, just do a full sync by pretending we couldn't get
	// results.
	if len(filePaths) > 100 {
		log.Warningf("git fs monitor returned too many changes: %d", len(filePaths))
		return nil, nil
	}

	// The crazy git protocol can return / to mean "everything might have
	// changed".
	if len(filePaths) == 1 && filePaths[0] == "/" {
		return nil, nil
	}

	isDir := func(fname string) bool {
		fi, err := os.Stat(fname)
		if err != nil {
			return false
		}
		return fi.IsDir()
	}
	// This filter is expensive because of the directory checking.
	filteredFileSet := make(map[string]bool, len(filePaths))
	for _, fname := range filePaths {
		if fname != "" && fname != ".git" && strings.HasPrefix(fname, ".git/") && isDir(fname) {
			filteredFileSet[fname] = true
		}
	}
	if len(filteredFileSet) > 0 {
		ignoredFilePaths, err := gitCheckIgnore(workdir, stringSet2Slice(filteredFileSet))
		if err != nil {
			return nil, err
		}
		for _, fname := range ignoredFilePaths {
			delete(filteredFileSet, fname)
		}
	}
	return stringSet2Slice(filteredFileSet), nil
}

// fixme look for more uses
func stringSet2Slice(ss map[string]bool) []string {
	sl := make([]string, 0, len(ss))
	for x := range ss {
		sl = append(sl, x)
	}
	return sl
}

func parsePorcelainStatus(data []byte) (modifiedFiles []string, untrackedDirs []string, renamedFiles []string, err error) {
	entries := splitNullTerminated(string(data))
	modifiedFiles = make([]string, 0, 16)
	untrackedDirs = make([]string, 0, 16)
	renamedFiles = make([]string, 0, 16)
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		status, fname := entry[:2], entry[3:]
		if strings.HasSuffix(fname, "/") {
			untrackedDirs = append(untrackedDirs, fname)
			continue
		}
		if status != "UU" {
			modifiedFiles = append(modifiedFiles, fname)
		} else {
			// Ignore merge conflicts. They have to be resolved by hand
			// anyway, which will require another sync.
			log.Warningln("ignoring unmerged file:", fname)
		}
		if status[0] == 'R' {
			i++
			renamedFile := entries[i]
			modifiedFiles = append(modifiedFiles, renamedFile)
			renamedFiles = append(renamedFiles, renamedFile)
		}
	}
	return modifiedFiles, untrackedDirs, renamedFiles, nil
}

func getGitStatus(workdir string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.makeCmd("status", "-z", "--porcelain", "--untracked-files=all")
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles, _, _, err = parsePorcelainStatus(stdout)
	return changedFiles, err
}

// Return all files that have been changed on HEAD relative to the merge base.
func getGitDiffChanges(workdir string, mergeBaseHash string) (changedFiles []string, err error) {
	gwd := &gitWorkDir{workdir}
	cmd := gwd.makeCmd("diff", "-z", "--no-renames", "--name-only", "HEAD", mergeBaseHash)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	changedFiles = splitNullTerminated(string(stdout))
	return changedFiles, nil
}

func joinNullTerminated(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return strings.Join(ss, "\000") + "\000"
}

func splitNullTerminated(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s[:len(s)-1], "\000")
}

// Return a list of ignored files.
func gitCheckIgnore(workdir string, filePaths []string) ([]string, error) {
	data := joinNullTerminated(filePaths)
	// NOTE: --no-index makes this call ~5ms instead of 150ms, but we have
	// false positives due to what we store in the tree.
	gwd := gitWorkDir{workdir}
	cmd := gwd.makeCmd("check-ignore", "-z", "--stdin", "--no-index")
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
	cmd := gwd.makeCmd(args...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	_, _, renamedFiles, err := parsePorcelainStatus(stdout)
	return renamedFiles, err
}

func gitSyncCmd(cfg *config, sc *syncCookie) (*Cmd, error) {
	bashCmdArgs := make([]string, 0, 16)
	// Plumb some handy profiling variables through.
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "GIT_TRACE") {
			bashCmdArgs = append(bashCmdArgs, "export "+env)
		}
	}
	// FIXME(msolo) Remove? Too bazel specific and magical.
	excludePaths := []string{"--exclude=/bazel-*"}
	for _, ex := range cfg.gitSyncExcludePaths {
		excludePaths = append(excludePaths, ex)
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
		files, err := getGitStatus(workdir)
		if err != nil {
			return err
		}
		updateSet(files)
		return nil
	}

	y := func() error {
		files, err := getGitDiffChanges(workdir, sc.mergeBaseHash)
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
	changedFiles = make([]string, 0, len(fileSet))
	for fname := range fileSet {
		changedFiles = append(changedFiles, fname)
	}
	mu.Unlock()

	// todo sort?
	return changedFiles, nil
}

func remoteGitFetchCmd(workdir string, cfg *config) (*Cmd, error) {
	shCmd := "flock --nonblock {{.RemoteDir}}/.git/FETCH_HEAD {{.GitRemotePath}} -C {{.RemoteDir}} fetch -q origin master < /dev/null > /dev/null 2>&1 &"
	tmpl := template.New("remoteGitFetchCmd").Option("missingkey=error")
	shCmdFmt := struct {
		RemoteDir     string
		GitRemotePath string
	}{bashQuote(cfg.remoteDir()), cfg.gitRemotePath}
	buf := bytes.NewBuffer(make([]byte, 1024))
	if err := tmpl.ExecuteTemplate(buf, shCmd, shCmdFmt); err != nil {
		return nil, err
	}
	cmd := makeSSHCmd(cfg, "", []string{buf.String()})
	return cmd, nil
}

func topmostMissingDir(workdir string, fname string) (string, error) {
	dir := workdir
	names := strings.Split(strings.Trim(fname, "/"), "/")
	for _, name := range names {
		dir = path.Join(dir, name)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			// rsync wants relative paths.
			// we also append / to conform with rsync's convention for directory
			// names
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

func rsyncCmd(cfg *config, workdir string, remoteURL string, filePaths []string) (*Cmd, error) {
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
	sanitizedFilePaths := make([]string, 0, len(sanitizedFileSet))
	for k := range sanitizedFileSet {
		sanitizedFilePaths = append(sanitizedFilePaths, k)
	}

	tmpFile, err := ioutil.TempFile(tmpdir(), "git-sync-file-manifest-")
	if err != nil {
		return nil, err
	}
	atexit.Register(func() {
		_ = os.Remove(tmpFile.Name())
	})

	sort.Strings(sanitizedFilePaths)
	_, err = tmpFile.WriteString(joinNullTerminated(sanitizedFilePaths))
	if err != nil {
		return nil, err
	}

	// fixme(msolo) ssh interface feels klunky
	sshArgs := []string{"ssh"}
	sshArgs = append(sshArgs, makeSSHArgs(cfg, "", nil)...)
	for i, arg := range sshArgs {
		sshArgs[i] = bashQuote(arg)
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

	cmd := Command(cfg.rsyncLocalPath, rsyncCmdArgs...)
	cmd.Env = getRestrictedEnv()
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
		// If we are going to ship some files, do a speculative fetch to improve performance.
		cmd, err := remoteGitFetchCmd(workdir, cfg)
		if err != nil {
			return nil, err
		}
		bgGroup.Go(func() error {
			_, err := cmd.Output()
			return err
		})
	}
	// FIXME(msolo) bgGroup might need to be canceled on return

	var syncCmd *Cmd

	if !foundResults {
		// fixme: should this return new synccookie with git state?
		// this is hiding the implementation of sync for peformance
		syncCmd, err = gitSyncCmd(cfg, sc)
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
			return nil, err
		}

	}

	// FIXME(msolo)
	// 		# If all the files exist we don't need rsync for delete handling.
	// 		if config.tarsync and all([os.path.exists(os.path.join(workdir, x))
	// 															 for x in file_paths]):
	// 				tarsync(workdir, remote_host, remote_dir, file_paths)
	// 		else:
	if len(changedFiles) > 0 {
		// fixme cfg args look klunky
		cmd, err := rsyncCmd(cfg, workdir, cfg.remoteURL, changedFiles)
		if err == nil {
			err = cmd.Run()
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
		if exitErr, ok := errors.Cause(err).(*exec.ExitError); ok {
			log.Warningf("background process failed: %s\n%s", exitErr, exitErr.Stderr)
		} else {
			log.Warningf("background process failed: %s", err)
		}
	}

	log.Infof("git-sync %d files %v", len(changedFiles), changedFiles)

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
    # git clean can get up to 3x slower (500ms -> 1500ms) if the index is not "tidy" - which is
    # difficult to quantify. Usually an update-index improves performance.
    {{.GitRemotePath}} -C {{.RemoteDir}} clean -qfdx {{.ExcludePaths}} &
fi
pids+=" $!"
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
