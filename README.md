# gcb2gh

If you're both a Google Cloud Build and GitHub user then you will either be:

- resigned to the "summary" and multiple clicks of using Google Cloud Build's
  GitHub app; or
- manually making API calls to GitHub from within build steps to provide more
  detail.

gcb2gh solves this with a build step in your cloudbuild.yaml which sends status
updates to GitHub for each step that starts or stops in Google Cloud Build. The
status links directly to the running or failed steps.

gcb2gh runs in a detached Docker container - putting itself in the background -
and connects to the [Docker API event
stream](https://docs.docker.com/engine/api/v1.41/#operation/SystemEvents) which
it translates into updates to [GitHub repo
statuses](https://docs.github.com/en/rest/reference/repos#create-a-commit-status).

At ravelin.com (github.com/unravelin) we use gcb2gh alongside the [GCB GitHub
app](https://github.com/marketplace/google-cloud-build), which is able to report
when a build fails to start (such as with an invalid build YAML) or when a build
gets cancelled. gcb2gh runs inside the build so cannot tell you this.

## Usage

### 1. Build your own gcb2gh image

Like with the [community
cloud-builders](https://github.com/GoogleCloudPlatform/cloud-builders-community),
you must build and host your own gcb2gh container image on the Google Container
Registry. To do so, clone this repo and submit the directory as a build.

The following will create the image gcr.io/MY-PROJECT/gcb2gh for you to use in a
build step:

```
git clone https://github.com/unravelin/gcb2gh
cd gcb2gh
gcloud --project MY-PROJECT builds submit . --substitutions _GCR_HOST=gcr.io
```

### 2. Add the gcb2gh build step to your build manifest

The following example build step runs gcb2gh with the configuration envvars:

- GITHUB_USER and GITHUB_REPO: Pointing to github.com/unravelin/gcb2gh.

- GITHUB_TOKEN: Read from a github-token secret.

- BUILD_MANIFEST: Pointing to /workspace/cloudbuild.yaml so that we can read the
  step IDs. Note that we have to explicitly mount /workspace for gcb2gh as it is
  not a build step, so GCB won't mount it automatically.

- BUILD_ID, PROJECT_ID, and COMMIT_SHA: all taken from [built-in
  substitutions](https://cloud.google.com/build/docs/configuring-builds/substitute-variable-values)
  of the same name.

If you built your gcb2gh image to a different project or gcr.io host, be sure
that the last line is pointing to the correct image.

Placing the gcb2gh step at the end ensures we don't cause other steps to wait
while the gcb2gh Docker image is downloaded.

```yaml
availableSecrets:
  secretManager:
  - versionName: projects/$PROJECT_ID/secrets/github-token/versions/1
    env: 'GITHUB_TOKEN'

steps:
  ...

  # At the end, to ensure we don't block other steps.
  - id: gcb2gh
    waitFor: ["-"]
    name: "gcr.io/cloud-builders/docker"
    secretEnv: [GITHUB_TOKEN]
    args: [
      "run", "--name", "gcb2gh", "--detach",
      # Configure: the GitHub repo.
      "--env", "GITHUB_TOKEN",
      "--env", "GITHUB_USER=unravelin",
      "--env", "GITHUB_REPO=gcb2gh",
      # Configure: the build manifest.
      "--mount", "type=bind,source=/workspace,target=/workspace,bind-propagation=rprivate",
      "--env", "BUILD_MANIFEST=/workspace/cloudbuild.yaml",
      # GCB specifics.
      "--env", "BUILD_ID=$BUILD_ID",
      "--env", "PROJECT_ID=$PROJECT_ID",
      "--env", "COMMIT_SHA=$COMMIT_SHA",
      "--mount", "type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock,bind-propagation=rprivate",
      "gcr.io/$PROJECT_ID/gcb2gh",
    ]

  # Optional step to debug gcb2gh.
  - id: gcb2gh_logs
    name: "gcr.io/cloud-builders/docker"
    args: [logs, gcb2gh]
```

### 3. Configure further as needed

The full set of support environment variables are:

- PROJECT_ID: The GCB Project ($PROJECT_ID substitution).

- BUILD_ID: The GCB Build ID ($BUILD_ID substitution).

- COMMIT_SHA: The Git commit SHA of the code we're building ($COMMIT_SHA
  substitution).

- DOCKER_HOST: The docker daemon to connect to. Defaults to
  unix:///var/run/docker.sock as used in GCB.

- GITHUB_API: The GitHub API URL. Defaults to https://api.github.com.

- GITHUB_TOKEN: The GitHub API authentication Token in the form "user:pass",
  ":pass" or just "pass".

- GITHUB_USER: The user in https://github.com/user/repo.

- GITHUB_REPO: The repo in https://github.com/user/repo.

- STATUS_CONTEXT: The title given to the Commit Status at the bottom of PRs.
  Defaults to "gcb".

- BUILD_MANIFEST: The filepath of the GCB build manifest which we read to get
  pretty step names. You will need to ensure the directory is mounted into the
  background container. Steps will be "step_1" to "step_n" in the commit status
  if a build manifest cannot be read.
