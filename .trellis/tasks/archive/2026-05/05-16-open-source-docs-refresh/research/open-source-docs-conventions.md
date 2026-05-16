# Research: Open-source docs conventions

- **Query**: Research common documentation organization conventions for mature open-source self-hosted web applications / infrastructure tools. Compare 2-4 examples or common patterns for README vs docs/ split, deployment docs, admin guides, developer/contributor docs, and handling archived/process docs.
- **Scope**: external
- **Date**: 2026-05-16

## Findings

### Files Found

| Project | File Path / URL | Description |
|---|---|---|
| Grafana | `grafana/grafana/README.md` | Repository landing page: product summary, feature bullets, get-started links, docs link, contributor links, community links, license. |
| Grafana | `grafana/grafana/docs/sources/` | Product documentation tree with top-level sections such as setup, administration, troubleshooting, tutorials, upgrade guide, developer resources. |
| Grafana | `grafana/grafana/docs/sources/setup-grafana/` | Deployment/setup docs: installation, Docker configuration, HTTPS, HA, monitoring, start/restart, sign-in. |
| Grafana | `grafana/grafana/docs/sources/administration/` | Operator/admin docs: backup, CLI, migration, provisioning, plugin management, roles/permissions, service accounts, user/team/org management. |
| Grafana | `grafana/grafana/contribute/` | Contributor/process docs separate from product docs: developer guide, PR creation, issue triage, deprecation policy, style guides, engineering docs. |
| Grafana | `grafana/grafana/.changelog-archive/`, `grafana/grafana/docs/sources/upgrade-guide/` | Archived changelog content and versioned upgrade guides kept outside README. |
| NetBox | `netbox-community/netbox/README.md` | Repository landing page: product positioning, screenshots, role/why sections, getting-started links, community/contribution links. |
| NetBox | `netbox-community/netbox/docs/` | In-repository documentation site source with `index.md` and major sections: installation, getting-started, configuration, administration, integrations, plugins, development, release-notes. |
| NetBox | `netbox-community/netbox/docs/installation/` | Sequential deployment guide: PostgreSQL, Redis, NetBox, Gunicorn/uWSGI, HTTP server, optional LDAP, upgrading. |
| NetBox | `netbox-community/netbox/docs/configuration/` | Configuration reference split by required parameters, system, security, remote auth, plugins, miscellaneous, development. |
| NetBox | `netbox-community/netbox/docs/administration/` | Admin/operator docs: authentication providers, permissions, error reporting, replication, shell. |
| NetBox | `netbox-community/netbox/docs/development/` | Developer docs: getting started, style guide, models, adding/extending models, web UI, i18n, release checklist. |
| NetBox | `netbox-community/netbox/docs/release-notes/` | Versioned release notes from historical major/minor versions through current releases. |
| NetBox | `netbox-community/netbox/mkdocs.yml` | Navigation explicitly separates Introduction, Features, Installation & Upgrade, Getting Started, Configuration, Customization, Best Practices, Integrations, Plugins, Administration, Data Model, Reference, Development, Release Notes. |
| Gitea | `go-gitea/gitea/README.md` | Repository landing page: purpose, docs website link, build/run snippets, contribution/security notes, translation/community info, screenshots. |
| Gitea | `go-gitea/gitea/docs/` | Application-repo process/governance docs: backend/frontend guidelines, release management, community governance. |
| Gitea | `go-gitea/gitea/docker/README.md`, `go-gitea/gitea/contrib/` | Deployment-adjacent artifacts and docs in repo: Docker README, service scripts, upgrade script, sample/contrib assets. |
| Gitea | `go-gitea/gitea/CHANGELOG.md`, `go-gitea/gitea/CHANGELOG-archived.md` | Current changelog plus archived changelog file at repo root. |
| Gitea docs | `go-gitea/docs/content/doc/` | Separate docs website repository with user-facing sections such as installation, upgrade, usage, features, advanced, help. |
| Home Assistant Core | `home-assistant/core/README.rst` | Minimal repository landing page pointing users to the website for demo, installation, tutorials, docs, help; points developers to architecture and component docs. |
| Home Assistant Core | `home-assistant/core/CONTRIBUTING.md`, `home-assistant/core/CLA.md`, `home-assistant/core/CODEOWNERS`, `.github/` | Contributor/legal/review process files live in repo root or GitHub metadata, not the product README. |
| Home Assistant developers | `home-assistant/developers.home-assistant/docs/` | Separate developer documentation site with architecture, API, development, documenting, review process, frontend, supervisor, operating system, integration creation docs. |

### Common README vs `docs/` Split

