// Package projectinit creates project-local doit scaffolding.
package projectinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diakovliev/doit/internal/apperr"
)

// Result describes the files created or preserved by initialization.
type Result struct {
	Workspace string   `json:"workspace"`
	Created   []string `json:"created"`
	Existing  []string `json:"existing"`
}

type template struct {
	path    string
	content string
}

var templates = []template{
	{path: ".doit/config.json", content: configTemplate},
	{path: ".doit/instructions.md", content: instructionsTemplate},
	{path: ".doit/instructions/README.md", content: instructionsReadmeTemplate},
	{path: ".doit/skills/README.md", content: skillsReadmeTemplate},
	{path: ".doit/skills/example/SKILL.md.template", content: skillTemplate},
}

const configTemplate = `{
  "default_profile": "lmstudio",
  "profiles": {
    "lmstudio": {
      "api_root": "http://localhost:1234/v1",
      "model": "replace-with-your-model"
    },
    "ollama": {
      "api_root": "http://127.0.0.1:11434/v1",
      "model": "replace-with-your-model"
    }
  },
  "tasks": {}
}
`

const instructionsTemplate = `# Project Instructions

<!-- Replace this starter content with instructions that apply to the whole repository. -->

## Repository purpose

Describe what this repository does and who uses it.

## Engineering rules

- Keep changes focused and explain important behavior in tests.
- Run the repository's configured validation tasks before reporting completion.
- Do not place credentials or other secrets in project files.
`

const instructionsReadmeTemplate = `# Instruction Files

The parent .doit/instructions.md file is loaded for every model request. Add focused Markdown files in this directory when a rule applies to a specific area of the repository.

Use short, actionable guidance. Keep secrets and generated session data out of instruction files.
`

const skillsReadmeTemplate = `# Skill Templates

Copy example/SKILL.md.template to a new <name>/SKILL.md directory when you want to add a repository skill. Skill files are loaded automatically after they are created.

Keep each skill focused on one repeatable workflow and state when it should be used.
`

const skillTemplate = `# Skill Name

Describe one repeatable repository workflow here.

## When to use

State the request or conditions that activate this skill.

## Procedure

1. Gather the relevant context.
2. Perform the smallest safe change.
3. Run the focused validation for the change.
`

// Initialize creates the project-local .doit scaffold below workspace.
// Existing files are preserved so initialization is safe to repeat.
func Initialize(ctx context.Context, workspace string) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	absoluteWorkspace, err := absoluteWorkspacePath(workspace)
	if err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(absoluteWorkspace)
	if err != nil {
		return Result{}, apperr.Wrap(apperr.KindConfig, "projectinit.workspace", err)
	}
	defer func() { _ = root.Close() }()

	result := Result{Workspace: absoluteWorkspace}
	for _, fileTemplate := range templates {
		if err := contextError(ctx); err != nil {
			return result, err
		}
		if err := createParentDirectories(root, fileTemplate.path); err != nil {
			return result, err
		}
		created, err := createFile(root, fileTemplate)
		if err != nil {
			return result, err
		}
		if created {
			result.Created = append(result.Created, fileTemplate.path)
		} else {
			result.Existing = append(result.Existing, fileTemplate.path)
		}
	}
	return result, nil
}

func absoluteWorkspacePath(workspace string) (string, error) {
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return "", apperr.Wrap(apperr.KindConfig, "projectinit.workspace", err)
		}
	}
	absolutePath, err := filepath.Abs(workspace)
	if err != nil {
		return "", apperr.Wrap(apperr.KindConfig, "projectinit.workspace", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", apperr.Wrap(apperr.KindConfig, "projectinit.workspace", err)
	}
	if !info.IsDir() {
		return "", apperr.New(apperr.KindConfig, "projectinit.workspace", "workspace is not a directory: "+absolutePath)
	}
	return absolutePath, nil
}

func createParentDirectories(root *os.Root, path string) error {
	parent := filepath.Dir(path)
	if parent == "." {
		return nil
	}
	if err := root.MkdirAll(parent, 0700); err != nil {
		return apperr.Wrap(apperr.KindConfig, "projectinit.directory", err)
	}
	return nil
}

func createFile(root *os.Root, fileTemplate template) (bool, error) {
	file, err := root.OpenFile(fileTemplate.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := root.Stat(fileTemplate.path)
		if statErr != nil {
			return false, apperr.Wrap(apperr.KindConfig, "projectinit.file", statErr)
		}
		if info.IsDir() {
			return false, apperr.New(apperr.KindConfig, "projectinit.file", "template path is a directory: "+fileTemplate.path)
		}
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(apperr.KindConfig, "projectinit.file", err)
	}
	if _, err := file.WriteString(fileTemplate.content); err != nil {
		_ = file.Close()
		return false, apperr.Wrap(apperr.KindConfig, "projectinit.file", err)
	}
	if err := file.Close(); err != nil {
		return false, apperr.Wrap(apperr.KindConfig, "projectinit.file", err)
	}
	return true, nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return apperr.Wrap(apperr.KindCancelled, "projectinit", fmt.Errorf("initialization cancelled: %w", err))
	}
	return nil
}
