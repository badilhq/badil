package github

import (
	"context"
	"fmt"
)

// CheckApp checks app's credentials and its installation installationID:
// GET /app with the App JWT, then an installation token for installationID.
// The first failure is returned with the GitHub error.
func CheckApp(ctx context.Context, app *App, installationID int64) error {
	if _, _, err := app.Client(app.AppsTransport()).Apps.Get(ctx, ""); err != nil {
		return fmt.Errorf("GET /app: %w", err)
	}
	if _, err := app.InstallationTransport(installationID).Token(ctx); err != nil {
		return fmt.Errorf("installation %d token: %w", installationID, err)
	}
	return nil
}
