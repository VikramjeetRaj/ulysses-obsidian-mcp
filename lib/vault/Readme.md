# Vault path validation

The vault service keeps files under `<vault>/Knowledge/`. `vault.New` checks that the vault path is an existing directory and creates `Knowledge` if needed. The private `validatePath` method accepts a path only when it points to a descendant of that directory. It returns an error wrapping `ErrInvalidPath` for the `Knowledge` directory itself or for a path outside it. `ListNotes` additionally accepts the `Knowledge` directory itself as a folder.

## Why `resolvePath` exists

`validatePath` must handle a path for a file that has not been created yet. `filepath.EvalSymlinks` follows symbolic links, but it fails if any part of its input does not exist. `resolvePath` finds the nearest existing ancestor, resolves its symbolic links, then adds the missing path components back.

This matters when a path appears to be inside `Knowledge` but a symbolic link points elsewhere. For example, if `Knowledge/shared` links to `/tmp/shared`, the proposed file `Knowledge/shared/new/note.md` actually belongs under `/tmp/shared`.

## Step-by-step example

Suppose the input is `/vault/Knowledge/new/idea.md`. `/vault/Knowledge` exists, while `new` and `idea.md` do not.

1. `filepath.Abs(path)` converts the input to an absolute, cleaned path. This also handles a relative input using the process's current working directory.
2. `current` starts as `/vault/Knowledge/new/idea.md`; `missing` starts empty.
3. `os.Lstat(current)` reports that `idea.md` does not exist. The function saves `idea.md` in `missing` and sets `current` to its parent, `/vault/Knowledge/new`.
4. `new` does not exist either. The function saves `new` and moves `current` to `/vault/Knowledge`.
5. `/vault/Knowledge` exists. `filepath.EvalSymlinks(current)` resolves symbolic links in that existing path.
6. `missing` contains `[idea.md, new]`. The function joins its entries onto the resolved ancestor in reverse order: `new`, then `idea.md`.
7. It returns the resulting absolute path, `/vault/Knowledge/new/idea.md` in this example.

| Loop check | `current` | `missing` after check |
| --- | --- | --- |
| First | `/vault/Knowledge/new/idea.md` | `[idea.md]` |
| Second | `/vault/Knowledge/new` | `[idea.md, new]` |
| Third | `/vault/Knowledge` (exists) | `[idea.md, new]` |

The reverse order matters because the function discovers missing components from the file upward but must rebuild the path from the parent downward.

## Symbolic-link example

Suppose `/vault/Knowledge/shared` is a symbolic link to `/tmp/shared`, and `/tmp/shared` exists. For `/vault/Knowledge/shared/new/note.md`, `resolvePath` walks up past the missing `note.md` and `new` components. It then resolves the existing `shared` link to `/tmp/shared` and returns `/tmp/shared/new/note.md`. `validatePath` sees that this is outside the resolved `Knowledge` directory and rejects it.

## How `validatePath` uses the result

`validatePath` resolves `<vault>/Knowledge`, calls `resolvePath` on the proposed path, and calculates `filepath.Rel(knowledge, resolved)`. A relative result such as `notes/idea.md` identifies a descendant. `.` identifies `Knowledge` itself; `..` or a path beginning with `../` identifies a path outside it. Those cases return an error.

`resolvePath` also returns an error if it cannot make the path absolute, inspect an existing ancestor, or resolve its symbolic links. It does not create the proposed file or its parent directories.
