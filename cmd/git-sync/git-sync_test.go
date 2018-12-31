package main

import (
	"io"
	"io/ioutil"
	"os"
	"path"
	"testing"

	"github.com/apex/log"
)

type repo struct {
	tmpDir      string
	upstreamDir string
	localDir    string
	syncDir     string
}

func (rp *repo) Close() error {
	return os.RemoveAll(rp.tmpDir)
}

// Sets up the test repo environment, with one upstream repo, one local repo,
// and one sync repo.
func repoSetup() (*repo, error) {
	tmpDir, err := ioutil.TempDir("", "git-sync-test-repo-")
	if err != nil {
		return nil, err
	}
	upstreamDir := path.Join(tmpDir, "upstream")
	if err := os.MkdirAll(upstreamDir, 0775); err != nil {
		return nil, err
	}
	cmd := Command("git", "-C", upstreamDir, "init", "-q")
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	dummyFile := path.Join(upstreamDir, "dummy")
	err = ioutil.WriteFile(dummyFile, []byte(""), 0664)
	if err != nil {
		return nil, err
	}
	cmd = Command("git", "-C", upstreamDir, "config", "receive.denyCurrentBranch", "ignore")
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// when we rsync from mac to itself, for some reason on the remote side,
	// /usr/bin/rsync is picked over /usr/local/bin/rsync, causing us to not
	// use the newer rsync from homebrew and thus causing flags like
	// --delete-missing-args to fail.
	cmd = Command("git", "-C", upstreamDir, "config", "--add", "sync.rsyncRemotePath", "/usr/local/bin/rsync")
	if _, err := cmd.Output(); err != nil {
		return nil, err
	}
	cmd = Command("git", "-C", upstreamDir, "add", "dummy")
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	cmd = Command("git", "-C", upstreamDir, "commit", "-q", "-m", "initial commit")
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	localDir := path.Join(tmpDir, "local")
	syncDir := path.Join(tmpDir, "sync")
	cmd = Command("git", "-C", upstreamDir, "clone", "-q", upstreamDir, localDir)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	cmd = Command("git", "-C", upstreamDir, "clone", "-q", upstreamDir, syncDir)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	cmd = Command("git", "-C", localDir, "remote", "add", "sync", "localhost:"+syncDir)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	wd, _ := os.Getwd()
	cmd = Command(path.Join(wd, "git-sync"), "push")
	cmd.Dir = localDir
	if _, err := cmd.Output(); err != nil {
		return nil, err
	}
	return &repo{tmpDir: tmpDir, upstreamDir: upstreamDir, localDir: localDir, syncDir: syncDir}, nil
}

func failOnErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func failOnCmdError(t *testing.T, workdir string, bin string, args ...string) {
	t.Helper()
	cmd := Command(bin, args...)
	cmd.Dir = workdir
	_, err := cmd.Output()
	failOnErr(t, err)
}

func TestSync(t *testing.T) {
	wd, _ := os.Getwd()

	rp, err := repoSetup()
	failOnErr(t, err)
	defer rp.Close()

	// Very basic sync scenarios.
	err = ioutil.WriteFile(path.Join(rp.localDir, "a"), []byte(""), 0644)
	failOnErr(t, err)

	failOnCmdError(t, rp.localDir, path.Join(wd, "git-sync"), "push")

	_, err = os.Stat(path.Join(rp.syncDir, "a"))
	failOnErr(t, err)

	// Test simple append.
	fd, err := os.OpenFile(path.Join(rp.localDir, "a"), os.O_APPEND|os.O_WRONLY, 0644)
	failOnErr(t, err)
	_, err = io.WriteString(fd, "foo")
	failOnErr(t, err)
	failOnErr(t, fd.Sync())
	failOnErr(t, fd.Close())

	failOnCmdError(t, rp.localDir, "git", "add", "a")
	failOnCmdError(t, rp.localDir, "git", "commit", "-q", "-m", "added file a")
	failOnCmdError(t, rp.localDir, path.Join(wd, "git-sync"), "push")

	data, err := ioutil.ReadFile(path.Join(rp.syncDir, "a"))
	failOnErr(t, err)
	if string(data) != "foo" {
		t.Fatalf(`unexpected file content: %q != "foo"`, data)
	}

	// Test file remove.
	err = os.Remove(path.Join(rp.localDir, "a"))
	failOnErr(t, err)

	failOnCmdError(t, rp.localDir, "git", "add", "a")
	failOnCmdError(t, rp.localDir, "git", "commit", "-q", "-m", "removed file a")
	failOnCmdError(t, rp.localDir, "git", "push", "-q", "origin", "master")
	failOnCmdError(t, rp.localDir, path.Join(wd, "git-sync"), "push")

	_, err = os.Stat(path.Join(rp.syncDir, "a"))
	if !os.IsNotExist(err) {
		t.Fatalf(`unexpected file existence, err: %s`, err)
	}
}

func TestMain(m *testing.M) {
	if val := os.Getenv("GIT_TRACE"); val != "" && val != "0" {
		log.SetLevel(log.DebugLevel)
	}
	log.SetHandler(log.HandlerFunc(glogLine))
	// call flag.Parse() here if TestMain uses flags
	os.Exit(m.Run())
}
