// Package exec is glue between the vendored terraform codebase and installer.
package exec

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/logutils"
	"github.com/hashicorp/terraform/command"
	"github.com/hashicorp/terraform/helper/logging"
	"github.com/hashicorp/terraform/version"
	"github.com/mitchellh/cli"
)

type cmdFunc func(command.Meta) cli.Command

var commands = map[string]cmdFunc{
	"apply": func(meta command.Meta) cli.Command {
		return &command.ApplyCommand{Meta: meta}
	},
	"destroy": func(meta command.Meta) cli.Command {
		return &command.ApplyCommand{Meta: meta, Destroy: true}
	},
	"init": func(meta command.Meta) cli.Command {
		return &command.InitCommand{Meta: meta}
	},
}

func runner(cmd string, dir string, args []string, stdout, stderr io.Writer) int {
	lf := ioutil.Discard
	if level := logging.LogLevel(); level != "" {
		lf = &logutils.LevelFilter{
			Levels:   logging.ValidLevels,
			MinLevel: logutils.LogLevel(level),
			Writer:   stdout,
		}
	}
	log.SetOutput(lf)
	defer log.SetOutput(os.Stderr)

	// Make sure we clean up any managed plugins at the end of this
	defer plugin.CleanupClients()

	sdCh, cancel := makeShutdownCh()
	defer cancel()

	meta := command.Meta{
		Color:            false,
		GlobalPluginDirs: globalPluginDirs(stderr),
		Ui: &cli.BasicUi{
			Writer:      stdout,
			ErrorWriter: stderr,
		},

		OverrideDataDir: dir,

		ShutdownCh: sdCh,
	}

	f := commands[cmd]

	oldStderr := os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(stderr, "error creating Pipe: %v", err)
		return 1
	}
	os.Stderr = outW
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			fmt.Fprintf(lf, "%s\n", scanner.Bytes())
		}
	}()
	defer func() {
		outW.Close()
		os.Stderr = oldStderr
	}()
	return f(meta).Run(args)
}

// Apply is wrapper around `terraform apply` subcommand.
func Apply(datadir string, args []string, stdout, stderr io.Writer) int {
	return runner("apply", datadir, args, stdout, stderr)
}

// Destroy is wrapper around `terraform destroy` subcommand.
func Destroy(datadir string, args []string, stdout, stderr io.Writer) int {
	return runner("destroy", datadir, args, stdout, stderr)
}

// Init is wrapper around `terraform init` subcommand.
func Init(datadir string, args []string, stdout, stderr io.Writer) int {
	return runner("init", datadir, args, stdout, stderr)
}

// Version is a wrapper around `terraform version` subcommand.
// Comapared to other wrappers this only supports subset of the `terraform version` options.
func Version() string {
	var versionString bytes.Buffer
	fmt.Fprintf(&versionString, "Terraform v%s", version.Version)
	if version.Prerelease != "" {
		fmt.Fprintf(&versionString, "-%s", version.Prerelease)
	}
	return versionString.String()
}

// makeShutdownCh creates an interrupt listener and returns a channel.
// A message will be sent on the channel for every interrupt received.
func makeShutdownCh() (<-chan struct{}, func()) {
	resultCh := make(chan struct{})
	signalCh := make(chan os.Signal, 4)

	handle := []os.Signal{}
	handle = append(handle, ignoreSignals...)
	handle = append(handle, forwardSignals...)

	signal.Notify(signalCh, handle...)
	go func() {
		for {
			<-signalCh
			resultCh <- struct{}{}
		}
	}()

	return resultCh, func() { signal.Reset(handle...) }
}

func globalPluginDirs(stderr io.Writer) []string {
	var ret []string
	// Look in ~/.terraform.d/plugins/ , or its equivalent on non-UNIX
	dir, err := configDir()
	if err != nil {
		fmt.Fprintf(stderr, "Error finding global config directory: %s", err)
	} else {
		machineDir := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
		ret = append(ret, filepath.Join(dir, "plugins"))
		ret = append(ret, filepath.Join(dir, "plugins", machineDir))
	}

	return ret
}
