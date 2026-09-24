package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watchmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-18-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storePath := fs.String("store", "day-18/state/store.json", "путь к JSON-хранилищу")
	checkEvery := fs.Duration("check-every", 30*time.Second, "частота проверки расписания")
	once := fs.Bool("once", false, "выполнить один проход без MCP")
	guest := fs.Bool("guest", false, "ограничить изменения гостевым режимом")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *checkEvery < time.Second {
		fmt.Fprintln(stderr, "usage: day-18-mcp [-store путь] [-check-every 30s] [-once] [-guest]; -check-every должен быть не меньше 1s")
		return 2
	}
	store := watch.NewStore(*storePath)
	if *once {
		scheduler := &watch.Scheduler{Store: store, Output: stdout}
		count, err := scheduler.RunOnce(context.Background())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if count == 0 {
			fmt.Fprintln(stdout, "просроченных наблюдений нет")
		}
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := &watch.Scheduler{Store: store, Output: stderr}
	go watchmcp.RunScheduler(ctx, scheduler, *checkEvery, stderr)
	server := watchmcp.NewServer(watchmcp.Options{Store: store, Guest: *guest})
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
