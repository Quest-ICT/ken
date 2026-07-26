# Security policy

## Reporting a vulnerability

Please report security issues through GitHub's **private vulnerability reporting**:

> **Security** → **Report a vulnerability** on this repository.

That opens a private channel visible only to the maintainers — please use it rather
than a public issue, so the problem can be fixed before it is widely known.

Include what you need to make the issue reproducible: affected version
(`ken version`), configuration that matters (TLS mode, OAuth on/off, whether it runs
behind a proxy), and the steps or request that trigger it.

Ken is maintained alongside other work, so there is no guaranteed response window —
but a security report goes to the front of the queue, ahead of features and bug
reports, and you will get an acknowledgement once a maintainer has picked it up. If a
fix is warranted it ships in a patch release, and the advisory credits you unless
you'd rather stay anonymous.

## Supported versions

Fixes land on the latest minor release. Older minors are not backported.

## Scope

Ken is a self-hosted service, so the interesting surfaces are:

- the MCP endpoint and its bearer-token / OAuth authentication
- the curator web UI: session handling, CSRF, the first-run setup gate
- the optional OAuth 2.1 authorization server
- privilege boundaries in the installer and the remote-upgrade wrapper
- anything that lets an **agent token** reach a curation action (agent tokens must
  never be able to promote — that exclusion is the product's core guarantee)

Out of scope: findings that require a host the reporter already controls as root, and
denial of service by brute resource exhaustion against an unprotected instance
(rate limiting is on by default and tunable, but Ken is not a DDoS mitigation layer).
