# abapGit bootstrap spike: installing abapGit through ADT alone, on OSD

Date: 2026-10-02. Issue: #277. Target: open-steamgate OSD `vscode-v0.6.1504` (pinned,
sha256 checked by `.github/ci/osd-up.sh`), on a throwaway `XDG_DATA_HOME`/`HOME`/`STG_DB_PATH`,
`OSD_WARM=0`, client 001. No real SAP system (A4H, i3) was touched. vsp built from `origin/main` (2770c5e).

**Short answer:** not on OSD today. vsp can put the standalone program on OSD (5.16 MB in
one PUT, read back byte for byte), but it does not activate. 176-178 errors come back:
62 SAP standard objects that OSD does not have, plus one scoping bug in OSD. The failed
program then breaks every later activation on the system until someone deletes it. The
developer edition is further away, mostly because of what vsp is missing (no mass-write
path, class includes skipped by `copy`, no FUGR/W3MI/TRAN deploy). It is not blocked by
DDIC: the developer edition ships none.

## 1. What a bare install needs

### Standalone (one program)

| Source | Version | Bytes | Lines | Longest line |
|---|---|---|---|---|
| `embedded/deps/abapgit-standalone.zip` (vsp) | 1.134.0 | 4,977,653 | 154,655 | — |
| `abapGit/build@af91834` `zabapgit_standalone.prog.abap` (official, 2026-10-01) | 1.134.0 | 5,155,326 | 158,962 | 178 |

The install is one PROG with `SUBC=1` and a text pool of one entry. It has no CUA and no
dynpros: the standalone XML carries neither. The largest single source is the program
itself, about 5 MB. It also needs, at runtime, the persistence table `ZABAPGIT`, which
abapGit creates on first start through DDIF. So the system needs DDIC write at runtime
too, not only at install.

### Developer edition (`abapGit/abapGit@511cdbe`, 2026-10-01; the local `/home/alice/dev/abapGit` is a fork at 1.132.0 from Aug 2025, so a fresh clone was used)

| Type | Objects | ABAP bytes | Notes |
|---|---|---|---|
| CLAS | 466 | 5,842,212 | 136 have extra includes: 125 testclasses, 31 locals_imp, 5 locals_def, 0 macros |
| INTF | 114 | 191,707 | |
| DEVC | 51 | — | `$ZGIT_DEV` root plus 50 subpackages, FOLDER_LOGIC=PREFIX |
| W3MI | 7 | 200,295 data | CSS, JS, woff icon font |
| PROG | 3 | 18,683 | ZABAPGIT (type 1, **with a CUA/GUI status**), two includes (SUBC=I) |
| FUGR | 1 | 4,527 | ZABAPGIT_PARALLEL, 2 FMs (RFC, parallel serialize) |
| TRAN | 1 | — | ZABAPGIT |
| **Total** | **592 + 51 DEVC** | **6.06 MB ABAP** (6.59 MB all files, 1,400 files, 208k lines) | |

The largest single source is `zcl_abapgit_ajson.clas.testclasses.abap` at 179,394 B. The
largest main source is `zcl_abapgit_object_tabl_ddl.clas.abap` at 89,896 B. The largest
interface is `zif_abapgit_definitions` at 16,237 B.

**There are no DDIC objects at all**: no DOMA, DTEL, TABL, TTYP or MSAG. The bootstrap
order DOMA → DTEL → TABL is therefore moot for abapGit itself. What matters is classes
and interfaces that depend on each other in cycles (one mass activation), class includes,
one FUGR, the W3MI assets and the CUA of ZABAPGIT.

## 2. What OSD and vsp can do today, per type

Each probe was create, then write source, then activate, then read back, using the vsp
MCP server (expert mode, stdio) against OSD.

