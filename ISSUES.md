# Backlog issue

Backlog unico, ricavato da `SECURITY_REVIEW.md` (rimosso; testo nella storia
git), `NONO_COMPARISON.md` e dal confronto con drop. Ogni voce: tipo, priorità,
dimensione, dipendenze.

| Priorità | Significato |
|---|---|
| **P0** | Exploitabile o perde dati: subito |
| **P1** | Alto impatto: a breve |
| **P2** | Reale, raggio limitato |
| **P3** | Miglioria, su richiesta |

**Ordine consigliato:** ISS-06 → ISS-14/15 → ISS-37 → resto.

**Da firmare su macchina reale:** gli e2e di ISS-01/02/03/04 con
`.sdlc/e2e-security`.

---

## Aperte

### P1

**ISS-06 · Credential injection via proxy** · `security` `feature` · L · dipende da ISS-05 (fatta)
Il proxy, in modalità reverse, inietta il token (keyring/env/file host) negli
header verso l'upstream, opzionalmente ristretto a glob metodo+path; fuori
policy → 403. Il token non entra mai nella sandbox.
*Acceptance:* chiamata API autenticata riuscita senza token leggibile in env o
filesystem della sandbox; endpoint fuori policy → 403.

### P2

**ISS-14 · Audit log per run (NDJSON + redaction)** · `security` · M
Il record per run esiste già (`<run-id>.json`, ISS-08): estenderlo con
profilo risolto, mount, `allow`, verify e richieste del proxy; segreti in
argv/env redatti. Hash chain come passo successivo.
*Acceptance:* `inner log show <run-id> --json` dà il record completo; un token
passato con `-e` compare redatto.

**ISS-15 · Snapshot del workspace e rollback** · `feature` · L
Snapshot pre/post dei soli mount scrivibili (reflink su btrfs/XFS, altrimenti
dedup per hash; esclusioni gitignore, cap di spazio). `inner rollback <run-id>
--diff|--restore`. v0 per repo git: stato dirty pre-run + `git diff` post-run.
*Acceptance:* `--diff` mostra file modificati/creati/cancellati, `--restore`
torna allo stato pre-run.

**ISS-16 · Flag di policy one-off su `inner run`** · `ux` · S
`--allow <key>`, `--block <cmd>`, `--allow-domain <dom>`: stessa semantica dei
campi profilo, priorità alla CLI.
*Acceptance:* `inner run --block rm` blocca `rm`; `--dry-run` riflette i flag.

### P3

**ISS-22 · Filtro seccomp opzionale** · `security` · M
Default conservativo (`ptrace`, `process_vm_readv`, `keyctl`/`add_key`/
`request_key`, `bpf`, `mount`, `open_by_handle_at`, `ioctl(TIOCSTI/TIOCLINUX)`)
con opt-out da profilo. Ostacolo: `systemd-run` (resource limits) chiude gli fd
extra, quindi `--seccomp <fd>` richiede un wrapper. Landlock è già in tree, ma
usato solo per i socket astratti.

**ISS-37 · Profili remoti: residui del gate** · `security` · S
`--dry-run` senza consenso esegue comunque la preparazione lato host; i nomi
env "segreti" restano una denylist euristica. Il D-Bus astratto con
`network = full` è chiuso da Landlock su Linux 6.12+, aperto su kernel più
vecchi o con `allow = ["abstract-sockets"]`.

**ISS-38 · Il profilo fuso eredita il `name` della base** · `bug` · S
Se il figlio non dichiara `name`, quello ricavato dal file non conta come
"definito" e `mergeProfiles` tiene il nome della base: un run del figlio è
riportato con il nome della base (`RunConfig.Name`). `profile explain` lo
aggira già. Emerso da ISS-17.

**ISS-23 · `inner profile search/add` su indice firmato** · `feature` · M
Indice firmato (cosign/minisign) di `contrib/profiles`; `add` verifica la firma
e copia in `~/.config/inner/profiles/`.

**ISS-24 · Tool policy incatenate via shim rientranti** · `feature` · L
`[noop.sandbox] git = "git-only"`: lo shim rilancia il comando in una sandbox
figlia più stretta (nested userns). v1: solo restrizione per comando.

**ISS-25 · Sessioni detached (`inner ps/attach/stop`)** · `feature` · L
v0: integrazione tmux/abduco documentata o `ps`/`stop` su pidfile per run ID;
supervisor PTY solo se c'è domanda.

**ISS-26 · Cache della runtime detection** · `performance` · S
Cache in `~/.cache/inner/` di path/versione bwrap e display server,
invalidata per mtime.

**ISS-27 · Cache HTTP (ETag) per i profili remoti** · `performance` · S
`If-None-Match` invece del download a ogni run.

**ISS-28 · `inner verify` in parallelo** · `performance` · S
Check in parallelo mantenendo l'ordine dell'output (il dial timeout da solo
costa fino a 2 s).

**ISS-29 · Packaging + `inner init` interattivo** · `ux` · M
AUR, deb/rpm dalla pipeline di release; `init` guidato: agente → profilo →
`doctor`.

