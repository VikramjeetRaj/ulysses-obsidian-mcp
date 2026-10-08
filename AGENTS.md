# Repository Guidelines

## Project Structure & Module Organization

This repository is currently an empty scaffold. Add source code, tests, and documentation in clearly named top-level directories as the project takes shape. Keep related implementation and tests easy to locate; for example, pair `src/` with `tests/`. Update this guide when the layout is established.

## Build, Test, and Development Commands

No build system, package manifest, or runnable commands exist yet. When adding one, document the exact setup, build, test, and local run commands in `README.md` and keep them working from the repository root. Prefer a small set of repeatable commands over steps that depend on a contributor's machine.

## Coding Style & Naming Conventions

There is no established language, formatter, or linter. Follow the conventions of the language and tools selected for the first implementation, and commit their configuration so all contributors use the same rules. Use descriptive file and symbol names; keep naming consistent within each module. Avoid introducing formatting-only changes in unrelated work.

## Testing Guidelines

No test framework or coverage threshold is configured. Add focused tests alongside each new behavior and document how to run them. Name tests for the behavior they verify, and include failure cases for input validation and external integrations. Run the full available test suite before opening a pull request.

## Commit & Pull Request Guidelines

There is no Git history in this directory, so no commit-message convention can be inferred. Use short, imperative subjects such as `Add vault path validation`. Keep commits focused. Pull requests should explain the change, how it was tested, and any configuration or user-facing impact; link an issue when one exists. Include screenshots only for visual changes.

## Security & Configuration

Do not commit vault contents, credentials, tokens, or machine-specific paths. Keep local configuration out of version control and provide a sanitized example if contributors need configuration to run the project.
