package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
	"github.com/google/go-github/v60/github"
	"github.com/palantir/go-githubapp/githubapp"
	"sigs.k8s.io/yaml"
)

const (
	canonicalAppSpecLocation = ".do/app.yaml"

	actionOpened      = "opened"
	actionReopened    = "reopened"
	actionClosed      = "closed"
	actionSynchronize = "synchronize"

	deploymentStateInactive = "inactive"
	deploymentStateSuccess  = "success"
	deploymentStateError    = "error"
)

type deploymentPayload struct {
	AppID string `json:"app_id"`
}

type PRHandler struct {
	cc githubapp.ClientCreator
	do *godo.Client
}

func (h *PRHandler) Handles() []string {
	return []string{"pull_request"}
}

func (h *PRHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	var event github.PullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("failed to parse pull request event: %w", err)
	}

	switch event.GetAction() {
	case actionOpened, actionReopened, actionClosed, actionSynchronize:
	default:
		// Short-circuit for all the actions we don't want to deal with.
		return nil
	}

	repo := event.GetRepo()
	prNum := event.GetNumber()
	installationID := githubapp.GetInstallationIDFromEvent(&event)
	ctx, logger := githubapp.PreparePRContext(ctx, installationID, repo, prNum)
	logger = logger.With().Str("github_event_action", event.GetAction()).Logger()

	if repo.GetID() != event.GetPullRequest().GetHead().GetRepo().GetID() {
		logger.Warn().Msg("pull requests of forked repositories are not allowed")
		return nil
	}

	repoOwner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	prBranch := event.GetPullRequest().GetHead().GetRef()

	// TODO: The 32 char limit pretty narrow here. Maybe we should compute a hash?
	appName := fmt.Sprintf("%s-%s-%d", repoOwner, repoName, prNum)

	client, err := h.cc.NewInstallationClient(installationID)
	if err != nil {
		return fmt.Errorf("failed to create installation client: %w", err)
	}

	logger = logger.With().
		Str("github_event_action", event.GetAction()).
		Str("app_name", appName).
		Logger()

	waitAndPropagate := func(appID, deploymentID string, ghDeploymentID int64) error {
		d, err := h.waitForDeploymentTerminal(ctx, appID, deploymentID)
		if err != nil {
			return fmt.Errorf("failed to wait deployment to finish: %w", err)
		}

		if d.Phase != godo.DeploymentPhase_Active {
			_, _, err = client.Repositories.CreateDeploymentStatus(ctx, repoOwner, repoName, ghDeploymentID, &github.DeploymentStatusRequest{
				State:        ptr(deploymentStateError),
				AutoInactive: ptr(true),
			})
			if err != nil {
				return fmt.Errorf("failed to update deployment with failure: %w", err)
			}
			return nil
		}

		app, err := h.waitForAppLiveURL(ctx, appID)
		if err != nil {
			return fmt.Errorf("failed to wait for app to have a live URL: %w", err)
		}

		_, _, err = client.Repositories.CreateDeploymentStatus(ctx, repoOwner, repoName, ghDeploymentID, &github.DeploymentStatusRequest{
			State:          ptr(deploymentStateSuccess),
			EnvironmentURL: ptr(app.LiveURL),
			AutoInactive:   ptr(true),
		})
		if err != nil {
			return fmt.Errorf("failed to update deployment: %w", err)
		}
		return nil
	}

	if event.GetAction() == actionClosed || event.GetAction() == actionSynchronize {
		deployments, _, err := client.Repositories.ListDeployments(ctx, repoOwner, repoName, &github.DeploymentsListOptions{
			Environment: appName,
		})
		if err != nil {
			return fmt.Errorf("failed to list deployments: %w", err)
		}
		if len(deployments) == 0 {
			// No existing deployments. Nothing to do.
			return nil
		}
		deployment := deployments[0]

		var payload deploymentPayload
		if err := json.Unmarshal(deployment.Payload, &payload); err != nil {
			return fmt.Errorf("failed to parse deployment payload: %w", err)
		}

		if event.GetAction() == actionClosed {
			logger.Info().Msg("deleting app as the PR was closed")
			_, err = h.do.Apps.Delete(ctx, payload.AppID)
			if err != nil {
				return fmt.Errorf("failed to delete app: %w", err)
			}

			_, _, err = client.Repositories.CreateDeploymentStatus(ctx, repoOwner, repoName, deployments[0].GetID(), &github.DeploymentStatusRequest{
				State:        ptr(deploymentStateInactive),
				AutoInactive: ptr(true),
			})
			if err != nil {
				return fmt.Errorf("failed to update deployment: %w", err)
			}
		} else if event.GetAction() == actionSynchronize {
			logger.Info().Msg("redeploying app after change")
			// TODO: Should we figure out if the AppSpec changed and update? Should we just
			// always use "UpdateApp"?
			d, _, err := h.do.Apps.CreateDeployment(ctx, payload.AppID)
			if err != nil {
				return fmt.Errorf("failed to create deployment: %w", err)
			}

			ghDeployment, _, err := client.Repositories.CreateDeployment(ctx, repoOwner, repoName, &github.DeploymentRequest{
				Ref:              &prBranch,
				AutoMerge:        ptr(false),
				Environment:      ptr(appName),
				RequiredContexts: ptr([]string{}),
				Payload:          deploymentPayload{AppID: payload.AppID},
			})
			if err != nil {
				return fmt.Errorf("failed to create deployment: %w", err)
			}

			if err := waitAndPropagate(payload.AppID, d.GetID(), ghDeployment.GetID()); err != nil {
				return fmt.Errorf("failed to propagate deployment status: %w", err)
			}
		}
		return nil
	}

	// Fetch the app spec from the respective branch.
	appSpecFile, _, _, err := client.Repositories.GetContents(ctx, repoOwner, repoName, canonicalAppSpecLocation, &github.RepositoryContentGetOptions{
		Ref: prBranch,
	})
	if err != nil {
		return fmt.Errorf("failed to fetch app spec: %w", err)
	}
	appSpec, err := appSpecFile.GetContent()
	if err != nil {
		return fmt.Errorf("failed to get app spec content: %w", err)
	}
	var spec godo.AppSpec
	if err := yaml.Unmarshal([]byte(appSpec), &spec); err != nil {
		return fmt.Errorf("failed to parse app spec: %w", err)
	}

	// Override app name to something that identifies this PR.
	spec.Name = appName

	// Unset any domains as those might collide with production apps.
	spec.Domains = nil

	// Unset any alerts as those will be delivered wrongly anyway.
	spec.Alerts = nil

	// Override the reference of all relevant components to point to the PRs ref.
	var githubRefs []*godo.GitHubSourceSpec
	for _, svc := range spec.GetServices() {
		if svc.GetGitHub() != nil {
			githubRefs = append(githubRefs, svc.GetGitHub())
		}
	}
	for _, worker := range spec.GetWorkers() {
		if worker.GetGitHub() != nil {
			githubRefs = append(githubRefs, worker.GetGitHub())
		}
	}
	for _, job := range spec.GetJobs() {
		if job.GetGitHub() != nil {
			githubRefs = append(githubRefs, job.GetGitHub())
		}
	}
	for _, ref := range githubRefs {
		if ref.Repo != fmt.Sprintf("%s/%s", repoOwner, repoName) {
			// Skip Github refs pointing to other repos.
			continue
		}
		// We manually kick new deployments so we can watch their status better.
		ref.DeployOnPush = false
		ref.Branch = prBranch
	}

	logger.Info().Msg("creating new app")
	app, _, err := h.do.Apps.Create(ctx, &godo.AppCreateRequest{
		Spec: &spec,
	})
	if err != nil {
		return fmt.Errorf("failed to create app: %w", err)
	}

	ghDeployment, _, err := client.Repositories.CreateDeployment(ctx, repoOwner, repoName, &github.DeploymentRequest{
		Ref:              &prBranch,
		AutoMerge:        ptr(false),
		Environment:      ptr(appName),
		RequiredContexts: ptr([]string{}),
		Payload:          deploymentPayload{AppID: app.ID},
	})
	if err != nil {
		return fmt.Errorf("failed to create deployment: %w", err)
	}

	ds, _, err := h.do.Apps.ListDeployments(ctx, app.GetID(), &godo.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	if err := waitAndPropagate(app.GetID(), ds[0].GetID(), ghDeployment.GetID()); err != nil {
		return fmt.Errorf("failed to propagate deployment status: %w", err)
	}

	return nil
}

// waitForDeploymentTerminal waits for the given deployment to be in a terminal state.
func (h *PRHandler) waitForDeploymentTerminal(ctx context.Context, appID, deploymentID string) (*godo.Deployment, error) {
	t := time.NewTicker(2 * time.Second)

	var d *godo.Deployment
	for !isInTerminalPhase(d) {
		var err error
		d, _, err = h.do.Apps.GetDeployment(ctx, appID, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return d, nil
}

// waitForAppLiveURL waits for the given app to have a non-empty live URL.
func (h *PRHandler) waitForAppLiveURL(ctx context.Context, appID string) (*godo.App, error) {
	t := time.NewTicker(2 * time.Second)

	var a *godo.App
	for a.GetLiveURL() == "" {
		var err error
		a, _, err = h.do.Apps.Get(ctx, appID)
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return a, nil
}

// isInTerminalPhase returns whether or not the given deployment is in a terminal phase.
func isInTerminalPhase(d *godo.Deployment) bool {
	switch d.GetPhase() {
	case godo.DeploymentPhase_Active, godo.DeploymentPhase_Error, godo.DeploymentPhase_Canceled, godo.DeploymentPhase_Superseded:
		return true
	}
	return false
}

func ptr[T any](v T) *T {
	return &v
}
