# Governance

AIJIASU2API is a **single-maintainer project**. This document states how decisions are made so
that contributors know what to expect. It is intentionally short; it is not a corporate charter.

## Model

The project uses a **Benevolent Dictator For Life (BDFL)** model:

- The maintainer is `dwgx` (`@dwgx` on GitHub), the copyright holder named in [LICENSE](LICENSE).
- The maintainer has final authority over scope, design, releases, and access.
- There is no voting body, no steering committee, and no roadmap referendum.
- Decisions are explained in the issue or pull request where they are made. A decision may still
  be "no" after the reasoning is given.

This model fits a research project with a narrow boundary. It is not a claim that the maintainer
is always right; it is a statement of who is accountable.

## Responsibilities

The maintainer:

- triages issues and security reports;
- reviews and merges pull requests, or closes them with a reason;
- owns the release process in [PROTOCOL.md](PROTOCOL.md) and the version index in
  [CHANGELOG.md](CHANGELOG.md);
- keeps [SECURITY.md](SECURITY.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and
  [.github/CODEOWNERS](.github/CODEOWNERS) current;
- decides what is in scope, including declining work that is technically correct but outside the
  project's boundary.

The maintainer does not provide an SLA, private support, or a guarantee of response time beyond
the security best effort stated in [SECURITY.md](SECURITY.md).

## Decisions

- **Small fixes and docs** may be merged directly after review.
- **Behaviour changes, API changes, and new surfaces** need an issue first. The issue states the
  problem, the proposed change, and the non-goals. The maintainer accepts, declines, or asks for a
  smaller slice before code is written.
- **Security-sensitive changes** follow [SECURITY.md](SECURITY.md). A fix may be prepared and
  released privately before the issue is public.
- **Disagreements** are resolved by the maintainer after hearing the argument. Repeating a closed
  argument without new evidence is not a path to reopening it.

## Pull requests

Every pull request is reviewed against [CONTRIBUTING.md](CONTRIBUTING.md). A green CI run is
necessary but not sufficient: it proves the tests passed, not that the change is in scope or that
the boundary is correct.

The maintainer may push small fixups to a contributor's branch when that is faster than another
review round, and will say so. Force-pushing a contributor's branch without notice is not done.

## Releases

The maintainer is the only person who creates release tags. The procedure, tag semantics, and
verification gate are in [PROTOCOL.md](PROTOCOL.md). A release is identified by its tag and its
attached artifacts, not by a README claim.

## Security and conduct

Security reports and conduct incidents are handled privately, by the maintainer, through the
channels in [SECURITY.md](SECURITY.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Do not open a
public issue for either.

## Becoming a maintainer

There is no application process. The maintainer may invite a contributor who has consistently
supplied reviewed, in-scope changes and has shown good judgement about the project's boundary.
Invitation is discretionary; there is no promise that it will happen, and no contributor is
entitled to it.

## Changes to this document

The maintainer may amend this document. Material changes are made in a pull request with the
reasoning in the description, so the history is visible. This document does not override
[LICENSE](LICENSE) or [DISCLAIMER.md](DISCLAIMER.md).

## Scope of governance

This governance covers the repository and its community spaces. It does not govern the vendor's
service, any operator-run demo, or any fork. Operators of self-hosted instances set their own
policies and are responsible for their own users.
