# Mercator console demo - regenerate with:
#
#   docker build -t mercator:local .        # broker image (see README quickstart)
#   go build -o mercator ./cmd/mercator      # CLI on $PWD (used by this tape)
#   vhs docs/assets/mercator-demo.tape
#
# Mirrors the README "Try It In 5 Minutes" Docker quickstart: bring the broker
# up in Docker, create a digest-pinned run, read the terminal result, then ask
# why it landed where it did. Requires a running Docker daemon.

Output docs/assets/output/mercator-demo.gif
Output docs/assets/output/mercator-demo.webm
Output docs/assets/output/mercator-demo.mp4

Set Shell "bash"
Set FontSize 20
Set Width 1560
Set Height 800
Set Padding 24
Set TypingSpeed 22ms
Set Theme "Catppuccin Mocha"

# --- off-camera setup: clean prompt (hides cwd/hostname), CLI on PATH, fresh state ---
Hide
Type `export PS1='$ '; export PATH="$PWD:$PATH"; export MERCATOR_API_URL=http://127.0.0.1:8080 MERCATOR_API_TOKEN=dev-token MERCATOR_WORKSPACE_ID=ws_1`
Enter
Type `docker rm -f mercator >/dev/null 2>&1; docker pull -q busybox:latest >/dev/null; clear`
Enter
Sleep 1s
Show

# --- 1. bring the broker up in Docker (detached for the demo) ---
Type `# 1. Mercator runs your workload as a real Docker container - start the broker`
Enter
Sleep 500ms
Type `docker run -d --name mercator -p 8080:8080 \`
Enter
Type `  -e MERCATOR_ADAPTER=docker -e MERCATOR_DOCKER_ARCH=amd64 -e MERCATOR_ADDR=0.0.0.0:8080 \`
Enter
Type `  -e MERCATOR_API_TOKEN=dev-token -e MERCATOR_AUTH_WORKSPACES=ws_1 \`
Enter
Type `  -v /var/run/docker.sock:/var/run/docker.sock mercator:local serve`
Enter
Sleep 2500ms
Type `curl -fsS $MERCATOR_API_URL/health/ready | jq .`
Enter
Sleep 1200ms

# --- 2. create a digest-pinned run and read the result (mercator CLI) ---
Type `# 2. Create a run. The Docker adapter requires a digest-pinned image, so pin one:`
Enter
Sleep 500ms
Type `IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"`
Enter
Sleep 400ms
Type `RUN_ID=$(mercator run create "$IMAGE" -- echo hi | jq -r .run.id)`
Enter
Sleep 1200ms
Type `mercator run get --run-id "$RUN_ID" \`
Enter
Type `  | jq '{outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'`
Enter
Sleep 2000ms

# --- 3. why did it land there? ---
Type `# 3. The point of a broker is the placement decision - ask why it landed here:`
Enter
Sleep 500ms
Type `mercator run decision --run-id "$RUN_ID" \`
Enter
Type `  | jq '{selected: .decision.selected_offer_snapshot_id, candidate_count: (.decision.candidates | length)}'`
Enter
Sleep 3000ms

Hide
Type `docker rm -f mercator >/dev/null 2>&1`
Enter
Show
