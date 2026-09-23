package watchmcp

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

func RunScheduler(ctx context.Context, scheduler *watch.Scheduler, every time.Duration, errorsOut io.Writer) {
	run := func() {
		if _, err := scheduler.RunOnce(ctx); err != nil && errorsOut != nil {
			fmt.Fprintln(errorsOut, "планировщик:", mcpclient.SafeForTerminal(err.Error()))
		}
	}
	run()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
