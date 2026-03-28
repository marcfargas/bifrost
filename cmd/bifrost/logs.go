package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var follow bool
	var lines int

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print hub log file",
		Long:  "Print the last N lines of the hub log. Use -f to tail new lines as they appear.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(lines, follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log file, printing new lines as they appear")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to print from the end of the log")

	return cmd
}

func runLogs(n int, follow bool) error {
	dataDir := config.DataDir()
	logPath := filepath.Join(dataDir, "hub.log")

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("hub log not found: %s", logPath)
		}
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	// Collect the last n lines by scanning through the file.
	lines, err := tailLines(f, n)
	if err != nil {
		return fmt.Errorf("read log: %w", err)
	}

	for _, line := range lines {
		fmt.Println(line)
	}

	if !follow {
		return nil
	}

	// Tail the file: read new lines as they are appended.
	// bufio.Scanner returns false on EOF, so we poll.
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			// No new data — wait and retry.
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			return fmt.Errorf("follow log: %w", err)
		}
		fmt.Print(line)
	}
}

// tailLines returns the last n lines from r by reading all lines into a
// ring buffer.
func tailLines(r io.Reader, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}

	scanner := bufio.NewScanner(r)
	ring := make([]string, n)
	pos := 0
	total := 0

	for scanner.Scan() {
		ring[pos%n] = scanner.Text()
		pos++
		total++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if total == 0 {
		return nil, nil
	}

	count := total
	if count > n {
		count = n
	}

	result := make([]string, count)
	start := pos - count
	for i := 0; i < count; i++ {
		result[i] = ring[(start+i)%n]
	}
	return result, nil
}