| Type | Needed by | OSD | vsp | Result |
|---|---|---|---|---|
| DEVC | dev (51) | **partial.** POST works only under an SQL-backed parent (`$ZOSD_TEST`). A root package (empty superPackage), `$TMP` and the seeded `$ABAPGIT*` all answer "DEVC … does not exist" | CreatePackage OK | created `$ZOSD_TEST_SPK`, `$ZOSD_TEST_AG` |
| PROG | both | yes | WriteSource / CreateObject OK | create, write, activate and read back OK (with its include, through ActivateMultiple) |
| INCL | dev (2) | yes, **but activating an orphan include fails** with a build-log dump instead of a message | WriteSource OK | OK once activated together with its main program |
| INTF | dev (114) | yes | OK | create and activate 33.8 s (cold) |
| CLAS main | dev (466) | yes | OK when it has no locals | — |
| CLAS + locals / testclasses | dev (136) | yes (written inactive and mass-activated: 41 s; ABAP Unit 1/1 passed, so the code runs) | **WriteSource refuses**: its pre-save syntax check fails on the main source (`lcl_h` not found) because the locals are not there yet. The main source is never saved. `copy`/`DeployZip` skip classes with includes ("not implemented") | works only by hand: LockObject on the class, UpdateSource on each include and main with the handle, UnlockObject, ActivateMultiple |
| FUGR / FUNC | dev (1 + 2 FM) | **not served**: `/functions/groups` 404 for POST and GET, even for the seeded ZOSD_TEST_FG | CreateObject FUGR/F, FUGR/FF exist | blocked on OSD |
| DOMA | (none) | not served (`/ddic/domains`) | create only through hyperfocused `SAP create DOMA`; no read | blocked on OSD |
| DTEL | (none) | read only: GET 200, POST "not served". Discovery advertises the collection anyway | `SAP create DTEL` | blocked on OSD |
| TABL | runtime `ZABAPGIT` | read only: GET 200, POST "not served". Advertised in discovery | CreateTable (JSON to DDL) | blocked on OSD |
| STRUCT | (none) | not served | `SAP create STRUCT` | blocked |
| TTYP | (none) | not served | **no create in vsp** | blocked on both |
| MSAG | (none) | not served (`/messageclass`) | CreateObject MSAG/N | blocked on OSD |
| W3MI | dev (7) | no ADT endpoint (there is none on real SAP either) | read only (`vsp w3mi`) | needs a non-ADT path |
| TRAN | dev (1) | no ADT create | read only | needs a non-ADT path |
| CUA of ZABAPGIT | dev | none | none | needs a non-ADT path |
| Run a report | check | `RunReport` needs ZADT_VSP WebSocket (404). `/oo/classrun` is in discovery | no classrun support | ABAP Unit is the only way vsp can execute code on OSD today |

### Missing on OSD's side (to raise with OSG/dell, who owns the ADT façade)

1. **A failed activation poisons the system.** OSD writes the source into the live
   `src/` tree even when activation fails. Every later cold rebuild transpiles it again and
   fails, so *every* activation by anyone fails, even a 1-line program (69-77 s, then
   error). Deleting the failed object (`ZABAPGIT_STANDALONE`) was the only fix. Inactive
   sources need to be kept out of the generation.
2. **The activation error is a truncated build log.** When the runtime rebuild fails, the
   message is the first ~2,000 chars of the `osd-gui-convert`/`osd-tran-registry` output.
   It leaks absolute host paths and never gives the cause. The same happens for an orphan
   include.
3. **`GET /activation/inactiveobjects` is always empty**, even right after a lock, write
   and unlock without activation. vsp's `ActivatePackage`, which builds on that list,
   therefore cannot work on OSD.
4. **Local names do not shadow global ones.** In the standalone program,
   `INTERFACE zif_abapgit_gui_event` is local, but OSD resolved it to the *seeded global*
   `ZIF_ABAPGIT_GUI_EVENT` (an older version with `current_page_name`). The result was a
   false "Implement method current_page_name" at line 66025.
5. **The seed already contains a partial abapGit**: 9 `$ABAPGIT*` packages and about 47
   `ZCL_/ZIF_ABAPGIT_*` "library objects" (zlib, git, html, AFF types). They are read
   only, cannot be a package parent, and collide by name with a real install.
6. **SAP standard objects that the standalone program references but OSD lacks: 62.** Among
   them: TRDIR, PROGDIR, E070/E071/E071K/E070USE, DD02L/DD02T/DD02V/DD03P, TDDAT, THEAD,
   TLINETAB, DOKHL, BDCDATA, SEOCLSKEY/SEOCLSNAME/SEOCMPNAME/SEOCLASSTX/SEOCOMPOTX/SEOSUBCOTX,
   TRWBO_*, TR_OBJECTS, TROBJ_NAME, SCI_CHKV/SCI_OBJS/SCIR_OBJS/SCIT_ALVLIST, CL_CI_INSPECTION,
   CL_CI_OBJECTSET, CL_CI_CHECKVARIANT, CL_WB_CHECKLIST, IF_ENH_TOOL, IF_ENH_SPOT_TOOL,
   CL_APL_ECATT_CONFIG_DOWNLOAD (a superclass), SAPRELEASE, DEVLAYER, NAMESPACE, LVC_OUTLEN,
   SALV_T_INT4_COLUMN and RZLLI_APCL. Raw data is in the spike scratch (`sa_msgs.json`).
