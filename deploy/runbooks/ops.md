# Operations log

The repository is public. What an operator writes while running an instance stays out of it:
the go-live checklist, incidents and breaches, break-glass uses, restore tests and rollbacks.
These reveal hosts, timings and weaknesses before they are fixed.

They go in a **private operations repository** owned by the instance's owner (for example
`<owner>/tenure-ops`), as issues with the labels the runbooks name: `go-live`, `incident`,
`break-glass`, `restore-test`, `rollback`. Only the owner and the people who run the instance
have access to it.

Rules:

- Never a secret, even there: the safe holds them (`break-glass.md`).
- A fix that comes out of an incident is opened here, in the public repository, only once it
  reveals nothing exploitable; until then it stays in the operations repository, or in a
  private security advisory of this one.
- A vulnerability found in the code is reported as `SECURITY.md` says, not in a public issue.
