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

**Da firmare su macchina reale:** gli e2e di `.sdlc/e2e-security` (profili
remoti, copie con symlink, denylist, home isolata: issue chiuse, verifica
manuale ancora dovuta).

---

## Aperte

### P1

**ISS-06 · Credential injection via proxy** · `security` `feature` · L
Il proxy, in modalità reverse, inietta il token (keyring/env/file host) negli
header verso l'upstream, opzionalmente ristretto a glob metodo+path; fuori
policy → 403. Il token non entra mai nella sandbox.
*Acceptance:* chiamata API autenticata riuscita senza token leggibile in env o
filesystem della sandbox; endpoint fuori policy → 403.

### P2

**ISS-14 · Audit log per run (NDJSON + redaction)** · `security` · M
Il record per run esiste già (`<run-id>.json`): estenderlo con
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
aggira già.

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

## Limiti noti delle issue chiuse

Le issue risolte sono state rimosse (il dettaglio è nella storia git). Restano
questi limiti accettati, da riprendere se diventano rilevanti:

- **Gitlink annidato:** con `git_dir = "protected"` l'agente può registrare un
  repo annidato come gitlink; un `git status` sull'host ne esegue il
  `core.fsmonitor`. Possibile avviso a fine run.
- **`[noop] block`:** una copia del binario bloccato fatta altrove (es. in
  `/tmp`) gira ancora; chiuderlo richiede controllo sull'exec (vedi ISS-22).
- **Diagnosi dei fallimenti bwrap:** non usata da `inner verify`; i fallimenti
  di `systemd-run` (resource limits) non sono classificati.
- **Denylist:** `inner verify` controlla solo i path fissi, non quelli spostati
  da variabili d'ambiente (in sandbox l'ambiente è ripulito).

---

## Dipendenze

```
ISS-06 ──► ISS-30
```
