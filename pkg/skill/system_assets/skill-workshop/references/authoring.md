# Authoring a Skill

The one place the writing rules for a SKILL.md live. Every skill that creates or
edits skills points here instead of restating them, so the rules cannot drift
apart between the paths a user reaches them through.

## Anatomy of a Skill

```
skill-name/
├── SKILL.md (required)
│   ├── YAML frontmatter (name, description required)
│   └── Markdown instructions
└── Bundled Resources (optional)
    ├── scripts/    - Executable code for deterministic/repetitive tasks
    ├── references/ - Docs loaded into context as needed
    └── assets/     - Files used in output (templates, icons, fonts)
```

## Progressive Disclosure

Skills use a three-level loading system:

1. **Metadata** (name + description) - Always in context (~100 words)
2. **SKILL.md body** - In context whenever the skill triggers (<500 lines ideal)
3. **Bundled resources** - As needed (unlimited; scripts can execute without
   loading)

**Key patterns:**

- Keep SKILL.md under 500 lines. Approaching that limit means the skill needs
  another layer of hierarchy plus clear pointers telling whoever follows it
  where to go next.
- Reference files clearly from SKILL.md, with guidance on when to read them.
- For large reference files (>300 lines), include a table of contents.

**Domain organization**: when a skill supports several domains or frameworks,
organize by variant:

```
cloud-deploy/
├── SKILL.md (workflow + selection)
└── references/
    ├── aws.md
    ├── gcp.md
    └── azure.md
```

Only the relevant reference file is read.

## Frontmatter

- **name** (required): the skill's identifier. Lowercase letters, digits and
  hyphens; at most 64 characters; identical to the directory name. It also
  becomes the skill's slash command, so it must not collide with a built-in
  command.
- **description** (required): the primary triggering mechanism — what the skill
  does *and* the contexts that should bring it up. A skill without a description
  is invisible at every entry point, so it is not written at all. All "when to
  use this" information belongs here, not in the body.
- **compatibility** (optional): required tools or dependencies.

## Writing the description

The description is what decides whether the skill is used. Write it for the
moment of the decision:

- Lead with what the skill does, then say when to use it.
- Name the concrete contexts, phrasings and file types that should bring it up.
- Prefer slightly "pushy" over under-triggering: a skill that is not consulted
  when it applies may as well not exist. Instead of "How to build a quick
  dashboard", write "How to build a quick dashboard. Use this whenever the user
  mentions dashboards, data visualization, internal metrics, or wants to display
  company data — even when they do not say 'dashboard'."
- Keep it honest: a description that promises more than the body delivers gets
  the skill loaded and then ignored.

## Writing patterns

Prefer the imperative form.

**Defining output formats** — state the template exactly:

```markdown
## Report structure
ALWAYS use this exact template:
# [Title]
## Executive summary
## Key findings
## Recommendations
```

**Examples pattern**:

```markdown
## Commit message format
**Example 1:**
Input: Added user authentication with JWT tokens
Output: feat(auth): implement JWT-based authentication
```

## Writing style

Explain *why* something matters instead of shouting MUST. Use theory of mind:
write for someone who has the context you have now and none of the context you
had while learning it. Keep the skill general rather than narrowly fitted to the
one example that produced it. Write a draft, then read it again with fresh eyes
and improve it.

## Principle of Lack of Surprise

A skill's contents must not surprise the user in their intent. No malware,
exploit code, or anything that could compromise system security — and no
requests to create misleading skills, or skills for unauthorized access, data
exfiltration, or other malicious activity. A "roleplay as an XYZ" skill is fine.

## What does not belong in a skill

- One-off facts from the session that produced it: a ticket number, a
  temporary directory, a date.
- Secrets, tokens, credentials. Skills are committed to repositories; write
  placeholders and say where the real value comes from.
- Personal absolute paths from one machine.
- Content that is already the repository's documented standard, copied in.
