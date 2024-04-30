# App Platform Review Apps

This is a PoC-implementation of a Review Apps functionality, completely externally orchestrateable by the user. This could be used by any customer of us without implementing anything on our end.

## How it works

This sets up a Github App that essentially listens for pull-request related events on repositories authorized through it. It'll then create a new app per opened pull-request and create a Deployment in Github that it updates with the status and eventually the public link to the App Platform deployment. On a push to the pull-request, the app is updated and a new Deployment is created. When the pull-request is merged or closed, the app is deleted.

It is expected that the repository defines a valid app spec at `.do/app.yaml` and that the pull-request is not created from a forked repository but a branch of the repository itself for safety reasons.

## Setup

This expects a Github App setup, so first, create a Github App, pointing to the service hosted herein. The [Github App Quickstart Guide](https://docs.github.com/en/apps/creating-github-apps/writing-code-for-a-github-app/quickstart) is very handy in setting this up locally.

### Needed Permissions

- **Contents**: `Read-only`
- **Deployments**: `Read-and-write`
- **Pull requests**: `Read-only`

### Needed event subscriptions

- Pull request

### Configuration

The service can be configured by creating a `config.yml` with the following contents:

```yaml
server:
  address: "127.0.0.1"
  port: 8080

do:
  token: $DO_API_TOKEN

github:
  v3_api_url: "https://api.github.com/"
  app:
    integration_id: $GITHUB_APP_ID
    webhook_secret: $GITHUB_WEBHOOK_SECRET
    private_key: |
      $GITHUB_PRIVATE_KEY

```