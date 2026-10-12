package menu

import (
	"context"
	"io"

	"cf-san/internal/autoupdate"
	"cf-san/internal/config"
)

func updateLatestFromWorker(ctx context.Context, cfg config.Config, output io.Writer) error {
	return autoupdate.InstallFromWorker(ctx, cfg, "", output)
}