7. **No DDIC or FUGR/MSAG write**: POST to `/ddic/{domains,dataelements,tables,structures,tabletypes}`,
   `/functions/groups` and `/messageclass` is "not served". Discovery advertises
   `ddic/dataelements` and `ddic/tables` although only GET works.
8. **No root package and no `$TMP` as a parent** (see the DEVC row). vsp's
   `EnsurePackage` with no parent fails on OSD.
9. **Every activation is a full cold rebuild**: 25-41 s for a small object. A 5 MB
   synthetic program took **348 s** (and succeeded). `OSD_WARM` only helps content edits
   of existing classes.
10. A lock on a `…/source/main` URL is "not served". Real SAP locks the object URI too,
    so this is listed for completeness; the vsp side is item V4 below.

### Missing on vsp's side

- **V1. `InstallAbapGit` (MCP) deploys nothing**: the deploy is a `TODO`, and it always prints
  "Waiting for actual ZIP files". The `dev` edition looks up `abapgit-dev` while the
  dependency is registered as `abapgit-full`, and `abapgit-full.zip` is 0 bytes (#277,
  confirmed). The CLI `vsp install abapgit` *does* deploy the standalone through
  `WriteSource`.
- **V2. The CLI install hides the activation errors.** It prints "FAILED: Activation failed
  - check activation messages" and never shows them.
- **V3. There is no inactive mass-write path.** `WriteSource`, `DeployFromFile` and
  `copy` all do per-object syntax check, then write, then activate. With abapGit's cyclic
  class graph, and classes whose main source needs its locals, that cannot work. The low-level
  `LockObject`/`UpdateSource`/`UnlockObject` + `ActivateMultiple` path does work, as the
  probe showed.
- **V4. `UpdateSource` without a handle locks the URL it was given.** For
  `…/source/main` that should be the object URI (OSD: 404).
- **V5. `copy`/`DeployZip` refuse classes with includes** (136 of 466 in the developer
  edition), although `WriteSource include=` now exists. They have no FUGR, W3MI, TRAN,
  DEVC hierarchy or text-pool deploy.
- **V6. Long requests are cut at 60 s.** The per-request HTTP client timeout is 60 s, and
  `--call-timeout` lifts it only for `longCall` tools (DeployFromFile, unit tests, ...),
  not for `WriteSource`, `Activate` or `ActivateMultiple`. On OSD a cold activation of
  anything beyond small already passes 60 s.
- **V7. `deps.DeploymentOrder` puts INTF before DOMA/DTEL/TABL.** That is the wrong
  direction for code that types against DDIC. It is harmless for abapGit, which has no
  DDIC, but wrong for a general engine.
- **V8. No TTYP create; DOMA and DTEL have no read target** in the hyperfocused router.
- **V9. The installer's default package `$ABAPGIT` is the seeded read-only package on OSD.**
  The package detection ("ZADT_VSP: installed" on OSD) is also a false positive.

## 3. Standalone end to end on OSD

| Step | Result | Time |
|---|---|---|
| `vsp install abapgit --edition standalone --package '$ZOSD_TEST_AG'` (embedded 4.98 MB) | create OK, write OK, **activation failed** (CLI shows no reason) | 16.7 s wall |
| Read back | byte-identical (4,977,653 B) | 0.17 s |
| ActivateMultiple, to get the messages | 176 errors: 175 unknown types/classes (62 distinct SAP standard names), 1 local/global shadowing bug | — |
| Official build 1.134.0 (5.16 MB): lock + UpdateSource + unlock | OK, read back byte-identical | write 0.45 s |
| SyntaxCheck of the 5.16 MB source | 178 errors | 27.3 s |
| Activate | fails, 178 errors | — |
| Side effect | every later activation on the system fails until `ZABAPGIT_STANDALONE` is deleted (OSD gap 1) | 69-77 s per failing attempt |

