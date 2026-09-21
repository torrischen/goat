# Security Policy

## Supported versions

Until the project publishes stable releases, security fixes are applied to the default branch. After versioned releases begin, this table will be updated with the supported release lines.

| Version | Supported |
| --- | --- |
| Default branch | Yes |
| Older commits and unmaintained forks | No |

## Reporting a vulnerability

Please do not report suspected vulnerabilities in a public issue, discussion, or pull request.

Use GitHub's private vulnerability reporting for this repository:

<https://github.com/torrischen/goat/security/advisories/new>

Include, when possible:

- the affected package, version, or commit;
- impact and realistic attack scenario;
- reproduction steps or a minimal proof of concept;
- suggested mitigations;
- whether the issue is already public.

Do not include real provider credentials, personal data, or production conversation content. Use redacted examples and test credentials.

Maintainers will acknowledge the report, investigate it, and coordinate a fix and disclosure. Response and remediation time depend on severity and complexity. Please allow maintainers a reasonable opportunity to release a fix before public disclosure.

## Security considerations for users

Model output and model-generated tool arguments are untrusted input. Applications using goat should validate tool parameters, enforce authorization and idempotency, use timeouts, and sandbox privileged operations. In particular, terminal and shell tools execute with the permissions of the host process.

Keep provider credentials out of source control, logs, tool output, and persisted conversation context. Use least-privilege credentials and rotate any credential that may have been exposed.
