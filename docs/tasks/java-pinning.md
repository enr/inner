# Task: pinning di una versione precisa di Java nella sandbox

Contesto: una macchina host con più JDK installati (es. `/opt/jdk/jdk-21`, `/opt/jdk/jdk-26`).
Obiettivo: dentro la sandbox il comando `java` è in `$PATH` e punta esattamente alla versione desiderata.

La root host è già visibile read-only nella sandbox (`--ro-bind / /`, `internal/isolator/bwrap.go:93`),
quindi i JDK non richiedono mount: il problema è interamente `PATH`/`JAVA_HOME`. I task seguenti
rimuovono le frizioni individuate nel codice.

Ordine consigliato: **T4 → T2 → T3 → T7 → T1 → T5 → T6** (i primi sono piccoli e indipendenti;
T6 dipende in parte da T1).

---

## T1 — `[env] path_prepend`: meccanismo first-class per anteporre directory al PATH della sandbox

**Tipo:** feature · **Size:** M

**Contesto.** Oggi per fissare una toolchain (es. un JDK preciso) bisogna riscrivere l'intero
`PATH` in `[env] set`, sfruttando il dettaglio non documentato che i valori passano per
`ExpandPath` (`internal/config/loader.go:518`) e che `${PATH}` si espande contro l'host.
Con `extends`, un profilo figlio che sovrascrive `PATH` deve conoscere quello del padre.

**Comportamento atteso.** Nuovo campo TOML:

```toml
[env]
path_prepend = ["/opt/jdk/jdk-21/bin"]
```

Il PATH effettivo nella sandbox diventa:
`<shim-dir se presente>:<path_prepend joinati con ":">:<PATH base>`.

**Implementazione.**

1. `internal/config/types.go`, struct `EnvConfig`: aggiungere
   ``PathPrepend []string `toml:"path_prepend"` `` con doc comment che ne descrive semantica e ordine.
2. `internal/config/merge.go` (sezione `[env]`, righe 72–91): merge con
   `meta.IsDefined("env", "path_prepend")` e unione senza duplicati in cui **le voci
   dell'overlay precedono quelle del base** (`mergeUnique(overlay.Env.PathPrepend, base.Env.PathPrepend)`),
   così il profilo più derivato vince nella risoluzione dei binari. Documentare questa scelta nel commento.
3. `internal/config/loader.go`, `toRunConfig` (righe 512–520): applicare `ExpandPath` a ogni
   elemento di `PathPrepend`, come già fatto per `Env.Set`.
4. `internal/isolator/bwrap.go`: estrarre il calcolo del PATH base in un'unica funzione,
   oggi duplicato/implicito nel blocco shim (righe 342–351):
   - base = `cfg.Env.Set["PATH"]` se presente; altrimenti `os.Getenv("PATH")` se `PATH` è in
     `cfg.Env.Inherit` o `cfg.Env.InheritAll` è true; altrimenti `/usr/local/bin:/usr/bin:/bin`.
   - se `len(cfg.Env.PathPrepend) > 0`: finale = `strings.Join(PathPrepend, ":") + ":" + base`,
     emesso come `--setenv PATH` **dopo** il loop di `Env.Set` (righe 231–238), così vince sul set esplicito.
   - il blocco shim antepone `/tmp/inner-shims:` a questo valore finale (gli shim devono restare primi).
5. `internal/profile/validator.go`: nuovo step — per ogni elemento di `path_prepend` espanso,
   se `os.Stat` fallisce con `IsNotExist` → `addWarning` (non error: il path potrebbe esistere
   solo in sandbox via mount).

**Casi limite.** Elementi vuoti in lista → errore di validazione; `path_prepend` senza `PATH`
né in `set` né in `inherit` → si usa il default hard-coded; interazione con
`inherit_all = true` → base = PATH host.

**Test.**

- `merge_test`: overlay+base → ordine "overlay prima", niente duplicati.
- `loader_test`: espansione `~` e `${VAR}` negli elementi.
- `bwrap_test`:
  - (a) solo `path_prepend` → `--setenv PATH` con prepend+PATH host;
  - (b) `path_prepend` + `Set["PATH"]` → prepend + valore set;
  - (c) `path_prepend` + shim dir → PATH inizia con `/tmp/inner-shims:` seguito dal prepend;
  - (d) senza `path_prepend` → comportamento identico a oggi (regressione).