Size limits:
- No source-size limit was hit. A 5 MB PUT works and reads back exact.
- A **5,000,124 B / 113,753-line synthetic program activated successfully in 348 s**, but
  only through a raw activation request with no client timeout. Through vsp
  `WriteSource`, the same call ends at 60 s with "context deadline exceeded" (V6).
- Executability: OSD has no way for vsp to run a report (RunReport needs ZADT_VSP). Code
  execution was shown with ABAP Unit on the probe class (1/1 passed). The standalone
  program never reached an active state, so it could not be executed.

## 4. Bootstrap engine design (developer edition)

Goal: deploy abapGit-serialized files object by object through plain ADT, then
mass-activate, with no abapGit or ZADT_VSP on the target.

**Pipeline**

1. **Read and plan.** Reuse `pkg/adt/git_import.go` `AnalyzeGitZip`: it reads
   `.abapgit.xml` STARTING_FOLDER and FOLDER_LOGIC and maps folders to packages
   (`gitFolderPackages`), with zip-bomb limits. Reuse `embedded/deps` `UnzipInMemory`,
   `ParseAbapGitFilename` and `GroupByObject` for file to object grouping (main + includes + XML).
2. **Packages.** Create the DEVC tree top-down from the folder map. On OSD this needs a
   parent that exists (gap 8).
3. **Shells.** CreateObject for every object, with no source, in type order:
   DOMA → DTEL → TABL/TTYP → INTF → CLAS → FUGR (+FM) → PROG/INCL → MSAG. Fix V7 and keep
   `GitDeleteRank` as its mirror image.
4. **Inactive writes, no syntax check.** For each object: lock the object URI, PUT each part
   (class includes before main: definitions, implementations, macros, testclasses, then
   main), unlock. This is the probed path. It needs a new `adt` function, e.g.
   `WriteInactive(ctx, obj, parts)`, because none of today's high-level writers skip the
   check-and-activate step.
5. **Mass activation.** One `ActivateMultiple` over everything, as Eclipse does for a
   mutually dependent set, with a long timeout (V6). Then iterate on the remaining
   inactive objects, the way `ActivatePackageIterative` does. On OSD this has to work from
   our own list because the inactive list is empty (gap 3).
6. **Metadata.** Descriptions and text pools (`vsp texts`), and message texts. W3MI, TRAN
   and CUA have no ADT write: either leave them out (abapGit runs without the W3MI assets
   and without the transaction, but the ZABAPGIT CUA matters for SAP GUI), or hand them to
   abapGit itself once it runs (the self-pull step below).
7. **Self-pull (optional).** Once the core is active, run abapGit's own deserialize
   (`git_import` / ZCL_VSP_GIT_SERVICE) on its own repo to fill in W3MI, TRAN, CUA and
   FUGR texts. That is the classic "standalone pulls the developer edition" bootstrap,
   done by vsp.

**What to reuse and what to replace**
- `DeployFromFile`/`ParseABAPFile`: reuse the file parsing and type detection. The
  per-object check-and-activate workflow does not fit; it stays for single-file edits.
- `copy`/`DeployZip`/`InstallAbapGit`: replace their deploy loop with the engine. Fix the
  `abapgit-dev`/`abapgit-full` naming and build the zip from upstream (commit 462e1c7
  already builds the standalone from upstream).
- `git_import` ordering: `GitDeleteRank` gives the reverse topological order by type. The
  engine needs the forward order plus a cycle-tolerant mass activation, not a fine-grained
  topological sort.

**Prefixed derivative (coexisting with a real abapGit)**

This is feasible, using abaplint's own renamer, not sed. A real run in this spike:
`npx @abaplint/cli@2.120.64 --rename` with patterns `^z(cl|if|cx)_abapgit_(.*)$ →
z$1_vspgit_$2` and `^zabapgit(.*)$ → zvspgit$1` renamed all 580 CLAS/INTF and the 3
PROGs across 1,400 files in 94 s, with 0 issues. It also rewrites references
(`TYPE REF TO`, `=>`, `INTERFACES`, `RAISING`, XML). The prefix `VSPGIT` is shorter than
`ABAPGIT`, so no name can exceed 30 characters.

