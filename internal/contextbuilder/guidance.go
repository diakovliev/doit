package contextbuilder

import (
	stdcontext "context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxGuidanceFileBytes = 16 * 1024
	maxGuidanceBytes     = 64 * 1024
)

type guidanceDocument struct {
	path    string
	content string
}

type guidanceDirectory struct {
	path   string
	suffix string
}

var guidanceInstructionFiles = []string{
	".github/copilot-instructions.md",
	"AGENTS.md",
	".doit/instructions.md",
}

var guidanceInstructionDirectories = []guidanceDirectory{
	{path: ".github/instructions", suffix: ".instructions.md"},
	{path: ".doit/instructions", suffix: ".md"},
}

var guidanceSkillDirectories = []string{
	".github/skills",
	".agents/skills",
	".doit/skills",
}

func loadRepositoryGuidance(ctx stdcontext.Context, workspace string) string {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()

	instructions := readGuidanceFiles(ctx, root, guidanceInstructionFiles)
	instructions = append(instructions, readGuidanceFilesFromDirectories(ctx, root, guidanceInstructionDirectories)...)
	skills := readSkillFiles(ctx, root)
	return renderGuidance(instructions, skills)
}

func readGuidanceFiles(ctx stdcontext.Context, root *os.Root, paths []string) []guidanceDocument {
	documents := make([]guidanceDocument, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return documents
		}
		if document, ok := readGuidanceFile(root, path); ok {
			documents = append(documents, document)
		}
	}
	return documents
}

func readGuidanceFilesFromDirectories(ctx stdcontext.Context, root *os.Root, directories []guidanceDirectory) []guidanceDocument {
	documents := make([]guidanceDocument, 0)
	for _, directory := range directories {
		entries := readDirectoryEntries(root, directory.path)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return documents
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), directory.suffix) {
				continue
			}
			path := filepath.Join(directory.path, entry.Name())
			if document, ok := readGuidanceFile(root, path); ok {
				documents = append(documents, document)
			}
		}
	}
	return documents
}

func readSkillFiles(ctx stdcontext.Context, root *os.Root) []guidanceDocument {
	documents := make([]guidanceDocument, 0)
	for _, directory := range guidanceSkillDirectories {
		entries := readDirectoryEntries(root, directory)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return documents
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(directory, entry.Name(), "SKILL.md")
			if document, ok := readGuidanceFile(root, path); ok {
				documents = append(documents, document)
			}
		}
	}
	return documents
}

func readDirectoryEntries(root *os.Root, path string) []os.DirEntry {
	directory, err := root.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil
	}
	slices.SortFunc(entries, func(first, second os.DirEntry) int {
		return strings.Compare(first.Name(), second.Name())
	})
	return entries
}

func readGuidanceFile(root *os.Root, path string) (guidanceDocument, bool) {
	contents, err := root.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return guidanceDocument{}, false
		}
		return guidanceDocument{}, false
	}
	if len(contents) > maxGuidanceFileBytes {
		contents = contents[:maxGuidanceFileBytes]
	}
	content := strings.TrimSpace(string(contents))
	if content == "" {
		return guidanceDocument{}, false
	}
	return guidanceDocument{path: filepath.ToSlash(path), content: content}, true
}

func renderGuidance(instructions, skills []guidanceDocument) string {
	var builder strings.Builder
	appendGuidanceSection(&builder, "Repository instructions", instructions)
	appendGuidanceSection(&builder, "Repository skills", skills)
	return builder.String()
}

func appendGuidanceSection(builder *strings.Builder, title string, documents []guidanceDocument) {
	if len(documents) == 0 || builder.Len() >= maxGuidanceBytes {
		return
	}
	writeGuidance(builder, "\n"+title+":\n", maxGuidanceBytes)
	for _, document := range documents {
		if builder.Len() >= maxGuidanceBytes {
			return
		}
		writeGuidance(builder, "\n--- "+document.path+" ---\n", maxGuidanceBytes)
		writeGuidance(builder, document.content+"\n", maxGuidanceBytes)
	}
}

func writeGuidance(builder *strings.Builder, value string, limit int) {
	remaining := limit - builder.Len()
	if remaining <= 0 {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	builder.WriteString(value)
}