- `validator_test`: warning su directory inesistente.

**Doc.** `docs/content/profiles.md` sezione `[env]`: nuovo campo, semantica di merge con
`extends`, esempio JDK pinning.

**Accettazione.** Con profilo contenente solo `path_prepend = ["/opt/jdk/jdk-21/bin"]` e
`inherit = [..., "PATH", ...]`, dentro la sandbox `command -v java` →
`/opt/jdk/jdk-21/bin/java` e il resto del PATH host è preservato. Tutti i test esistenti verdi.

---

## T2 — Espandere le variabili nei valori di `-e/--env` come nei profili

**Tipo:** fix di coerenza · **Size:** S

**Comportamento attuale.** I valori di `[env] set` nei profili passano per `config.ExpandPath`
(`internal/config/loader.go:518`); i valori del flag `-e KEY=VAL` no: in
`cmd/inner/cmd_run.go:440-449` il valore di `parseEnvVar` è assegnato raw a `rc.Env.Set[k]`.
Risultato: `-e 'PATH=/opt/jdk/bin:${PATH}'` (quoting singolo) mette la stringa letterale
`${PATH}` nella sandbox.

**Comportamento atteso.** Stessa semantica del profilo: in `cmd_run.go:448` applicare
`rc.Env.Set[k] = config.ExpandPath(v)`.

**Nota di design (decisione presa, non riaprire):** si sceglie la coerenza col profilo.
Valori contenenti `$` letterale non sono supportati né qui né in `[env] set` — va documentato,
non gestito con escaping.

**Test.** In `cmd_run_test.go`:

- (a) `-e 'X=${INNER_TEST_VAR}/sub'` con var host impostata → valore espanso;
- (b) `-e X=plain-value` → invariato;
- (c) `-e 'X=~/dir'` — attenzione: `ExpandPath` espande `~` solo a inizio stringa;
  verificare e asserire il comportamento reale.

**Doc.** `docs/content/commands.md`, help del flag (`cmd_run.go:866`): aggiornare in
`"Set env variable: KEY=VAL (~, $VAR and ${VAR} are expanded against the host environment)"`.

**Accettazione.** `inner run --dry-run -e 'JH=${HOME}/x'` mostra `--setenv JH /home/<user>/x`.

---

## T3 — Validator: warning su riferimenti a variabili host non definite in `[env] set`

**Tipo:** feature (validazione) · **Size:** S/M

**Comportamento attuale.** `ExpandPath` (`internal/config/expand.go:13`) usa `os.Getenv`
dentro `os.Expand`: una variabile non definita si espande a stringa vuota, in silenzio.
`set = { JAVA_HOME = "${INNER_JDK}" }` con `INNER_JDK` non impostata produce `JAVA_HOME=""`
e nessun segnale.

**Comportamento atteso.** `inner profile validate` (e ogni percorso che usa `profile.Validate`)
emette un warning per ogni variabile referenziata in un valore di `[env] set`
(e in `path_prepend`, se T1 è già mergiato) non definita nell'ambiente host.

**Implementazione.**

1. Nuova funzione in `internal/config/expand.go`:

   ```go
   // UndefinedVarRefs returns the names of $VAR/${VAR} references in s that are
   // not defined in the host environment. "UID" is always considered defined.
   func UndefinedVarRefs(s string) []string
   ```

   Implementata con `os.Expand` e una mapping func che registra i nomi per cui `os.LookupEnv`
   restituisce `ok == false` (scartando il valore di ritorno) — così il parsing dei nomi resta
   identico a quello dell'espansione reale. Trattare come "definiti" anche i token `workdir`
   e `workspaces_path`.
2. `internal/profile/validator.go`, nuovo step dopo lo step 2: per ogni `k, v` in `p.Env.Set`,
   per ogni nome restituito da `config.UndefinedVarRefs(v)`:
   `addWarning(fmt.Sprintf("[env] set %s references undefined host variable $%s (expands to empty string)", k, name))`.

