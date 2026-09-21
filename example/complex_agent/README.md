# Complex OpenAI agent example

This example runs a realistic, read-only incident investigation with an OpenAI Responses model. It demonstrates:

- four custom tools with JSON Schema parameters;
- run-scoped region metadata available inside tools;
- built-in task planning and plan updates;
- parallel tool execution with a concurrency limit;
- precise context compression;
- before/after execution callbacks;
- streamed final-answer output and token accounting.

The operational data is deterministic and embedded in the example. No production service is queried or modified.

## Run

```bash
export OPENAI_API_KEY="your-api-key"
export OPENAI_MODEL="gpt-5.2" # optional; gpt-5.2 is the default

go run ./example/complex_agent
```

Use a custom question or timeout:

```bash
go run ./example/complex_agent \
  -question "Investigate the EU checkout errors and recommend a safe mitigation." \
  -timeout 2m
```

`OPENAI_BASE_URL` can optionally point to an OpenAI-compatible Responses API endpoint.

The agent is intentionally given only read-only tools. Its final response should distinguish observed evidence from hypotheses and recommend, rather than claim to execute, mitigations.
