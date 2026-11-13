# Load testing

[`journey.js`](journey.js) is a [k6](https://k6.io/) script that simulates a
student's session: register (first iteration) and sign in, save the profile,
read the dashboard, record an expense and, optionally, ask the assistant
(`POST /assistant/ask`) for a budget overview. It ramps from 100 to 200 virtual
users over about nine minutes.

## Before you run it

- Use a dedicated environment with `AUTH_MODE=local`. Do not enable local
  sign-in on a production deployment.
- Point the backend at `cmd/mock-llm` rather than a real model when the agent
  is included.
- Each virtual user registers `lt_<RUN_ID>_<n>` and keeps writing
  transactions. Nothing is deleted; reset the test database afterwards. A
  summary is written to `loadtest/results/<RUN_ID>.summary.json`.

## Run

From the repository root, with k6 installed:

```bash
mkdir -p loadtest/results
export BASE_URL=http://localhost:8090
export RUN_ID=local01                       # new value for every run
export LOADTEST_PASSWORD='<dedicated test password>'
export ALLOW_LOAD_TEST=yes                  # explicit opt-in
export INCLUDE_ASSISTANT=false              # true to include an agent turn

k6 run --stage 10s:1 loadtest/journey.js    # smoke run with one user
k6 run --out json=loadtest/results/$RUN_ID.samples.json loadtest/journey.js
```

With `INCLUDE_ASSISTANT=true` each iteration waits for the SSE `done` event, so
the agent timing is total turn latency, not time to first token.

## Reporting results

Record the commit SHA, time, load-generator machine and network location,
environment size, dataset, mock-model settings, request count, throughput,
P50/P95/P99 latency and error rate, plus server-side CPU, memory and task count
for the same window.
