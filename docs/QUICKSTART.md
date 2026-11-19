# Quickstart

The quickstart runs a first badil preview on your machine: no cloud cluster, no public URL. It takes about 5 minutes once the images are downloaded.

## What it sets up

- A local k3d cluster.
- Argo CD and its preview wiring.
- badil, from its Helm chart.
- Your own public gitops repo, created from [`badilhq/gitops-template`](https://github.com/badilhq/gitops-template) and filled with a four-service demo catalog.
- One `POST /v1/deploy` call that pins `backend-1` at the head of an open demo pull request, so the preview runs that version next to the other services at `main`.

## What you do by hand

- Create the gitops repo from the template.
- Create the GitHub App, with its webhook off.
- Install the App on the gitops repo.

The rest is a walkthrough of copy-paste commands, or the `up.sh` script that runs the same steps.

## Prerequisites

- `docker`, `k3d`, `kubectl`, `helm`, `gh`, `curl`, `openssl` and `git`.
- Host ports 80 and 443 free.

The walkthrough is in [`badilhq/quickstart`](https://github.com/badilhq/quickstart).

## Next

Follow the [Install](INSTALL.md) guide to run badil on a real cluster, with the label-a-pull-request loop.
