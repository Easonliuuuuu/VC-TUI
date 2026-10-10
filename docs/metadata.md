# Tags and custom attributes

## Export metadata

`assessment metadata` writes stored tags and custom attributes as one row per
value. Columns are fixed across estates and vCenters:

```sh
vsfleet assessment metadata --format csv --file metadata.csv
vsfleet assessment metadata nightly --kind vm --source tag -o json
```

| Column | Meaning |
| --- | --- |
| `run_id`, `captured_at` | The assessment, and when the row's context was captured (RFC3339 UTC) |
| `context`, `vcenter_id`, `kind`, `object_id` | Object identity. `vcenter_id` + `kind` + `object_id` follows an object across captures; names are not unique |
| `object_name`, `path` | As vCenter reported them |
| `source` | `tag` or `custom_attribute` |
| `field_id`, `field` | Tag category ID and name, or the numeric custom attribute key and name. The key survives a rename |
| `value_id`, `value` | Tag ID and name, or the attribute value (`value_id` is empty) |
| `status`, `error` | The source's state for this object, and why it could not be read |

Every object has a row for each source. An empty, fully read source has status
`available` and an empty `field` (verified none). An unreadable source gets
a row with its status:

| Status | Meaning |
| --- | --- |
| `available` | Read completely |
| `unavailable` | The read failed; retrying may succeed |
| `denied` | The account lacks the privilege, or the tagging service rejected the session (HTTP 401/403, vSphere `NoPermission`) |
| `unsupported` | The endpoint has no tagging service or custom fields manager |
| `not_recorded` | The capture predates metadata collection |

Coverage appears per context, kind, and source on stderr and in JSON
`coverage`. Counts include every object's status: one failed lookup makes a
collection `partial`. `collection_failed` means neither the objects nor
metadata are known. `--fail-on-incomplete` exits 3 when anything in scope is
not `available`.

### Saved report definitions

Save a TOML definition at `<config dir>/vsfleet/reports/<name>.toml` and run
it by name, or pass a path. `VSFLEET_CONFIG` and `--config` relocate the reports
directory with `config.toml`:

```toml
name = "pci-production"
description = "VMs in PCI scope"
contexts = ["prod-east", "prod-west"]   # names or vCenter IDs; omit for all
kinds = ["vm"]                          # omit for every stored kind
where = ["tag.Compliance=PCI", "tag.Environment=Production"]
columns = ["context", "vcenter_id", "name", "id", "tag.Compliance", "custom.owner"]
```

```sh
vsfleet assessment metadata-report pci-production
vsfleet assessment metadata-report ./pci.toml nightly --format csv --file pci.csv
vsfleet assessment metadata-report pci-production --base baseline
```

- `where` uses the [`--where` language](commands.md#inventory-and-search);
  predicates are ANDed. Unknown keys, kinds, fields, and columns are errors.
- `columns` accepts `context`, `vcenter_id`, `kind`, `id`, `name`, `path`,
  any scalar inventory field of the selected kinds (for example `cpu` or
  `power_state`), `tags` (every `Category/Tag`), `tag.<Category>`,
  `custom_attributes`, `custom.<name>` or `custom.#<key>`, and `tags_status` /
  `custom_attributes_status`. Default: `context`, `kind`, `name`, `id`.
  Multiple values are sorted and joined with `; `. Unreadable sources show
  their status, such as `(denied)`.
- Output always names the report, the assessment ID and label, and the
  capture time. Identical definitions and assessments produce byte-identical output.

Membership has three states:

| State | Meaning |
| --- | --- |
| Member | Every predicate holds |
| Excluded | Any predicate is definitely false |
| Undetermined | No predicate is false, but a required metadata source was unreadable |

For example, unreadable tags make `tag.Compliance!=PCI` undetermined.
Undetermined objects appear separately (CSV `membership=undetermined`) and
make the report incomplete; `--fail-on-incomplete` exits 3. Live and stored
`--where` queries exclude these objects and warn with the source status.

With `--base`, the report runs against both assessments and lists membership
changes by object identity:

| Change | Meaning |
| --- | --- |
| `added` | Not a member in the base, a member in the target |
| `removed` | A member in the base; in the target its collection succeeded and the object is not a member (excluded or gone) |
| `unknown` | Membership is undetermined on one side, or the object's collection failed or was not recorded in one capture |

### Workbook sheet and sharing profiles

`assessment export --include-metadata` appends a `vsfleetMetadata` sheet
with the same rows. Default exports and RVTools-compatible sheets retain their
fixed columns. Metadata can contain owner, customer, or project names; treat
CSV, JSON, and workbook output as sensitive.

- `sizing-summary` omits `vsfleetMetadata` even when `--include-metadata` is
  given; the preview lists it under omitted worksheets.
- `full-inventory` includes it when requested. `--pseudonymize` tokenizes
  `Field`, `Value`, object names, tag/category/object IDs, context, vCenter ID,
  and path; it scrubs `Source error` like other system text. Equal values keep
  equal tokens for grouping, and object IDs join to other sheets' `VM ID` and
  `Object ID` tokens.
- `assessment metadata` and `metadata-report` write unprofiled values. Use a
  [sharing profile](sharing.md) when the output leaves your control.