What it leaves behind, which a second lexer-aware pass must handle (string tokens only,
from a curated map):
- **Dynamic names in literals (about 290 lines).** The critical ones:
  `'ZCL_ABAPGIT_OBJECT_' && obj_type` (serializer lookup in `zcl_*_objects`,
  `filename_logic`, `objects_compare`, `gui_page_debuginfo`), `'ZCL_ABAPGIT_USER_EXIT'`,
  `'ZCL_ABAPGIT_DEFAULT_AUTH_INFO'`, `'ZIF_ABAPGIT_EXIT'`, `'Z_ABAPGIT_SERIALIZE_PARALLEL'`
  (an RFC FM name), the W3MI keys `'ZABAPGIT_CSS_COMMON'` and so on, and the persistence
  table `'ZABAPGIT'`. **The table must change**, or both abapGits share one repo store.
- **Types abaplint cannot rename**: FUGR (ZABAPGIT_PARALLEL and its FMs), W3MI and TRAN.
  Each needs a file rename plus a literal rewrite.
- **Self-recognition.** abapGit treats `ZCL_ABAPGIT*`/`$ZGIT*` specially in places (for
  example "is this abapGit itself"). This needs review per hit. User exits
  (`ZCL_ABAPGIT_USER_EXIT`) should keep their original names or be documented as renamed.

Recommended shape: a vsp command (or a small Go step wrapping the abaplint CLI) that does
abaplint `--rename` plus a token-level literal pass with vsp's `pkg/abaplint` lexer, then
a check that no `ABAPGIT` string token remains outside an allow-list.

## 5. Gaps to raise with OSG (dell, ADT façade)

In priority order for this track:
1. Keep failed or inactive sources out of the live generation (poisoning, gap 1).
2. Return the real activation/transpile error, not the head of the build log; no host paths (gap 2).
3. Serve `/activation/inactiveobjects` (gap 3).
4. Local declarations must shadow global ones (gap 4).
5. Do not seed `ZCL_ABAPGIT_*`/`$ABAPGIT*` in the bare profile, or make them overwritable (gap 5).
6. Allow root `$` packages and `$TMP` as a parent (gap 8).
7. FUGR/FUNC create, read and write (`/functions/groups`), needed by the developer edition.
8. The SAP standard DDIC and classes abapGit references (gap 6). This is the real blocker
   for *any* abapGit on OSD. A stub layer (type definitions only) would be enough for
   activation; runtime use of DD02L/E071/TRDIR etc. is a separate question.
9. DDIC write (DOMA/DTEL/TABL/TTYP), MSAG, and stopping discovery from advertising
   collections that are read only. abapGit itself needs only the runtime `ZABAPGIT` table.
10. A warm or incremental activation for creates. 30-350 s per cold activation makes
    per-object activation of 592 objects impossible, so mass activation is mandatory.

## 6. Recommended next step

The cheapest step that teaches the most is to build the **vsp-side engine first, on
OSD, with abapGit's dependency-free core as the payload**. Do not wait for OSD to grow
SAP standard DDIC:

1. In vsp: add `WriteInactive` (lock the object URI, write all parts, unlock, no check),
   and give `ActivateMultiple`/`WriteSource`/`Activate` the call-timeout budget (V6). Then
   rewrite `copy`/`DeployZip`/`InstallAbapGit` on top as "write all inactive, then one
   ActivateMultiple". Fix #277's name and empty zip, and surface activation messages in
   the CLI (V2).
2. Test it on OSD with a slice that only needs what OSD has, e.g. the zlib/git/html/json
   classes (the subset OSD itself already seeds). Use the `VSPGIT` prefixed derivative so it
   does not collide with the seed, and pass if ABAP Unit runs green.
3. Send OSG items 1-4 now: they are bugs that hit any multi-object deploy, not only
   abapGit. Ask about item 8 (a stub layer for SAP standard DDIC) as the gate for a full
   standalone or developer-edition activation on OSD.
4. Only then move to the separate, restorable-A4H track, where the standard DDIC exists and the
   same engine should install the full developer edition (prefixed or not).

## Appendix: reproduce

```bash
work=$scratch/osd-abapgit-spike-$(date +%s)
STG_PORT=3141 OSD_WARM=0 .github/ci/osd-up.sh "$work"          # pinned vscode-v0.6.1504
# vsp from an empty dir, env only: SAP_URL=http://localhost:3141 SAP_USER=DEVELOPER SAP_PASSWORD=osd SAP_CLIENT=001
vsp install abapgit --edition standalone --package '$ZOSD_TEST_AG'   # package pre-created under $ZOSD_TEST
```
The probe scripts (an MCP stdio client, per-type probes and size probes) were kept in the
session scratchpad, not in the repo.
