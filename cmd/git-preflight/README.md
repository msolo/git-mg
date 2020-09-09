# git-preflight

git-preflight examines local changes to a git working directory and dispatches trigger commands based on the patterns matched by modified files.

## Why do this?
In small repos, this may not be particularly more valueable than a `Makefile` with a handy `make all`. In fact, you might ask why such a thing is necessary if you have an adequate build system with various tests, linting and change-detection mechanisms.  The answer is somewhat subtle. A good build system should not modify the contents of its repository. Similarly, a `Makefile` that modifies its inputs is generally a situation one avoids, even if it's something seemingly innocuous, like running a formatter. It's an arbitrary distinction, but one that proves valuable in gigantic repos.

Having a standard config for triggers permits easy subsetting, tagging or other forms of analysis later one. However attractive a shell script seems at first blush, its beauty is lost on the next poor soul that has to modify it.

# Config

Triggers are stored in the repository root in `.git-preflight`. The file is JSONC - which is simply JSON with the added wonderfeature of comments. Right now there is only one `.git-preflight` per repo - more didn't seem to make a lot of sense based on how it is used.

Includes and Excludes patterns are interpreted similarly to fnmatch rules, though patterns without a / character will be matched against the file name only, not the path.

This is an annotated sample config that runs gofmt on all changed *.go files that aren't vendored.

```
{
  // Comments are allowed, this is a JSONC file. See github.com/msolo/jsonc for more details.
  "triggers": [
    {
      "name": "gofmt-or-go-home", // A short name to disambiguate.
      "input_type": "args", // Specify that files are appended as arguments to the command.
      "cmd": ["gofmt", "-w"] // Run this command when files are matched.
      // TODO(msolo) Implement json, null-terminated and line-terminated options on stdin.
      "includes": ["*.go"], // Run on modified files that match the given glob. See fnmatch for more details.
      "excludes": ["vendor/*"] // Skip included files that match any of these globs. ** is not supported.
    }
  ]
}
```

# Usage
```
Usage of git-preflight:

git-preflight [-validate] [-config-file] [-v] [-dry-run] [-commit-hash] [<trigger name>, ...]

Run all triggers for all files changed with respect to the merge base:
  git-preflight

Run a specific trigger for all files changed with respect to the merge base:
	git-preflight <trigger name>

Setting GIT_TRACE_PERFORMANCE=1 or setting -log.level=INFO shows detailed performance logging.

The config file .git-preflight should be place in the root directory of the repository.


  -commit-hash string
    Use a specific commit to generate a list of changed files.
  -config-file string
    Use the specified config file.
  -dry-run
    Log the triggers and commands that would have been executed.
  -log.backtrace-at value
    when logging hits line file:N, emit a stack trace
  -log.level value
    logs at or above this threshold go to stderr (default 1)
  -v	Print more debug data.
  -validate
    Exit after validating the config.

Install bash completions by running:
  complete -C git-prefight git-preflight
```

With `-v`, the tool logs verbosely to the console and injects GIT_PREFLIGHT_VERBOSE=1 into the environment of all triggers so that downstream processes can emit their own additional statement on stderr.
