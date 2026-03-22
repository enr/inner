package main

import (
	"os"
	"os/exec"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/isolator"
)

// App holds application-level dependencies shared across all commands.
// Injecting them (rather than reading globals) makes every command
// independently testable: tests supply a Loader pointing at a temp dir,
// a no-op editorFn, and fake isolator/launcher factories.
type App struct {
	loader     *config.Loader
	editorFn   func(path string) error
	isolatorFn func() (isolator.Isolator, error)
	launcherFn func() *executor.Launcher
}

// newApp creates an App wired to real production dependencies.
func newApp() (*App, error) {
	loader, err := config.DefaultLoader()
	if err != nil {
		return nil, err
	}
	if cwd, err := os.Getwd(); err == nil {
		loader.WorkDir = cwd
	}
	return &App{
		loader:     loader,
		editorFn:   openInEditor,
		isolatorFn: isolator.NewIsolator,
		launcherFn: executor.New,
	}, nil
}

// openInEditor opens path in the user's $EDITOR (fallback: vi).
func openInEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