**Livello: warning, non error** — un profilo parametrico può essere validato su una macchina
dove la variabile non serve.

**Test.** `expand_test.go`: casi `$FOO`, `${FOO}`, misti definiti/non, `$UID` mai segnalata,
stringa senza `$` → nil. `validator_test.go`: profilo con var non definita → un warning col
nome giusto; con var definita via `t.Setenv` → nessun warning.

**Accettazione.** `inner profile validate java` su macchina senza `INNER_JDK` stampa il
warning; con `INNER_JDK` esportata non stampa nulla.

---

## T4 — `[env] inherit`: non inoltrare come stringa vuota le variabili assenti sull'host

**Tipo:** bugfix · **Size:** XS

**Comportamento attuale.** `internal/isolator/bwrap.go:226-228`:

```go
for _, key := range cfg.Env.Inherit {
    args = append(args, "--setenv", key, os.Getenv(key))
}
```

Se la variabile non esiste sull'host viene comunque emesso `--setenv KEY ""`: nella sandbox
risulta *impostata ma vuota*. Con `inherit = ["JAVA_HOME"]` (profili contrib
`java-maven`/`java-gradle`) e host senza `JAVA_HOME`, tool che distinguono unset da vuota
si comportano male.

**Comportamento atteso.** Variabile **non definita** sull'host → nessun `--setenv`
(resta unset in sandbox, dato `--clearenv`). Variabile **definita ma vuota** → inoltrata
vuota (comportamento invariato: `os.LookupEnv` con `ok == true`).

**Implementazione.** Sostituire con:

```go
for _, key := range cfg.Env.Inherit {
    if val, ok := os.LookupEnv(key); ok {
        args = append(args, "--setenv", key, val)
    }
}
```

**Test.** In `bwrap_test.go`:

- (a) var non definita in `Inherit` → nessuna tripla `--setenv KEY` negli args;
- (b) var definita a `""` via `t.Setenv` → `--setenv KEY ""` presente;
- (c) regressione sul caso normale (già coperto da `INNER_TEST_KEY`, riga 338).

**Accettazione.** `inner run --dry-run -p java-maven` su host senza `JAVA_HOME` non contiene
`--setenv JAVA_HOME`.

---

## T5 — Validator: warning per valori di `[env] set` che sono path assoluti inesistenti

**Tipo:** feature (validazione) · **Size:** S

**Comportamento attuale.** Il validator (`internal/profile/validator.go`) verifica esistenza
di mount src/dest ed entrypoint, ma `set = { JAVA_HOME = "/opt/jdk/jdk-99" }` con directory
inesistente passa senza segnalazioni; l'errore emerge solo al primo comando in sandbox.

**Comportamento atteso.** Warning per i valori di `[env] set` che, dopo espansione, sembrano
path assoluti e non esistono sull'host.

**Regola precisa (per evitare falsi positivi):** un valore è candidato al check se e solo se,
dopo `ExpandPath`, TUTTE queste condizioni valgono:

1. inizia con `/`;
2. non contiene `://` (esclude `unix:///...`, URL);
3. non contiene `:` (esclude valori PATH-like multipli);
4. non contiene i token `${workspaces_path}` o `${workdir}` nel valore originale;
5. non coincide con il `dest` espanso di alcun mount del profilo (path che esistono solo in sandbox).

Se candidato e `os.Stat` fallisce con `IsNotExist` →
`addWarning(fmt.Sprintf("[env] set %s=%q: path does not exist on host", k, expanded))`.
Altri errori di `Stat` (permessi) → nessuna segnalazione. Sempre **warning**, mai error.

**Implementazione.** Nuovo step in `Validate` dopo il check dei mount, riusando la mappa
`expandedMountDests` già costruita per le capabilities (righe 173–178: spostarne la
costruzione prima, se serve).

**Test.** `validator_test.go`: JAVA_HOME inesistente → warning; JAVA_HOME esistente
(dir temporanea) → nessun warning; `DOCKER_HOST=unix:///...` → nessun warning; valore con
`:` (PATH-like) → nessun warning; valore uguale a un mount dest → nessun warning.

