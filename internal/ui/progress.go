// Package ui handles terminal rendering and progress tracking.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/diakovliev/doit/internal/agent"
	"golang.org/x/term"
)

// Renderer is the terminal output surface used by app orchestration.
type Renderer interface {
	Update(agent.ProgressEvent)
	Clear()
	Close()
}

// Factory constructs a progress renderer for one command invocation.
type Factory func(io.Writer, bool) Renderer

// ProgressLine manages a single replaceable line in the terminal for progress updates.
type ProgressLine struct {
	mu          sync.Mutex
	closeOnce   sync.Once
	writer      io.Writer
	enabled     bool
	replace     bool
	metricsText string
	statusText  string
	rendered    bool
	width       int
	resizeStop  chan struct{}
	resizeDone  chan struct{}
	stopResize  func()
}

// NewProgressLine creates a new ProgressLine.
func NewProgressLine(writer io.Writer, enabled bool) Renderer {
	line := &ProgressLine{
		writer:  writer,
		enabled: enabled,
		replace: enabled && isTerminalWriter(writer),
	}
	if line.replace {
		line.width = terminalWidth(writer)
		line.watchResize()
	}
	return line
}

// Update processes a ProgressEvent and updates the terminal line.
func (line *ProgressLine) Update(event agent.ProgressEvent) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if !line.enabled {
		return
	}
	text := fmt.Sprintf("[doit] %s: %s", event.Phase, event.Message)
	if event.Phase == "metrics" {
		line.metricsText = text
		if line.replace {
			line.renderMetricsLine()
		} else {
			_, _ = fmt.Fprintln(line.writer, text)
		}
		return
	}
	line.statusText = text
	if !line.replace {
		_, _ = fmt.Fprintln(line.writer, text)
		return
	}
	line.renderStatusLine()
}

// Clear removes the current rendered line from the terminal.
func (line *ProgressLine) Clear() {
	line.mu.Lock()
	defer line.mu.Unlock()
	line.clearLocked()
}

func (line *ProgressLine) clearLocked() {
	if line.replace && line.rendered {
		_, _ = fmt.Fprint(line.writer, "\r\x1b[2K\x1b[1A\r\x1b[2K\n")
		line.rendered = false
	}
}

// Close clears the progress line before closing.
func (line *ProgressLine) Close() {
	line.closeOnce.Do(func() {
		if line.resizeStop != nil {
			line.stopResize()
			close(line.resizeStop)
			<-line.resizeDone
		}
		line.Clear()
	})
}

func (line *ProgressLine) renderStatusLine() {
	if !line.rendered {
		if line.metricsText == "" {
			line.metricsText = "[doit] metrics: waiting for first model step"
		}
		_, _ = fmt.Fprintf(line.writer, "%s\n%s", line.limit(line.metricsText), line.limit(line.statusText))
		line.rendered = true
		return
	}
	_, _ = fmt.Fprintf(line.writer, "\r\x1b[2K%s", line.limit(line.statusText))
}

func (line *ProgressLine) renderMetricsLine() {
	if !line.rendered {
		line.statusText = "[doit] session: starting"
		line.renderStatusLine()
		return
	}
	_, _ = fmt.Fprintf(line.writer, "\x1b[1A\r\x1b[2K%s\n\r\x1b[2K%s", line.limit(line.metricsText), line.limit(line.statusText))
}

func (line *ProgressLine) limit(value string) string {
	if line.width <= 0 || utf8.RuneCountInString(value) <= line.width {
		return value
	}
	runes := []rune(value)
	if line.width <= 3 {
		return string(runes[:line.width])
	}
	return string(runes[:line.width-3]) + "..."
}

func (line *ProgressLine) watchResize() {
	notifications, stop := terminalResizeNotifications()
	if notifications == nil {
		return
	}
	line.resizeStop = make(chan struct{})
	line.resizeDone = make(chan struct{})
	line.stopResize = stop
	go func() {
		defer close(line.resizeDone)
		for {
			select {
			case <-line.resizeStop:
				return
			case <-notifications:
				line.mu.Lock()
				line.width = terminalWidth(line.writer)
				if line.rendered {
					line.redraw()
				}
				line.mu.Unlock()
			}
		}
	}()
}

func (line *ProgressLine) redraw() {
	_, _ = fmt.Fprint(line.writer, "\r\x1b[1A\r\x1b[2K")
	_, _ = fmt.Fprintf(line.writer, "%s\n%s", line.limit(line.metricsText), line.limit(line.statusText))
}

func terminalWidth(writer io.Writer) int {
	file, ok := writer.(*os.File)
	if !ok {
		return 80
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Markdown helpers for session rendering.
func MarkdownEscape(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func MarkdownTableEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "*", "\\*")
	return value
}