**ISS-30 · Login OAuth sandboxed** · `feature` · L · dipende da ISS-06
Token di login degli agenti nel keyring invece che in `~/.claude` copiato in
blocco. Da rivalutare dopo ISS-06.

---

## Chiuse

| ID | Titolo | Cosa è stato fatto · residui |
|---|---|---|
| ISS-01 | Profili remoti: consenso + `--sha256` | Hardening (via `inherit_all`, segreti env, chiavi host-privilege, mount che rilocano path nascosti, log/workdir/workspaces), consenso bloccante non coperto da `--yes`, pinning. Un test impone una policy remota per ogni nuovo campo. |
| ISS-02 | Copie che seguono i symlink | `O_NOFOLLOW`, solo file regolari, symlink ricreati e non dereferenziati, niente setuid. |
| ISS-03 | Denylist estesa | Nuovi path + socket runtime (`session-bus`, `systemd-user`, `ssh-agent`, `gpg-agent`); bus filtrato con `xdg-dbus-proxy` per claude; `<settings/>` vuoto per Maven. Canary test. |
| ISS-04 | Home isolata | `home = "isolated"` + `home_allow`; default per i profili agente; check `home-isolated` in verify. |
| ISS-05 | Proxy di rete con allowlist | `network_mode = "allowlist"`, liste a livelli con provenienza, gruppi `@npm`/`@github`…, always-deny di loopback/LAN/metadata, scoping delle porte. Dettagli in `network.md`. |
| ISS-07 | Sign-off TUI/PID namespace | Checklist manuale ok il 2026-08-25. |
| ISS-08 | Run ID + `--json` | Run ID stampato su stderr a inizio/fine run; record `<run-id>.json` per ogni run (anche interattiva) nella dir dei log; `inner log <run-id>`, `log list/show` sui record. `--json` su `run --dry-run`, `verify`, `doctor`, `profile list`, `log list/show`. |
| ISS-09 | `-m` rifiuta `safe-rw` | `-m` accetta `ro`/`rw`/`safe-rw`; `tmpfs` rifiutato con rimando a `[mounts]`. |
| ISS-10 | Rollback parziale dei workspace | Directory registrate prima di `MkdirAll`. |
| ISS-11 | Scadenza token nondeterministica | Sceglie la scadenza più vicina. |
| ISS-12 | `checkUsrReadonly` fail-open | Legge il flag `ro` da `/proc/self/mountinfo`. |
| ISS-13 | Comandi bloccati invocabili per path | Shim montato anche sopra il binario reale (PATH + bin standard, symlink risolti). Esclusi con warning: binari condivisi con nomi non bloccati (busybox, alternatives) e `/bin/sh`. Residuo: una copia del binario altrove gira ancora. |
| ISS-17 | `inner profile explain` | Catena `extends` e layer di ogni valore, con le stesse regole del merge. |
| ISS-18 | Triage dei fallimenti bwrap | Con exit 1/126/127 un probe ricostruisce la sandbox e mappa l'errore (userns col sysctl colpevole, mount, pty, entrypoint) a `error:` + `hint:`. `doctor` nomina `max_user_namespaces` e AppArmor. Residui: non usato da `verify`; fallimenti di `systemd-run` non classificati. |
| ISS-19 | `\r` riscritto come `\n` | CR grezzo tra virgolette, come git. |
| ISS-20 | Alias splittati sugli spazi | Tokenizer con quote ed escape. |
| ISS-21 | Pattern di `checkEnvSecrets` | Pattern ampliati, dichiarato euristico. |
| ISS-34 | Denylist: path rilocati e tool mancanti | Path spostati da env (`XDG_*`, `KUBECONFIG`, `GNUPGHOME`, `DOCKER_CONFIG`, `CARGO_HOME`, `AWS_*_FILE`, `SSH_AUTH_SOCK`, …) nascosti sotto la stessa chiave; nuove voci (browser flatpak/snap, Thunderbird, git XDG, containers auth, vault, rclone, s3cfg, age/sops, pulumi, history REPL) e token degli agenti (`agent-credentials`, esentato dalla capability che lo copia). Residuo: `verify` controlla solo i path fissi (env pulito in sandbox). |
| ISS-31 | `.git` scrivibile = escape | `git_dir = "protected"` (default): config, hooks, commondir… in sola lettura, commit ok. Residuo: un repo annidato registrato come gitlink esegue il suo `core.fsmonitor` sull'host; possibile avviso a fine run. |
| ISS-32 | Check TIOCSTI in `doctor` | Avviso se `legacy_tiocsti` non è 0. |
| ISS-33 | Maschere su `/proc` e `/sys` | Come runc, solo se i path esistono. |
| ISS-35 | Check `docker-socket` basato su `Stat` | Fallisce solo se il socket accetta `connect(2)`. |
| ISS-36 | Symlink rotti nella denylist | Hide saltato con warning; fatale solo se il target cade in un mount `rw`. |

---

## Dipendenze

```
ISS-05 (fatta) ──► ISS-06 ──► ISS-30
               └─► ISS-16 (--allow-domain), ISS-14 (eventi rete)
ISS-01 (fatta) ──► ISS-23, ISS-27
```
