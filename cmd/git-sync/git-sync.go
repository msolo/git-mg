package main

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"time"

	log "github.com/msolo/go-bis/glug"
	"github.com/tebeka/atexit"

	"github.com/msolo/cmdflag"
)

type predictGitRemoteName struct{}

// Predict a single valid name for a git remote.
func (*predictGitRemoteName) Predict(cargs cmdflag.Args) []string {
	if cargs.LastCompleted != "push" && cargs.LastCompleted != "pull" {
		return nil
	}
	cmd := exec.Command("git", "remote")
	stdout, err := cmd.Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(stdout))
}

var cmdPush = &cmdflag.Command{
	Name:      "push",
	Run:       runPush,
	Args:      &predictGitRemoteName{},
	UsageLine: `Push a working directory to a remote working dir.`,
	UsageLong: `Push a working directory to a remote working dir.

  git-sync push [<remote name>]`,
}

var cmdPull = &cmdflag.Command{
	Name:      "pull",
	Run:       runPull,
	Args:      &predictGitRemoteName{},
	UsageLine: `Pull unstaged changes from a remote working directory.`,
	UsageLong: `Pull unstaged changes from a remote working directory.
  git-sync pull [<remote name>]`,
}

func exitOnError(err error) {
	if err != nil {
		atexit.Fatal(err)
	}
}

func runPush(ctx context.Context, cmd *cmdflag.Command, args []string) {
	remoteName := ""
	if len(args) == 1 {
		remoteName = args[0]
	}
	cfg, err := readConfigFromGit(remoteName)
	exitOnError(err)

	gitWorkdir := getGitWorkdir()
	_, err = fullSync(cfg, gitWorkdir)
	exitOnError(err)
}

func runPull(ctx context.Context, cmd *cmdflag.Command, args []string) {
	remoteName := ""
	if len(args) == 1 {
		remoteName = args[0]
	}
	cfg, err := readConfigFromGit(remoteName)
	exitOnError(err)

	gitWorkdir := getGitWorkdir()
	_, err = syncPull(cfg, gitWorkdir)
	exitOnError(err)
}

var cmdMain = &cmdflag.Command{
	Name: "git-sync",
	UsageLong: `git-sync - a tool to sync working directories

git-sync uses SSH, rsync and git (and optionally watchman via
fsmonitor) to efficiently copy local working directory changes to a
mirrored copy on a different machine. The goal is generally to be able
to reliably sync (even on an LTE connection) within 500ms even if the
repo contains > 250k files.

git-sync is potentially destructive to the target working directory -
it will clean, reset and checkout changes to ensure the source and
destination working directories are equivalent.

Config:
git-sync reads a few variables from the [sync] section of the git config:

sync.remoteName (default "sync")
  This determines the remote target to use for syncing changes.

sync.excludePaths (default empty)
  A colon-delimited list of patterns that will be passed to git clean
  on the remote target.  This allows some remote data to persist, even
  if it does not exist on the source workdir.

sync.rsyncRemotePath (default "/usr/local/bin/rsync")
  The path for the remote rsync binary.

git-sync uses the remote name to determine the SSH URL that is used as
the target for rsync operations.

If core.fsmonitor is configured it will be used to find changes quickly.
`,
	Flags: []cmdflag.Flag{
		{"timeout", cmdflag.FlagTypeDuration, 0 * time.Millisecond, "timeout for command execution", nil},
	},
	Args: cmdflag.PredictNothing, // TODO(msolo) Add support for picking a specific remote.
}

var subcommands = []*cmdflag.Command{
	cmdPush,
	cmdPull,
}

func main() {
	defer atexit.Exit(0)

	if val := os.Getenv("GIT_TRACE_PERFORMANCE"); val != "" && val != "0" {
		log.SetLevel("INFO")
	} else {
		log.SetLevel("WARNING")
	}

	var timeout time.Duration
	fs := cmdMain.BindFlagSet(map[string]interface{}{"timeout": &timeout})
	log.RegisterFlags(fs)
	RegisterFlags(fs)

	cmd, args := cmdflag.Parse(cmdMain, subcommands)

	ctx := context.Background()
	if timeout > 0 {
		nctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = nctx
	}

	cmd.Run(ctx, cmd, args)
}