**Accettazione.** Profilo con `JAVA_HOME = "/opt/jdk/jdk-99"` → `inner profile validate`
stampa il warning; con path esistente non stampa nulla.

---

## T6 — Profilo contrib di esempio: JDK pinning (`java-21.toml`)

**Tipo:** docs/esempio · **Size:** XS · **Dipendenza:** meglio dopo T1 (usa `path_prepend`);
se T1 non è mergiato, usare la variante `${PATH}` indicata sotto.

**Contesto.** `contrib/profiles/java-maven.toml` e `java-gradle.toml` fanno solo
`inherit = ["JAVA_HOME"]`: non c'è alcun esempio che mostri come fissare una versione precisa
di Java, né garanzia che il `java` in PATH coincida con `JAVA_HOME`.

**Deliverable.** Nuovo file `contrib/profiles/java-21.toml`:

```toml
schema_version = "1"
name        = "java-21"
description = "Java shell pinned to JDK 21 (adjust the /opt/jdk path to your machine)"
extends     = "shell"

[env]
# Con T1 mergiato:
# path_prepend = ["/opt/jdk/jdk-21/bin"]
# set = { JAVA_HOME = "/opt/jdk/jdk-21" }
# Senza T1 (variante attuale — ${PATH} è espanso a load time col PATH dell'host):
set = { JAVA_HOME = "/opt/jdk/jdk-21", PATH = "/opt/jdk/jdk-21/bin:${PATH}" }

[verify.custom]
checks = [
  { name = "java resolves to pinned JDK", cmd = "test \"$(readlink -f \"$(command -v java)\")\" = \"$(readlink -f /opt/jdk/jdk-21/bin/java)\"", severity = "critical" },
  { name = "JAVA_HOME matches pinned JDK", cmd = "test \"$JAVA_HOME\" = /opt/jdk/jdk-21", severity = "high" },
]
```

Nel file finale tenere solo UNA delle due varianti `[env]`, secondo lo stato di T1;
il commento sull'altra va rimosso.

**Doc.**

- `docs/content/examples.md`: nuova sezione "Pinning a specific JDK version" che mostra il
  profilo, l'uso di `inner verify`, e l'override one-shot:
  `inner run -p java-21 -e JAVA_HOME=/opt/jdk/jdk-26 -e "PATH=/opt/jdk/jdk-26/bin:$PATH"`.
- `docs/content/profiles.md`: nella sezione `[env]`, documentare esplicitamente che i valori
  di `set` sono espansi contro l'ambiente host a load time (`~`, `$VAR`, `${VAR}`, `$UID`).

**Accettazione.** Su una macchina con `/opt/jdk/jdk-21`: `inner run -p java-21` →
`java -version` riporta 21; `inner verify -p java-21` passa entrambi i check custom.
Il profilo passa `inner profile validate`.

---

## T7 — Correggere il commento fuorviante su `[noop]` in `types.go`

**Tipo:** docs/chore · **Size:** XS

**Comportamento attuale.** `internal/config/types.go:107`:
*"A user-declared [noop] section replaces the built-in defaults entirely."*
Ma nel codice non esistono default built-in di noop (nessun `DefaultNoop`;
`internal/shim/builder.go` costruisce solo da ciò che è nel profilo), e con `extends`
il merge è **additivo**: `block` in union, `rewrite` con override per chiave
(`internal/config/merge.go`, sezione noop, righe ~144–162).

**Deliverable.**

1. Verificare il comportamento reale del merge noop leggendo `merge.go` e i test
   (`loader_test.go:664-668`).
2. Riscrivere il doc comment di `NoopConfig` in `types.go:104-111` in modo accurato, ad es.:
   *"There are no built-in noop defaults. With extends, block lists are unioned and rewrite
   maps are merged per key, with the child profile overriding the base."*
3. Allineare la sezione `[noop]` di `docs/content/profiles.md` se riporta la stessa
   affermazione errata (verificare con grep su "built-in" / "replaces").

**Accettazione.** Commento e doc descrivono il comportamento effettivamente implementato in
`merge.go`; nessun cambiamento di codice eseguibile in questo task.
