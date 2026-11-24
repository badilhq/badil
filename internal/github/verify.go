package github

import (
	"context"
	"fmt"
	"io"
)

// VerifyApp checks app's credentials with GET /app, writes the App's identity
// to w, then lists every installation reachable with them: the IDs
// GITHUB_INSTALLATION_ID takes.
func VerifyApp(ctx context.Context, app *App, w io.Writer) error {
	client := app.Client(app.AppsTransport())

	ghApp, _, err := client.Apps.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("GET /app: %w", err)
	}
	fmt.Fprintln(w, "App credentials OK")
	fmt.Fprintf(w, "  id:    %d\n", ghApp.GetID())
	fmt.Fprintf(w, "  slug:  %s\n", ghApp.GetSlug())
	fmt.Fprintf(w, "  name:  %s\n", ghApp.GetName())
	fmt.Fprintf(w, "  owner: %s\n", ghApp.GetOwner().GetLogin())
	fmt.Fprintf(w, "  url:   %s\n", ghApp.GetHTMLURL())

	insts, _, err := client.Apps.ListInstallations(ctx, nil)
	if err != nil {
		return fmt.Errorf("list installations: %w", err)
	}
	fmt.Fprintf(w, "\nInstallations (%d):\n", len(insts))
	if len(insts) == 0 {
		fmt.Fprintln(w, "  (none — install the App on an account before running `badil serve`)")
	}
	for _, ins := range insts {
		fmt.Fprintf(w, "  - id=%d  account=%s (%s)\n",
			ins.GetID(), ins.GetAccount().GetLogin(), ins.GetAccount().GetType())
	}
	return nil
}
