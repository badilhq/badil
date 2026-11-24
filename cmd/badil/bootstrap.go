package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/badilhq/badil/internal/config"
	"github.com/badilhq/badil/internal/github/manifest"
)

// bootstrapAppCmd creates a GitHub App via the manifest flow and writes its
// credentials to dev.private-key.pem and .env in the current working
// directory. The App's webhook URL comes from --webhook-url, or else from a
// running ngrok tunnel on :8080. The App is created on the GitHub host of
// GITHUB_URL and GITHUB_API_URL (config.LoadGitHubURLs). manifest.Run runs
// the flow.
//
// Documented at https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest.
func bootstrapAppCmd() *cobra.Command {
	var (
		appName       string
		gitopsRepoURL string
		envPath       string
		pemPath       string
		owner         string
		webhookURL    string
		assumeYes     bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap-app",
		Short: "Create a GitHub App via manifest flow and write .env + PEM",
		Long: `Create a GitHub App through the manifest flow.

Prints a preflight plan describing what will be created, what files will
be written, and what will be overwritten — then prompts for confirmation
before doing anything (skip with --yes).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			githubURL, githubAPIURL, err := config.LoadGitHubURLs()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), manifest.Timeout)
			defer cancel()
			return manifest.Run(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), manifest.Options{
				AppName:       appName,
				GitopsRepoURL: gitopsRepoURL,
				EnvPath:       envPath,
				PEMPath:       pemPath,
				Owner:         owner,
				WebhookURL:    webhookURL,
				AssumeYes:     assumeYes,
				GitHubURL:     githubURL,
				GitHubAPIURL:  githubAPIURL,
			})
		},
	}
	cmd.Flags().StringVar(&appName, "name", manifest.DefaultAppName(), "App name (must be globally unique)")
	cmd.Flags().StringVar(&gitopsRepoURL, "gitops-repo-url", "",
		"GITOPS_REPO_URL value to write into .env (the gitops repo's clone URL)")
	cmd.Flags().StringVar(&envPath, "env-file", ".env", "Path to write the .env file")
	cmd.Flags().StringVar(&pemPath, "pem-file", "dev.private-key.pem", "Path to write the App private key")
	cmd.Flags().StringVar(&owner, "owner", "", "Org login to install the App under (empty = personal account)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "",
		"Public base URL of badil; /webhooks/github is appended (default: the ngrok tunnel)")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}
