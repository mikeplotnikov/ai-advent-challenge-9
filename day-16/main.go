// Day 16 lists the tools offered by an MCP server; it neither calls a tool nor an LLM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("day-16", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", "", "Streamable HTTP MCP server address")
	command := flags.String("command", "", "stdio MCP server command")
	timeout := flags.String("timeout", mcpclient.DefaultTimeout.String(), "overall deadline")
	save := flags.String("save", "", "file for raw exchange capture")
	dump := flags.Bool("dump", false, "dump criterion definitions and exit")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, "ошибка использования:", err)
		return 2
	}
	if *dump {
		if err := writeDump(stdout); err != nil {
			fmt.Fprintln(stderr, "ошибка:", err)
			return 1
		}
		return 0
	}
	duration, err := mcpclient.ParseTimeout(*timeout)
	if err != nil {
		fmt.Fprintln(stderr, "ошибка использования:", err)
		return 2
	}
	transport, err := mcpclient.NewTransport(*endpoint, *command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	started := time.Now()
	client, err := mcpclient.New()
	var result mcpclient.Result
	var formatted mcpclient.Formatted
	if err == nil {
		result, err = client.List(ctx, transport)
		if err == nil {
			formatted, err = mcpclient.Format(result)
		}
	}
	// The connection outcome is reported FIRST and decides what the operator sees: a
	// successful, criterion-passing list must never be swallowed because -save could not
	// write its file. A failed -save still fails the run — the file was asked for — but
	// it is reported as its own line, after the result, not instead of it.
	if err != nil {
		fmt.Fprintln(stderr, readableError(err, transport))
		if *save != "" {
			reportSave(stderr, *save, transport, false)
		}
		return 1
	}
	fmt.Fprint(stdout, formatted.Text)
	name := mcpclient.SafeForTerminal(result.ServerName)
	if name == "" {
		name = "mcp"
	}
	fmt.Fprintf(stderr, "[%s: %d инструментов · tools %d байт · ответ %d байт · %.2f s]\n", name, len(result.Tools.Tools), formatted.ToolsBytes, formatted.ResponseBytes, time.Since(started).Seconds())
	if *save != "" && !reportSave(stderr, *save, transport, true) {
		return 1
	}
	return 0
}

// reportSave writes the capture and returns whether it succeeded. The wording depends on
// what actually happened: claiming "список получен" after a failed connection would be a
// false statement printed right under the true one.
func reportSave(stderr io.Writer, path string, transport mcpclient.Transport, listed bool) bool {
	err := saveCapture(path, transport)
	if err == nil {
		return true
	}
	prefix := "raw-обмен сохранить не удалось"
	if listed {
		prefix = "список получен, но сохранить raw-обмен не удалось"
	}
	fmt.Fprintf(stderr, "%s (%q): %s\n", prefix, path, oneLine(err.Error()))
	return false
}

func saveCapture(path string, transport mcpclient.Transport) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return mcpclient.WriteCapture(file, transport)
}

func readableError(err error, transport mcpclient.Transport) string {
	if err == nil {
		return "ошибка MCP-клиента"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		return "таймаут подключения или tools/list"
	}
	if strings.Contains(err.Error(), "ответ tools/list") || strings.Contains(err.Error(), "raw-ответ tools/list") {
		return err.Error()
	}
	// One line, but it carries the cause: "не удалось подключиться" alone cannot tell a
	// closed port from an unresolvable host, and the demo is recorded live.
	if transport.Command != "" {
		return "не удалось запустить stdio MCP-сервер: " + oneLine(err.Error())
	}
	return "не удалось подключиться к MCP-серверу: " + oneLine(err.Error())
}

// oneLine keeps a multi-line wrapped error readable as a single stderr line.
func oneLine(message string) string {
	return strings.Join(strings.Fields(message), " ")
}
