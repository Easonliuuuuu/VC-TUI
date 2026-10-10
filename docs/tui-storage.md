# Datastore browser

Use a datastore's [detail actions](tui-actions.md#detail-pane-actions) to
inspect files or search by name. Browsing requires
[`Datastore.Browse`](configuration.md#vsphere-permissions).

## Datastore file browser

“Browse files” opens a read-only browser. It cannot delete, rename, move,
upload, create directories, or download. Opening it reads only the root;
entering a folder reads that folder. Ordinary inventory makes no filesystem
queries.

| Key | Action |
|---|---|
| `Enter` | Open folder or inspect file |
| `Esc` | Go up; close at root; cancel a running request |
| `/` | Filter this directory by name |
| `f` | Search recursively |
| `y` | Copy selected entry's `[datastore] path` |

`f`, or “Find in datastore,” accepts a name or glob such as `*.iso`. It shows
progress and marks capped results as incomplete. `Esc` cancels; `Enter` on a
result opens its containing directory.

A VMDK inspector checks attached VMs/templates on demand and lists matching
backing paths and disk labels. `Enter` on a reference jumps to the exact VM
or template in its context. Missing contexts/objects show the reason. The
lookup cache lasts only for the open datastore workspace.

When available, the inspector also shows the current vCenter's newest finished
assessment, with run number and timestamp. Its point-in-time evidence can be
referenced, verified-unreferenced, suspected, or referenced-other-context.
Missing, stale, denied, or truncated evidence remains
`unknown-incomplete-coverage`. Neither that evidence nor a partial live scan
with no match proves a current file is orphaned.

An unreachable datastore disables browsing with a reason. Unreadable
directories show vCenter's error, rather than an empty listing. Browsing and
`vsfleet assessment run --browse-datastores` use the same read-only privilege
but run independently.

## Real-vSphere validation gate

Relationship-aware browsing still needs [manual acceptance against real
read-only vSphere](testing.md#datastore-browser-acceptance). Treat unknown or
partial coverage as incomplete evidence, never as an orphan verdict.