Observed pattern across the examples:

1. **README is a front door, not the manual.**
   - Grafana README contains product positioning, feature bullets, “Get started,” “Documentation,” “Contributing,” “Get involved,” and license, while detailed setup/admin docs live under `docs/sources/`.
   - NetBox README presents the product role, why it exists, screenshots, and links to official docs and community resources, while the full manual lives in `docs/`.
   - Gitea README includes purpose, documentation link, short build/run snippets, contribution/security notes, and screenshots; comprehensive docs are linked to an official docs site.
   - Home Assistant Core README is especially lean: product tagline plus links to demo, installation, tutorials, docs, help, architecture, and component development docs.

2. **Full documentation is organized as a navigable site source.**
   - NetBox keeps the docs website source in-repo under `docs/` and uses `mkdocs.yml` navigation to group content by audience and task.
   - Grafana keeps docs source in-repo under `docs/sources/`, with product docs and developer-resource docs under the same docs source tree.
   - Gitea and Home Assistant use separate documentation repositories/websites for major user/developer docs, keeping the application repository README as a pointer to those sites.

3. **Root-level governance/community files remain standard.**
   - Mature repos commonly keep `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `LICENSE`, `MAINTAINERS`, `DCO`/`CLA`, and GitHub templates at repo root or `.github/`, even when product docs live elsewhere.

### Deployment Docs Patterns

Observed deployment docs are usually separated from general admin usage:

- **NetBox** uses an explicit `docs/installation/` sequence: `1-postgresql.md`, `2-redis.md`, `3-netbox.md`, `4a-gunicorn.md`, `4b-uwsgi.md`, `5-http-server.md`, `6-ldap.md`, plus `upgrading.md`. Deployment prerequisites and service components are separated into numbered steps.
- **Grafana** uses `docs/sources/setup-grafana/` for install and operational setup, including `installation/`, `configure-docker.md`, `configure-grafana/`, `configure-security/`, `set-up-https.md`, HA, monitoring, sign-in, and start/restart docs.
- **Gitea** points from the app README to the docs site for installation/source build docs; the app repo also keeps deployment artifacts in `docker/README.md` and `contrib/` service/upgrade scripts.
- **Home Assistant** points application-repo readers to the public website for installation and getting-started docs rather than carrying end-user installation details in `home-assistant/core`.

### Admin Guide Patterns

Admin/operator content is commonly distinct from installation:

- **Grafana** has a broad `docs/sources/administration/` tree with backup, CLI, migration, provisioning, plugin management, roles/permissions, service accounts, and user/team/org management.
- **NetBox** has `docs/administration/` for authentication, permissions, error reporting, replication, and shell, while system/security/plugin configuration is in `docs/configuration/`.
- **Gitea** README states the official docs include administration; the GitHub mirror of the docs repository groups user-facing docs under `content/doc/` sections such as installation, upgrade, usage, advanced, features, and help rather than a top-level `administration/` directory in the inspected listing.
- **Home Assistant** keeps end-user/admin guidance on the main public website; the core repo stays focused on source and contributor concerns.

### Developer / Contributor Docs Patterns

Two related but distinct documentation audiences appear repeatedly:

1. **Contributor process docs**: how to participate in the project.
   - Root files: `CONTRIBUTING.md`, code of conduct, security policy, CLA/DCO, maintainers, GitHub issue/PR templates.
   - Grafana goes further with `contribute/` for issue triage, PR creation, dependency upgrades, deprecation policy, engineering docs, architecture, and style guides.
   - Gitea app repo keeps contributor guidelines plus `docs/guideline-backend.md`, `docs/guideline-frontend.md`, `docs/release-management.md`, and `docs/community-governance.md`.

2. **Developer/reference docs**: how to extend or develop the software.
   - NetBox uses `docs/development/` for models, web UI, style guide, i18n, search, release checklist, and plugin development under `docs/plugins/development/`.
   - Grafana uses `docs/sources/developer-resources/` for API reference, developer tools, and public contribution docs, plus `contribute/developer-guide.md` for local contributor setup.
   - Home Assistant uses a separate `developers.home-assistant` docs site with architecture, APIs, development setup, integration/component creation, review process, documenting, frontend, supervisor, and operating-system docs.

### Archived / Process Docs Patterns

Observed handling for long-lived or process-heavy docs:

- **Changelogs and release notes are versioned or archived explicitly.**
  - Gitea keeps `CHANGELOG.md` and `CHANGELOG-archived.md` at repo root.
  - Grafana has `.changelog-archive/` and a `docs/sources/upgrade-guide/` tree with per-version upgrade folders.
  - NetBox keeps `docs/release-notes/` with version files across many major/minor versions.

- **Process docs are separated from user manuals when they do not serve operators.**
  - Grafana’s `contribute/` contains PR, triage, deprecation, engineering, architecture, and style-guide docs outside the product docs navigation.
  - Gitea app repo’s `docs/` contains governance/guideline/release-management docs, while public user docs live in the separate docs repository.
  - NetBox includes development/release checklist docs inside the docs site navigation, but root-level `CONTRIBUTING.md` remains the main contributor entry point.
  - Home Assistant separates user docs, developer docs, and source repository process files across different repos/sites.

### Comparison Summary

| Convention Area | Common Pattern | Examples |
|---|---|---|
| README | Product pitch, badges/logo, short feature overview, screenshots/demo links, docs link, contribution/community links, license. | Grafana, NetBox, Gitea, Home Assistant Core |
| Full user docs | Dedicated docs tree or separate docs website repo; README links out. | NetBox `docs/`; Grafana `docs/sources/`; Gitea docs repo; Home Assistant website |
| Deployment docs | Separate install/setup section, often distinct from admin guide; upgrades called out separately. | NetBox `docs/installation/`; Grafana `setup-grafana/`; Gitea `content/doc/installation` + `upgrade`; Home Assistant website |
| Admin docs | Auth, permissions, backup/replication, provisioning/configuration, users/orgs/services. | Grafana `administration/`; NetBox `administration/` + `configuration/` |
| Developer docs | Either `docs/development/`, `developer-resources/`, `contribute/`, or a separate developer docs repo. | NetBox, Grafana, Home Assistant, Gitea |
| Contributor process | Root `CONTRIBUTING.md` plus optional deeper process directory. | Grafana `contribute/`; Gitea root + `docs/`; NetBox root + `docs/development/`; Home Assistant root + developer site |
| Archives | Versioned release notes, upgrade guides, archived changelogs; not embedded in README. | NetBox `release-notes/`; Grafana `.changelog-archive/` + `upgrade-guide/`; Gitea `CHANGELOG-archived.md` |

### External References

- [Grafana repository README](https://github.com/grafana/grafana/blob/main/README.md) — README as landing page with docs and contribution links.
- [Grafana docs source tree](https://github.com/grafana/grafana/tree/main/docs/sources) — in-repo product docs grouped by setup, administration, tutorials, troubleshooting, upgrades, and developer resources.
- [Grafana contribution docs](https://github.com/grafana/grafana/tree/main/contribute) — process/developer docs outside the product docs tree.
- [NetBox repository README](https://github.com/netbox-community/netbox/blob/main/README.md) — product positioning, screenshots, getting-started links, community/contribution links.
- [NetBox docs tree](https://github.com/netbox-community/netbox/tree/main/docs) — full docs source organized into installation, configuration, administration, integrations, development, release notes, and model references.
- [NetBox mkdocs navigation](https://github.com/netbox-community/netbox/blob/main/mkdocs.yml) — explicit docs information architecture.
- [Gitea repository README](https://github.com/go-gitea/gitea/blob/main/README.md) — README points to official documentation website and includes concise build/use/contribution info.
- [Gitea app-repo docs](https://github.com/go-gitea/gitea/tree/main/docs) — governance, backend/frontend guidelines, release management.
- [Gitea documentation repository](https://github.com/go-gitea/docs/tree/main/content/doc) — separate docs site source for installation, upgrade, usage, advanced, features, and help.
- [Home Assistant Core README](https://github.com/home-assistant/core/blob/dev/README.rst) — concise repo landing page pointing to public user docs and developer docs.
- [Home Assistant developer docs repository](https://github.com/home-assistant/developers.home-assistant/tree/master/docs) — separate developer documentation site.

### Related Specs

- Not applicable. This was external documentation-convention research; no local Trellis spec files or application code were modified.

## Caveats / Not Found

- This research used GitHub repository listings and README/docs source inspection rather than a full crawl of each rendered documentation website.
- Gitea’s README points to `https://docs.gitea.com/` and `https://gitea.com/gitea/docs`; the inspected GitHub mirror was `go-gitea/docs`.
- In the inspected Gitea docs repository listing, admin/operator content was not exposed as a top-level `administration/` directory; relevant public docs appear grouped under `installation`, `upgrade`, `usage`, `advanced`, `features`, and `help`.
- Home Assistant user/operator documentation primarily lives outside `home-assistant/core`; the inspected separate developer docs repository covers developer-facing documentation rather than end-user installation/admin manuals.
