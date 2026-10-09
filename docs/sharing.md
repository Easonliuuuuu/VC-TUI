# Sharing inventory

Use an export profile to limit a shared workbook, and add `--pseudonymize` to
replace identifying values. Profiles leave the local assessment unchanged.
Without `--profile`, export writes the ordinary workbook.

| Profile | Contents |
| --- | --- |
| `sizing-summary` | `vInfo`, `vDisk`, `vSource`, `vCluster`, `vHost`, `vDatastore` and `vsfleetCoverage`, each limited to the columns listed in the allowlist below |
| `full-inventory` | Every worksheet and column of the ordinary export, so pseudonymization can be applied to the whole inventory; with `--include-metadata`, also `vsfleetMetadata` (see [metadata sharing](metadata.md#workbook-sheet-and-sharing-profiles)) |

`sizing-summary` allowlist (a column not listed is omitted, not blanked):

- `vInfo`: VM, Powerstate, Template, CPUs, Memory, In Use MiB, Datacenter, Cluster, Host, OS according to the configuration file, VM ID, vsfleet Context
- `vDisk`: VM, Powerstate, Template, Disk, Disk Key, Capacity MiB, Thin, Disk Mode, Sharing mode, Path, VM ID, vsfleet Context
- `vSource`: everything except VI SDK Server and VI SDK UUID
- `vCluster`, `vHost`, `vDatastore`: capacity, state and version columns plus names and vsfleet Context; no Object ID or VI SDK columns
- `vsfleetCoverage`: all columns

Both profiles retain run/context provenance and coverage gaps, and append:

- `vsfleetShare`: profile, run ID/status, partial status, contexts, gaps,
  omitted worksheets, and warnings.
- `vsfleetShareFields`: every ordinary-export column, its sensitivity class,
  and whether it was kept, pseudonymized, scrubbed, or omitted.

Sensitivity classes are `name`, `ip`, `path`, `id`, `free-text`, `topology`,
and `none`. Missing sensitivity rules or nonexistent allowlisted columns are
errors.

Preview shows contexts, worksheets, row counts, columns, and sensitivity
classes without writing a file. Export uses the same plan:

```sh
vsfleet assessment export --profile sizing-summary --pseudonymize --preview
```

## Pseudonymize values

Profiles preserve values unless `--pseudonymize` is given:

```sh
openssl rand -base64 32 > ~/.config/vsfleet/share.key && chmod 600 ~/.config/vsfleet/share.key
vsfleet assessment export --profile sizing-summary --pseudonymize \
  --pseudonymize-key-file ~/.config/vsfleet/share.key --file sizing.xlsx
```

- Names, IPs, MACs, IDs, path segments, and endpoints become HMAC-SHA256 tokens
  such as `vm-3f9a1c2b7d40`. Equal values share tokens across worksheets:
  contexts distinguish equal VM names, and shared backing paths still join.
  IP tokens hide subnet relationships.
- Paths are tokenized per segment. The datastore in `[ds] dir/x.vmdk` uses
  the `Datastore`/`Name` token; short alphabetic extensions such as `.vmdk`
  remain.
- Tokens are run-bound by default. `--link-exports` lets the same key produce
  matching tokens across runs of the estate; use it only for intended correlation.
- The same input, profile, key and options produce a byte-identical workbook.
  A different key produces different tokens.
- Read the key from `--pseudonymize-key-file` (`-` for stdin). It must be at
  least 16 bytes and, on Unix, unreadable by group/others. Missing or short keys
  fail without writing output. Neither the key nor its identifier enters the
  workbook.

## Sharing limits

Pseudonymized workbooks remain identifying:

- Free text (annotations, snapshot descriptions, contacts, labels) becomes
  tokens. System text (`Error`, health `Message`, `Evidence`,
  `Recommendation`) stays readable. Scrubbing replaces known names/addresses
  of four or more characters on a best-effort basis; other identifying wording
  can remain. Preview and `vsfleetShare` list retained free-text columns.
- VLANs, subnet masks, uplink names, segment IDs, counts, capacities, versions,
  builds, and timestamps remain and may identify a familiar environment.
- Tokens are one-way. A key holder can test guesses, such as a known VM name
  or IP, by recomputing them. To identify a token locally, keep the assessment
  and recompute inventory tokens with the same key and options.
- Protect the key as a credential: a mode 0600 file on encrypted disk or a secret
  manager. Keep it outside the export directory and never send it with the
  workbook. Losing it prevents matching-token recreation but leaves the local
  assessment intact; rotation changes future tokens.

The plan, preview, `vsfleetShare`, and coverage gaps retain partial or failed
status. Missing rows do not prove missing objects.

Profiles write XLSX only and reject `--format csv`. They retain worksheet and
column names, but acceptance by RVTools or other importers is not guaranteed.
